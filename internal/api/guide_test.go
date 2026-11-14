package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
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
	if c := ov["caller"].(map[string]any); c["kind"] != "admin" || fmt.Sprint(c["scopes"]) != "[admin session task ticket observe act]" {
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
		limits["check_timeout"]["min"] != float64(1) || limits["check_timeout"]["default"] != float64(600) || limits["comment"]["max"] != float64(16<<10) || limits["inbox"]["max"] != float64(50) || limits["task_id"]["pattern"] != `^[a-z0-9][a-z0-9_-]{0,39}$` {
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
		// A Brakeman turn's reads are routes of their own, outside
		// /api/sessions/{id} (TestEveryOperationIsARegisteredRoute).
		if op.Scope != "admin" && strings.HasPrefix(op.Path, "/api/sessions/{id}") && !registered[key] {
			t.Errorf("%s: %s is no route the server registers", op.Name, key)
		}
	}
}

// encodedLen is the bytes s takes inside a JSON string as the guide
// writes it, HTML escaping off: a control character its six-byte escape.
func encodedLen(t *testing.T, s string) int {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	return b.Len() - len(`""`) - len("\n")
}

// Every operation's summary and description fit their bounds as the
// guide encodes them, so a summary at its bound in
// TestEachSectionIsBounded, ASCII that encodes as written, is the
// longest the table can hold.
func TestEveryOperationFitsItsBounds(t *testing.T) {
	if n := encodedLen(t, "a\x01\u2028\"<"); n != 1+6+6+2+1 {
		t.Fatalf("encodedLen: %d", n)
	}
	for _, op := range Operations() {
		if n := encodedLen(t, op.Summary); n > maxOpSummary || op.Summary == "" {
			t.Errorf("%s: a summary of %d bytes encoded, over %d", op.Name, n, maxOpSummary)
		}
		if n := encodedLen(t, op.Description); n > maxOpDescription {
			t.Errorf("%s: a description of %d bytes encoded, over %d", op.Name, n, maxOpDescription)
		}
	}
}

// The largest guide the table can make: every operation's summary and
// description at their bounds as encoded (TestEveryOperationFitsItsBounds),
// more agents than the host section lists
// with their ids and adapters at their bounds and their names at theirs
// in the rune encoding/json writes longest (a control one, six bytes;
// U+2028 is as long but a space, and a name is not blank), the
// ledger's and Dispatch's reasons at their 500 bytes and each agent's
// version at its 64, both in the bytes it writes longest (a control
// byte, six), a session caller of every kind, keeps each section, the
// overview and the whole within their bounds.
func TestEachSectionIsBounded(t *testing.T) {
	e := newTestEnv(t, nil)
	var list []catalog.Agent
	for i := range maxGuideAgents + 10 {
		list = append(list, catalog.Agent{ID: fmt.Sprintf("%032d", i), Name: strings.Repeat(string(rune(1)), 60), Adapter: strings.Repeat("a", 32), Command: []string{"/bin/cat"}})
	}
	if b, _ := json.Marshal(list[0].Name); len(b) != 2+60*6 {
		t.Fatalf("a name encodes to %d bytes", len(b))
	}
	worst := func(n int) string { return strings.Repeat(string(rune(1)), n) }
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
	callers := []guideCaller{adminCaller(), {Kind: "session", Session: long, Kinds: []string{"run_member", "attempt", "ticket_member"}, Run: long, Member: strings.Repeat("m", 40), Scopes: []string{"session", "task", "ticket"}, local: true},
		// A Brakeman turn granted every operation, its budget at its
		// largest.
		{Kind: "session", Session: long, Kinds: []string{"run_member", "brakeman"}, Run: long, Member: strings.Repeat("m", 40), Scopes: []string{"session", "observe", "act"}, local: true, brakeman: true,
			acts: brakeman.Grantable, budget: brakeman.Budget{TurnMinutes: 60, ReadsPerTurn: 2000, TurnsPerDay: 500, MinutesPerDay: 1440}}}
	for _, c := range callers {
		content := e.srv.guideContent(t.Context(), c, ops)
		h := content["host"].(guideHost)
		if len(h.Agents) != maxGuideAgents {
			t.Fatalf("%s's host: %d agents", c.Kind, len(h.Agents))
		}
		// What the server cuts to its bounds, at them: a reason the admin
		// alone reads, a version every caller that sees the agents does.
		if c.Kind == "admin" {
			h.Ledger = guideStore{State: "read_only", Why: worst(500)}
			h.Dispatch = guideStore{State: "read_only", Why: worst(500)}
		}
		for i := range h.Agents {
			h.Agents[i].Version = worst(64)
		}
		// C9's fullest reply, every provider starting, in both sections
		// that name it.
		caps := fullestCapabilities(t)
		h.Extensions = extensionKinds(caps)
		content["providers"] = guideProvidersOf(caps)
		content["host"] = h
		replies, err := encodeGuide(c, content)
		if err != nil {
			t.Fatal(err)
		}
		whole := 0
		for name, b := range replies {
			whole += len(b)
			limit := guideBound(name)
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
// managed run the task tools too, and no add_member; a member of a run a
// ticket holds the ticket's two. The admin reads a session's guide as the
// session's.
func TestASessionsGuideListsItsScopesOnly(t *testing.T) {
	// A session in no run may form a crew; one in a run may add to it and
	// read it, and forms none (409 in_a_run); one in a managed run only
	// reads it, the run making its members itself (409 managed_run).
	plainOps := []string{"report", "set_state", "ask", "form_crew", "link", "guide", "open_url"}
	memberOps := []string{"report", "set_state", "ask", "add_member", "run_status", "link"}
	attemptOps := []string{"report", "set_state", "ask", "run_status", "link"}
	taskOps := []string{"task_context", "task_result", "message_send", "inbox_read", "inbox_ack", "task_blocked", "task_checkpoint", "decision_request", "decision_list", "decision_read", "decision_withdraw"}
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
		check(t, e, sid, e.agentToken(sid), []string{"plain"}, plainOps)
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
		check(t, e, sid, e.agentToken(sid), []string{"run_member", "attempt"}, slices.Concat(attemptOps, taskOps, []string{"guide", "open_url"}))
		// What the guide leaves out, the route refuses.
		status, data, out := e.callAs(t, e.agentToken(sid), http.MethodPost, "/api/sessions/"+sid+"/run/members", []byte(`{"name":"extra","agentId":"ready-cat","prompt":"","start":{"when":"immediately"}}`))
		if status != http.StatusConflict || errCodeOf(out) != "managed_run" {
			t.Fatalf("add_member in a managed run: %d %s", status, data)
		}
	})
	t.Run("ticket", func(t *testing.T) {
		e := dispatchEnv(t)
		run, sid, tok := e.memberSession(t, "Ticketed")
		check(t, e, sid, tok, []string{"run_member"}, slices.Concat(memberOps, []string{"guide", "open_url"}))
		e.newDispatchProject(t, "GDE", nil)
		key := e.newDispatchTicket(t, "GDE", "Guided", nil)["key"].(string)
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
		check(t, e, sid, tok, []string{"run_member", "ticket_member"}, slices.Concat(memberOps, ticketOps, []string{"guide", "open_url"}))
	})
	t.Run("self-service off", func(t *testing.T) {
		e := newTestEnv(t, func(c *config.Config) { off := false; c.Agents.SelfService = &off })
		sid := e.createSession("cat")
		check(t, e, sid, e.agentToken(sid), []string{"plain"}, []string{"report", "set_state", "ask", "guide"})
	})
}

// The overview says what the caller may do and within what, in a few
// lines, without a section read: the operations it may call (the
// operations section's names, in its order) and the headline limits that
// bind it, each its own. A real Codex told only "call the guide tool"
// answered with the version, its scope and the sections' names, which
// was all the overview said.
func TestTheOverviewListsWhatTheCallerMayDoAndItsLimits(t *testing.T) {
	overview := func(t *testing.T, e *testEnv, token, path string) (may, limits []string, ops []string) {
		t.Helper()
		status, raw, out := guideOf(t, e, token, path)
		if status != http.StatusOK || len(raw) > maxGuideOverview {
			t.Fatalf("overview %d, %d bytes", status, len(raw))
		}
		ov := out["overview"].(map[string]any)
		for _, x := range ov["may"].([]any) {
			may = append(may, x.(string))
		}
		for _, x := range ov["limits"].([]any) {
			limits = append(limits, x.(string))
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		_, _, sec := guideOf(t, e, token, path+sep+"section=operations")
		return may, limits, opNames(t, sec)
	}
	has := func(limits []string, prefix string) bool {
		return slices.ContainsFunc(limits, func(l string) bool { return strings.HasPrefix(l, prefix) })
	}
	t.Run("admin", func(t *testing.T) {
		e := newTestEnv(t, nil)
		may, limits, ops := overview(t, e, adminToken, "/api/guide")
		if !slices.Equal(may, ops) || !slices.Contains(may, "run_stop") || !has(limits, "crew_members: at most") || !has(limits, "body: at most") {
			t.Fatalf("the admin's overview: may %v, limits %v", may, limits)
		}
	})
	t.Run("session", func(t *testing.T) {
		e := newTestEnv(t, nil)
		sid := e.createSession("cat")
		may, limits, ops := overview(t, e, e.agentToken(sid), "/api/sessions/"+sid+"/guide")
		if !slices.Equal(may, ops) || !slices.Contains(may, "ask") || slices.Contains(may, "run_stop") || slices.Contains(may, "observe") ||
			!has(limits, "report_message: at most") || has(limits, "brief:") || has(limits, "crew_members") || !has(limits, "body: at most") {
			t.Fatalf("a session's overview: may %v, limits %v", may, limits)
		}
	})
	for _, granted := range []bool{false, true} {
		t.Run(fmt.Sprintf("a Brakeman turn granted %v", granted), func(t *testing.T) {
			bk := brakemanEnv(t, nil)
			bk.enable(t, "bk", func(c map[string]any) {
				act := []any{}
				if granted {
					act = []any{brakeman.ActLaunchApprovedPlan}
				}
				p := c["permissions"].(map[string]any)
				p["act"] = act
				c["budget"].(map[string]any)["readsPerTurn"] = 123
			})
			req := bk.briefing(t)
			sid, tok, _ := bk.turnSession(t, req)
			awaitRunMember(t, bk, sid)
			may, limits, ops := overview(t, bk.testEnv, tok, "/api/sessions/"+sid+"/guide")
			if !slices.Equal(may, ops) || !slices.Contains(may, "observe") || !slices.Contains(may, "brief_submit") || slices.Contains(may, "form_crew") ||
				!has(limits, "brief: at most") || !has(limits, "this turn: at most 123 reads and") {
				t.Fatalf("the turn's overview: may %v, limits %v", may, limits)
			}
			if slices.Contains(may, "act") != granted || slices.Contains(may, brakeman.ActLaunchApprovedPlan) != granted || has(limits, "acts: at most") != granted {
				t.Fatalf("the turn's overview, granted %v: may %v, limits %v", granted, may, limits)
			}
		})
	}
}

// The overview leads with what the caller may call and the limits it
// keeps, together, after the sentence that asks for both when the agent
// is asked what it can do (#726: a real Codex told only to call the guide
// named the operations and left the limits unsaid): report, may and
// limits are the overview's first fields after the caller, ahead of the
// sections and the rules, for a crew's member and a session of no crew.
func TestTheOverviewLeadsWithWhatTheCallerMayCallAndItsLimits(t *testing.T) {
	e := dispatchEnv(t)
	_, member, memberTok := e.memberSession(t, "Led")
	plain := e.createSession("cat")
	for _, c := range []struct{ sid, tok, op string }{{member, memberTok, "run_status"}, {plain, e.agentToken(plain), "form_crew"}} {
		status, raw, out := guideOf(t, e, c.tok, "/api/sessions/"+c.sid+"/guide")
		if status != http.StatusOK {
			t.Fatalf("%s's overview: %d %s", c.sid, status, raw)
		}
		ov := out["overview"].(map[string]any)
		may, _ := ov["may"].([]any)
		limits, _ := ov["limits"].([]any)
		if ov["report"] != guideReport || !slices.Contains(may, any(c.op)) || len(limits) == 0 {
			t.Fatalf("%s's overview: %v", c.sid, ov)
		}
		// The overview's fields in the order they are written.
		var env struct{ Overview json.RawMessage }
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var keys []string
		dec := json.NewDecoder(bytes.NewReader(env.Overview))
		if _, err := dec.Token(); err != nil {
			t.Fatal(err)
		}
		for dec.More() {
			k, err := dec.Token()
			var v json.RawMessage
			if err != nil || dec.Decode(&v) != nil {
				t.Fatalf("%s's overview: %v %s", c.sid, err, env.Overview)
			}
			keys = append(keys, k.(string))
		}
		if want := []string{"caller", "report", "may", "limits", "sections", "rules"}; !slices.Equal(keys, want) {
			t.Fatalf("%s's overview's fields %v, want %v", c.sid, keys, want)
		}
	}
	for _, w := range []string{"what you can do", "both", "operations you may call", "limits you must keep"} {
		if !strings.Contains(guideReport, w) {
			t.Fatalf("the sentence %q says nothing of %q", guideReport, w)
		}
	}
}

// awaitRunMember waits until the run of the Brakeman turn's session sid
// counts it a member. The session is registered (turnSession) before its
// run counts it one, which is what run_status needs and the guide asks
// (sessionCaller): a guide read either side of that moment lists other
// operations, so a test that compares two reads of it waits first.
func awaitRunMember(t *testing.T, bk *bkEnv, sid string) {
	t.Helper()
	if !waitFor(30*time.Second, func() bool { _, _, ok := bk.srv.runs.MemberOf(sid); return ok }) {
		t.Fatalf("%s never became its run's member", sid)
	}
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

// guideProvidersAndHost reads the providers and host sections of the
// guide at path with token: their content, decoded as a reader would,
// refusing a field the providers section does not have.
func guideProvidersAndHost(t *testing.T, e *testEnv, token, path string) (guideProviders, []string) {
	t.Helper()
	status, raw, _ := guideOf(t, e, token, path+"?section=providers")
	if status != http.StatusOK {
		t.Fatalf("%s providers: %d %s", path, status, raw)
	}
	var reply struct {
		Guide     int            `json:"guide"`
		Version   string         `json:"version"`
		Commit    string         `json:"commit"`
		Section   string         `json:"section"`
		Providers guideProviders `json:"providers"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("%s providers %s: %v", path, raw, err)
	}
	if reply.Providers.Providers == nil {
		t.Fatalf("%s providers %s: no list", path, raw)
	}
	status, raw, _ = guideOf(t, e, token, path+"?section=host")
	if status != http.StatusOK {
		t.Fatalf("%s host: %d %s", path, status, raw)
	}
	var host struct {
		Host struct {
			Extensions []string `json:"extensions"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &host); err != nil || host.Host.Extensions == nil {
		t.Fatalf("%s host %s: no extensions (%v)", path, raw, err)
	}
	return reply.Providers, host.Host.Extensions
}

// A plain build's guide knows its providers, and there are none: the
// providers section is {known: true, providers: []} and the host's
// extensions [], for the admin and for a session, as GET
// /api/capabilities answers {"providers": []}.
func TestAnEmptyRegistryIsKnownAndEmpty(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.StartExtensions(t.Context())
	sid := e.createSession("cat")
	if code, body := getCapabilities(t, e, adminToken); code != http.StatusOK || strings.TrimSpace(body) != `{"providers":[]}` {
		t.Fatalf("the capabilities: %d %s", code, body)
	}
	for path, tok := range map[string]string{"/api/guide": adminToken, "/api/sessions/" + sid + "/guide": e.agentToken(sid)} {
		status, raw, _ := guideOf(t, e, tok, path+"?section=providers")
		if status != http.StatusOK || !strings.Contains(string(raw), `"providers":{"known":true,"providers":[]}`) {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		if _, kinds := guideProvidersAndHost(t, e, tok, path); len(kinds) != 0 {
			t.Fatalf("%s: a plain build's host names the extensions %q", path, kinds)
		}
	}
}

// The guide's providers section is GET /api/capabilities's rows, the
// same for the admin and for a session, each provider's kind, name and
// state (one still starting said so, then ready once its Start
// succeeded, with no restart), and the host names their kinds, each
// once. The providers are package-local fakes of these tests: a value in
// two slots whose first Start fails (cloudSlots) and an entitlement
// source with no Start (plansOnly), which the guide names by kind and
// name and asks nothing.
func TestTheGuideListsC9sProviders(t *testing.T) {
	cloud := &cloudSlots{}
	p := ext.Providers{
		Identity: cloud, Media: cloud,
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "cloud", Value: cloud},
			{Kind: ext.KindEntitlements, Name: "plans", Value: plansOnly{}},
			{Kind: ext.KindMedia, Name: "cloud", Value: cloud},
		},
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithExtensions(local.Fill(p)))
	clock := newFakeClock(e.srv)
	sid := e.createSession("cat")
	paths := map[string]string{"/api/guide": adminToken, "/api/sessions/" + sid + "/guide": e.agentToken(sid)}
	check := func(when, cloudState string) {
		t.Helper()
		code, body := getCapabilities(t, e, adminToken)
		if code != http.StatusOK {
			t.Fatalf("%s: the capabilities %d %s", when, code, body)
		}
		rows := capabilityRows(t, body)
		want := []capabilityRow{
			{Kind: ext.KindIdentity, Name: "cloud", State: cloudState},
			{Kind: ext.KindEntitlements, Name: "plans", State: "ready"},
			{Kind: ext.KindMedia, Name: "cloud", State: cloudState},
		}
		if !reflect.DeepEqual(rows, want) {
			t.Fatalf("%s: the capabilities %+v, want %+v", when, rows, want)
		}
		for path, tok := range paths {
			got, kinds := guideProvidersAndHost(t, e, tok, path)
			if !got.Known || !reflect.DeepEqual(got.Providers, rows) {
				t.Fatalf("%s: %s's providers %+v, want known and %+v", when, path, got, rows)
			}
			if want := []string{ext.KindIdentity, ext.KindEntitlements, ext.KindMedia}; !reflect.DeepEqual(kinds, want) {
				t.Fatalf("%s: %s's host extensions %q, want %q", when, path, kinds, want)
			}
		}
	}
	check("before the first Start", "starting")
	e.srv.StartExtensions(t.Context())
	clock.next(t, ext.StartWait)
	backoff := clock.next(t, extStartFirst)
	check("after a Start that failed", "starting")
	backoff.fire <- time.Now()
	clock.next(t, ext.StartWait)
	waitState(t, e.srv, "cloud", extStarted)
	check("once a Start succeeded", "ready")
	if n := cloud.starts.Load(); n != 2 {
		t.Fatalf("cloud was started %d times, want 2: the guide asks a provider nothing", n)
	}
}

// fill sets every field of v it can set (an unexported embedded struct's
// fields it cannot, through reflect), at every depth, to a value that is
// not its zero, so encoding/json writes each one.
func fill(v reflect.Value) {
	switch {
	case v.Type() == reflect.TypeFor[json.RawMessage]():
		v.SetBytes([]byte("{}"))
		return
	case v.Type() == reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(time.Unix(1, 0)))
		return
	case v.Type() == reflect.TypeFor[brakeman.Ref]():
		ref, _ := brakeman.ParseRef("ticket:AB-1")
		v.Set(reflect.ValueOf(ref))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			// A field the guide leaves out is the server's, which a body
			// sending it is refused (crew.Member.Bound): none is sent.
			if v.Field(i).CanSet() && v.Type().Field(i).Tag.Get("guide") != "-" {
				fill(v.Field(i))
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	}
}

// encodedOf is v as encoding/json writes it, read back as JSON values.
func encodedOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// fullOf is what encoding/json writes for a value of typ with every field
// filled: the fields of an unexported embedded struct, which reflect
// cannot set through its parent, are its type's own encoding, filled.
func fullOf(t *testing.T, typ reflect.Type) map[string]any {
	t.Helper()
	v := reflect.New(typ).Elem()
	fill(v)
	out := encodedOf(t, v.Interface())
	for i := range typ.NumField() {
		f := typ.Field(i)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if f.Anonymous && ft.Kind() == reflect.Struct && !v.Field(i).CanSet() {
			maps.Copy(out, fullOf(t, ft))
		}
	}
	return out
}

// matchesEncoding fails t unless schema describes v, a value encoding/json
// wrote with every field filled: an object's properties its keys and no
// other property taken, at every depth, and each value of its type.
func matchesEncoding(t *testing.T, at string, schema map[string]any, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		props, ok := schema["properties"].(map[string]any)
		if schema["type"] != "object" || (!ok && len(x) > 0) {
			t.Errorf("%s: an object of %v, the schema says %v", at, slices.Sorted(maps.Keys(x)), schema)
			return
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s: the route takes no other property, the schema %v", at, schema)
		}
		if got, want := slices.Sorted(maps.Keys(props)), slices.Sorted(maps.Keys(x)); !slices.Equal(got, want) {
			t.Errorf("%s: the schema's properties %v, encoding/json's fields %v", at, got, want)
		}
		for k, p := range props {
			if val, ok := x[k]; ok {
				matchesEncoding(t, at+"."+k, p.(map[string]any), val)
			}
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		if schema["type"] != "array" || items == nil || len(x) != 1 {
			t.Errorf("%s: an array, the schema says %v", at, schema)
			return
		}
		matchesEncoding(t, at+"[]", items, x[0])
	case string:
		if schema["type"] != "string" {
			t.Errorf("%s: a string, the schema says %v", at, schema)
		}
	case float64:
		if schema["type"] != "integer" && schema["type"] != "number" {
			t.Errorf("%s: a number, the schema says %v", at, schema)
		}
	case bool:
		if schema["type"] != "boolean" {
			t.Errorf("%s: a boolean, the schema says %v", at, schema)
		}
	}
}

// Each admin operation's input is the schema of the type its route
// decodes, as encoding/json reads that type: every field it writes for a
// value with all of them filled is a property (an unexported embedded
// struct's opId, expectedRev and byName among them, a nested struct's
// fields its object's), no other, and the fields it writes for the zero
// value (no omitempty, no omitzero) are the required ones.
func TestEachBodySchemaIsWhatItsRouteDecodes(t *testing.T) {
	bodies := map[string]any{
		"run_pause": crew.Command{}, "run_unpause": crew.Command{}, "task_cancel": crew.Command{}, "task_retry": crew.Command{}, "task_accept": crew.Command{}, "task_recheck": recheckBody{},
		"ticket_create": ticketCreateBody{}, "ticket_edit": ticketEditBody{}, "ticket_comment_admin": commentBody{}, "ticket_link": linkBody{},
		"crew_create": crewInput{}, "crew_launch": launchBody{},
		"brakeman_config": brakemanConfigBody{}, "brakeman_ask": brakemanAskBody{}, "brakeman_cancel": brakemanCancelBody{},
		"accounts_add": addAccountBody{}, "accounts_rename": renameAccountBody{}, "accounts_retire": accountRevBody{}, "accounts_default": accountDefaultBody{}, "capacity_read": capacityReadBody{},
		// A Brakeman turn's granted operations take their routes' own bodies.
		"launch_approved_plan": ticketLaunchBody{}, "prepare_followup": planProposeBody{},
		// The operator's operations on a decision.
		"decision_respond": crew.Response{}, "decision_cancel": decisionCancelBody{}, "decision_comment": decisionCommentBody{}, "decision_route": decisionRouteBody{},
		// A resume from a session's record.
		"history_resume": historyResumeBody{},
		// A person's reviews.
		"review_submit": reviewSubmitBody{}, "review_retry": reviewRetryBody{}, "finding_mark": findingMarkBody{},
		"tracker_set": trackerSaveBody{},
		// A ticket's GitHub item.
		"ticket_import_preview": externalPreviewBody{}, "ticket_import": externalImportBody{}, "ticket_link_issue": externalLinkBody{},
		"ticket_unlink_issue": dispatchOp{}, "ticket_refresh": externalRefreshBody{}, "ticket_authority": externalAuthorityBody{}, "link_read": linkReadBody{},
		// A ticket's publications.
		"publication_prepare": publicationBody{}, "publication_send": publicationSendBody{}, "publication_check": publicationCheckBody{},
		"publication_close": dispatchOp{},
	}
	seen := 0
	for _, op := range Operations() {
		v, ok := bodies[op.Name]
		if !ok {
			if op.Scope == "admin" && op.Input["properties"] != nil {
				t.Errorf("%s: its input names properties, and no body type holds them here", op.Name)
			}
			continue
		}
		seen++
		if !reflect.DeepEqual(op.Input, bodySchema(v)) {
			t.Errorf("%s: its input is not its body's schema", op.Name)
		}
		// Through JSON, as a client reads it.
		raw, err := json.Marshal(op.Input)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		matchesEncoding(t, op.Name, schema, fullOf(t, reflect.TypeOf(v)))
		var required []string
		if r, ok := schema["required"].([]any); ok {
			for _, x := range r {
				required = append(required, x.(string))
			}
		}
		if want := slices.Sorted(maps.Keys(encodedOf(t, v))); !slices.Equal(required, want) {
			t.Errorf("%s: required %v, encoding/json writes %v for the zero value", op.Name, required, want)
		}
	}
	if seen != len(bodies) {
		t.Fatalf("%d of the %d bodies are the table's", seen, len(bodies))
	}
}

// fitsSchema fails t unless body is one schema describes: its keys among
// the properties, every required one there, at every depth.
func fitsSchema(t *testing.T, at string, schema, body map[string]any) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	for k, v := range body {
		p, ok := props[k].(map[string]any)
		if !ok {
			t.Fatalf("%s: %s is no property of %v", at, k, slices.Sorted(maps.Keys(props)))
		}
		if nested, ok := v.(map[string]any); ok {
			fitsSchema(t, at+"."+k, p, nested)
		}
	}
	req, _ := schema["required"].([]string)
	for _, k := range req {
		if _, ok := body[k]; !ok {
			t.Fatalf("%s: the required %s is not in %v", at, k, body)
		}
	}
}

// A body made from each Dispatch operation's schema, its required
// properties and the revision it names, is taken by its route.
func TestABodyFromEachDispatchSchemaIsTaken(t *testing.T) {
	e := dispatchEnv(t)
	e.newDispatchProject(t, "SCH", nil)
	key := e.newDispatchTicket(t, "SCH", "Schemas", nil)["key"].(string)
	ops := map[string]Operation{}
	for _, op := range Operations() {
		ops[op.Name] = op
	}
	rev := func() any {
		return entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")["rev"]
	}
	for _, c := range []struct {
		name   string
		body   func() map[string]any
		status int
	}{
		{"ticket_create", func() map[string]any {
			return map[string]any{"opId": opID(), "project": "SCH", "title": "From the schema"}
		}, http.StatusCreated},
		{"ticket_edit", func() map[string]any {
			return map[string]any{"opId": opID(), "expectedRev": rev(), "set": map[string]any{"title": "Edited"}, "add": map[string]any{"assignees": []string{"person:Ada"}}}
		}, http.StatusOK},
		{"ticket_comment_admin", func() map[string]any {
			return map[string]any{"opId": opID(), "expectedRev": rev(), "body": "From the schema"}
		}, http.StatusCreated},
		{"ticket_link", func() map[string]any {
			return map[string]any{"opId": opID(), "expectedRev": rev(), "kind": "external", "url": "https://tracker.example.com/SCH-1"}
		}, http.StatusCreated},
	} {
		op := ops[c.name]
		if op.Revision != "ticket" && c.name != "ticket_create" {
			t.Fatalf("%s: revision %q", c.name, op.Revision)
		}
		body := c.body()
		fitsSchema(t, c.name, op.Input, body)
		e.expect(t, op.Method, strings.Replace(op.Path, "{key}", key, 1), body, c.status, "")
	}
}

// crew_create's input is a crew as GET /api/crews/{id} reads it without
// the fields the server sets (id, createdAt, updatedAt, manifestRevision):
// a crew read back, with a project manifest or with a legacy setup (both
// carry manifestRevision), cut to the schema's properties fits it and is
// saved by its route.
func TestCrewCreatesSchemaIsACrewReadBackWithoutTheServersFields(t *testing.T) {
	var op Operation
	for _, o := range Operations() {
		if o.Name == "crew_create" {
			op = o
		}
	}
	props, _ := op.Input["properties"].(map[string]any)
	// origin is the server's too: only a Brakeman draft's make sets it.
	server := []string{"id", "createdAt", "updatedAt", "manifestRevision", "origin"}
	var want []string
	for k := range fullOf(t, reflect.TypeFor[crew.Crew]()) {
		if !slices.Contains(server, k) {
			want = append(want, k)
		}
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(props)); !slices.Equal(got, want) {
		t.Fatalf("crew_create's properties %v, a crew's without the server's %v", got, want)
	}
	e := newTestEnv(t, nil)
	legacy := e.manifestCrewBody("Legacy", "cat", nil)
	delete(legacy, "manifest")
	legacy["setup"] = []any{map[string]any{"id": "deps", "argv": []any{"npm", "ci"}}}
	manifested := e.manifestCrewBody("Manifested", "cat", map[string]any{"setup": []any{map[string]any{"id": "deps", "argv": []any{"npm", "ci"}}}})
	for i, body := range []map[string]any{manifested, legacy} {
		read := e.crew(e.sendCrew("POST", "/api/crews", body, http.StatusCreated)["id"].(string))
		if read["manifestRevision"] == nil {
			t.Fatalf("%s read back without its manifestRevision: %v", body["name"], read)
		}
		for k := range read {
			if _, ok := props[k]; !ok {
				delete(read, k)
			}
		}
		read["name"] = fmt.Sprintf("Copy %d", i)
		fitsSchema(t, "crew_create", op.Input, read)
		e.sendCrew(op.Method, op.Path, read, http.StatusCreated)
	}
}

// task_retry's summary names the task states a retry takes, as the
// engine enforces them (crew.RetryableStates), and no other.
func TestTaskRetrySaysTheStatesARetryTakes(t *testing.T) {
	var summary string
	for _, o := range Operations() {
		if o.Name == "task_retry" {
			summary = o.Summary
		}
	}
	for _, st := range []string{ledger.TaskBlocked, ledger.TaskReady, ledger.TaskRunning, ledger.TaskVerifying, ledger.TaskSucceeded, ledger.TaskFailed, ledger.TaskCancelled} {
		if strings.Contains(summary, st) != slices.Contains(crew.RetryableStates(), st) {
			t.Errorf("task_retry's summary %q on a %s task; a retry takes %v", summary, st, crew.RetryableStates())
		}
	}
}

// Each operation's advertised body bound is its route's: a body one byte
// past it is refused as too large, one at it is read whole (and refused
// only for the field it names, so it changes nothing). Every operation
// with a body bound is tried, by a caller its guide lists it for: a plain
// session, a member of a run that is not managed, an attempt of a managed
// run linked to a ticket, the admin.
func TestEachBodyBoundIsItsRoutes(t *testing.T) {
	plain := newTestEnv(t, nil)
	plain.srv.limiter = newRateLimiter(1e6, 1e6)
	psid := plain.createSession("cat")
	_, msid, mtok := plain.memberSession(t, "Bounded")

	e := readyEnv(t, func(c *config.Config) { c.DataDir = filepath.Join(t.TempDir(), "data") })
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	asid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	e.newDispatchProject(t, "BND", nil)
	key := e.newDispatchTicket(t, "BND", "Bounded", nil)["key"].(string)
	e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run.ID}, http.StatusCreated, "")

	// A Brakeman turn's session, whose guide lists its brief, and, granted
	// every operation this build serves, the act routes' own bodies.
	bk := brakemanEnv(t, nil)
	bk.enable(t, "bk", func(c map[string]any) {
		c["permissions"].(map[string]any)["act"] = append(slices.Clone(everyGrant), brakeman.ActRouteDecision, brakeman.ActRequestRepair)
	})
	bsid, btok, _ := bk.turnSession(t, bk.briefing(t))

	tried := map[string]bool{}
	for _, c := range []struct {
		e          *testEnv
		token, sid string
	}{{plain, plain.agentToken(psid), psid}, {plain, mtok, msid}, {e, e.agentToken(asid), asid}, {e, adminToken, ""}, {bk.testEnv, btok, bsid}} {
		guide := "/api/guide?section=operations"
		id := "x"
		if c.sid != "" {
			guide, id = "/api/sessions/"+c.sid+"/guide?section=operations", c.sid
		}
		status, raw, _ := guideOf(t, c.e, c.token, guide)
		var g struct {
			Operations []Operation `json:"operations"`
		}
		if err := json.Unmarshal(raw, &g); status != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %v", guide, status, err)
		}
		paths := strings.NewReplacer("{id}", id, "{run}", run.ID, "{task}", "build", "{key}", key, "{did}", "dr-1")
		for _, op := range g.Operations {
			n := op.Bounds["body"]
			if op.Method != http.MethodPost || n == 0 || (c.sid == "" && op.Scope != "admin") {
				continue
			}
			tried[op.Name] = true
			path := paths.Replace(op.Path)
			probe := `{"boundProbe":1`
			over := probe + strings.Repeat(" ", n+1-len(probe)-1) + "}"
			at := probe + strings.Repeat(" ", n-len(probe)-1) + "}"
			if len(over) != n+1 || len(at) != n {
				t.Fatalf("%s: bodies of %d and %d bytes", op.Name, len(over), len(at))
			}
			call := c.e.callAs
			if op.Scope == "act" {
				// A granted operation is the turn's on its own route as the
				// principal brakeman: its session named.
				call = func(t *testing.T, token, method, path string, body any) (int, []byte, map[string]any) {
					return c.e.callAsTurn(t, token, c.sid, method, path, body.([]byte))
				}
			}
			status, data, out := call(t, c.token, op.Method, path, []byte(over))
			if status != http.StatusRequestEntityTooLarge && !strings.Contains(apiMessage(out), errBodyTooLarge.Error()) {
				t.Errorf("%s: %d bytes, one past its bound, answers %d %.200s", op.Name, len(over), status, data)
			}
			// Read whole, it is refused for its member: in the decoder's
			// words, or in the tracker routes' own sentence (they never pass
			// a decoder's words on).
			status, data, out = call(t, c.token, op.Method, path, []byte(at))
			if status != http.StatusBadRequest || !strings.Contains(apiMessage(out), `unknown field "boundProbe"`) && apiMessage(out) != trackerBodyWords {
				t.Errorf("%s: %d bytes, at its bound, answers %d %.200s", op.Name, len(at), status, data)
			}
		}
	}
	for _, op := range Operations() {
		if op.Method == http.MethodPost && op.Bounds["body"] > 0 && !tried[op.Name] {
			t.Errorf("%s: no caller's guide lists it, so its body bound is untried", op.Name)
		}
	}
}

// jsonOf is v as encoding/json reads it back: maps, lists and numbers,
// so two values built differently compare by what they say.
func jsonOf(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The briefs section is internal/brakeman's kind table, the same for the
// admin and for a Brakeman turn: its words are brakeman.Words, its JSON
// Schema brief_submit's input's brief (every item kind, a draft crew's
// crew the body crew_create takes), its refs the ref forms. The
// operations section points to it for the brief rather than carry the
// schema twice; tools/list carries it whole. The live check's agent had
// {"type":"object"} and read Conductor's source to learn a brief.
func TestTheBriefsSectionIsTheKindTable(t *testing.T) {
	b := brakemanEnv(t, nil)
	b.enable(t, "bk", nil)
	id := b.briefing(t)
	sid, tok, _ := b.turnSession(t, id)
	ops := Operations()
	submit := ops[slices.IndexFunc(ops, func(o Operation) bool { return o.Name == "brief_submit" })]
	crewCreate := ops[slices.IndexFunc(ops, func(o Operation) bool { return o.Name == "crew_create" })]
	schema := submit.Input["properties"].(map[string]any)["brief"]
	if !reflect.DeepEqual(jsonOf(t, schema), jsonOf(t, brakeman.BriefJSONSchema(bodySchema(crewInput{})))) {
		t.Fatal("brief_submit's brief is not the kind table's schema")
	}
	draftCrew := jsonOf(t, schema).(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)[8].(map[string]any)
	crewBody := draftCrew["properties"].(map[string]any)["crew"].(map[string]any)
	delete(crewBody, "description")
	if draftCrew["properties"].(map[string]any)["kind"].(map[string]any)["const"] != brakeman.ItemDraftCrew || !reflect.DeepEqual(crewBody, jsonOf(t, crewCreate.Input)) {
		t.Fatalf("a draft crew's crew is not crew_create's input: %v", crewBody)
	}
	// A draft plan's crew is a saved crew's id, as DraftPlan decodes it:
	// never the body crew_create takes.
	for _, b := range jsonOf(t, schema).(map[string]any)["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any) {
		props := b.(map[string]any)["properties"].(map[string]any)
		if props["kind"].(map[string]any)["const"] != brakeman.ItemDraftPlan {
			continue
		}
		if c := props["crew"].(map[string]any); c["type"] != "string" || c["properties"] != nil {
			t.Fatalf("a draft plan's crew in brief_submit's schema: %v", c)
		}
	}
	for _, c := range []struct{ tok, path string }{{adminToken, "/api/guide"}, {tok, "/api/sessions/" + sid + "/guide"}} {
		status, _, out := guideOf(t, b.testEnv, c.tok, c.path+"?section=briefs")
		if status != http.StatusOK {
			t.Fatalf("%s: %d %v", c.path, status, out)
		}
		sec := out["briefs"].(map[string]any)
		if sec["schema"] != brakeman.BriefSchema || !reflect.DeepEqual(sec["words"], jsonOf(t, brakeman.Words())) ||
			!reflect.DeepEqual(sec["refs"], jsonOf(t, brakeman.RefForms())) || !reflect.DeepEqual(sec["jsonSchema"], jsonOf(t, schema)) {
			t.Errorf("%s: the briefs section is not the table's: %v", c.path, sec)
		}
		_, _, out = guideOf(t, b.testEnv, c.tok, c.path+"?section=operations")
		for _, op := range out["operations"].([]any) {
			op := op.(map[string]any)
			if op["name"] != "brief_submit" {
				continue
			}
			brief := op["input"].(map[string]any)["properties"].(map[string]any)["brief"].(map[string]any)
			if !strings.Contains(brief["description"].(string), "the guide's briefs section") || brief["properties"] != nil {
				t.Errorf("%s: the operations section's brief_submit: %v", c.path, brief)
			}
		}
	}
}

// missingRequired names each field schema requires that v leaves out, and
// each it does not take, through objects' properties and lists' items.
func missingRequired(schema map[string]any, v any, path string) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		req, _ := schema["required"].([]any)
		for _, name := range req {
			if _, ok := v[name.(string)]; !ok {
				out = append(out, path+"."+name.(string)+" left out")
			}
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			sub, ok := props[k].(map[string]any)
			if !ok {
				if schema["additionalProperties"] == false {
					out = append(out, path+"."+k+" not taken")
				}
				continue
			}
			out = append(out, missingRequired(sub, v[k], path+"."+k)...)
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range v {
				out = append(out, missingRequired(items, e, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return out
}

// The draft crew's example, as brief_submit's schema and the guide's
// briefs section give it, is a crew POST /api/crews saves: every field
// crew_create's schema requires, the crew validator's checks (a brief
// holding it is taken with the server's own facts), and the route's 201.
// The example once left out where, isolation, openAfterLaunch and a
// member's start, and an agent that copied it was refused.
func TestTheDraftCrewExampleIsACrewThatSaves(t *testing.T) {
	t.Parallel()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{{ID: "claude", Name: "Claude Code", Command: []string{"/bin/cat"}, Adapter: "claude"}})
	k := brakeman.ItemKinds()[slices.IndexFunc(brakeman.ItemKinds(), func(k brakeman.KindSpec) bool { return k.Kind == brakeman.ItemDraftCrew })]
	var ex struct {
		Crew json.RawMessage `json:"crew"`
	}
	if err := json.Unmarshal(k.Example, &ex); err != nil {
		t.Fatal(err)
	}
	var body any
	if err := json.Unmarshal(ex.Crew, &body); err != nil {
		t.Fatal(err)
	}
	if bad := missingRequired(jsonOf(t, bodySchema(crewInput{})).(map[string]any), body, "crew"); len(bad) > 0 {
		t.Errorf("the example against crew_create's schema: %v", bad)
	}
	if err := (brakemanFacts{s: e.srv}).CheckCrew(ex.Crew); err != nil {
		t.Errorf("the crew validator: %v", err)
	}
	brief := `{"schema":"` + brakeman.BriefSchema + `","title":"t","summary":"s","items":[` + string(brakeman.ItemKinds()[0].Example) + `,` + string(k.Example) + `]}`
	if _, err := brakeman.DecodeBrief([]byte(brief), brakeman.Permissions{Observe: true, Draft: true, Propose: true}, brakemanFacts{s: e.srv}); err != nil {
		t.Errorf("a brief of the example: %v", err)
	}
	e.expect(t, "POST", "/api/crews", body, http.StatusCreated, "")
}

// The rule a crew with tasks is refused by (a member started otherwise
// than by its task: start.when must be "task") is in the draft crew's
// guidance, where an agent writing one reads it: a real model's first
// draft crew was refused by it, a rule the guidance did not state.
func TestTheDraftCrewGuidanceStatesTheStartRule(t *testing.T) {
	t.Parallel()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{{ID: "claude", Name: "Claude Code", Command: []string{"/bin/cat"}, Adapter: "claude"}})
	crewBody := json.RawMessage(`{"name":"Locale fix","cwd":"` + e.root + `","where":"server","isolation":"worktree","members":[{"name":"writer","agentId":"claude","prompt":"Fix it.","start":{"when":"immediately"}}],"tasks":[{"id":"fix","member":"writer","kind":"work","instructions":"Fix it."}]}`)
	err := (brakemanFacts{s: e.srv}).CheckCrew(crewBody)
	if err == nil || !strings.Contains(err.Error(), `start.when must be "task"`) {
		t.Fatalf("a crew with tasks whose member starts at once: %v", err)
	}
	line := ""
	for _, w := range guideBriefsSection().Words {
		if strings.HasPrefix(w, brakeman.ItemDraftCrew+":") {
			line = w
		}
	}
	if !strings.Contains(line, `In a crew with tasks, every member's start.when is "task"`) {
		t.Fatalf("the draft crew's guidance: %q", line)
	}
}

// callAsTurn is callAs as a Brakeman turn: its session's token, the
// session named in X-Conductor-Session.
func (e *testEnv) callAsTurn(t *testing.T, token, sid, method, path string, body []byte) (int, []byte, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(brakemanSessionHeader, sid)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, data, out
}

// A Brakeman turn's guide lists the act tool and the operations its
// grant holds, the snapshot its turn began under and the configuration
// now together, and its request says the same: a turn granted none lists
// neither, a grant taken away mid-turn goes from both at once, and one
// given mid-turn waits for the next turn. The admin's guide lists every
// operation a grant may name, with its route, its body and its receipt.
func TestATurnsGuideListsItsGrantedOperations(t *testing.T) {
	bk := brakemanEnv(t, nil)
	bk.enable(t, "bk", func(c map[string]any) {
		c["permissions"].(map[string]any)["act"] = []string{brakeman.ActLaunchApprovedPlan}
	})
	req := bk.briefing(t)
	sid, tok, _ := bk.turnSession(t, req)
	look := func(t *testing.T, wantActs []string) {
		t.Helper()
		status, _, out := guideOf(t, bk.testEnv, tok, "/api/sessions/"+sid+"/guide?section=operations")
		var acts []string
		for _, n := range opNames(t, out) {
			if n == "act" || slices.Contains(brakeman.Grantable, n) {
				acts = append(acts, n)
			}
		}
		want := []string(nil)
		if len(wantActs) > 0 {
			want = append([]string{"act"}, wantActs...)
		}
		if status != http.StatusOK || !slices.Equal(acts, want) {
			t.Fatalf("the turn's act operations %d %v, want %v", status, acts, want)
		}
		_, _, out = guideOf(t, bk.testEnv, tok, "/api/sessions/"+sid+"/guide")
		scopes := fmt.Sprint(out["overview"].(map[string]any)["caller"].(map[string]any)["scopes"])
		if strings.Contains(scopes, "act") != (len(wantActs) > 0) {
			t.Fatalf("the caller's scopes %s with acts %v", scopes, wantActs)
		}
		status, rr := bk.readRequest(t, sid, tok)
		perm := rr["request"].(map[string]any)["permissions"].(map[string]any)
		got, _ := perm["act"].([]any)
		if status != http.StatusOK || fmt.Sprint(got) != fmt.Sprint(wantActs) && !(len(got) == 0 && len(wantActs) == 0) {
			t.Fatalf("the request's permissions %d %v, want act %v", status, perm, wantActs)
		}
	}
	look(t, []string{brakeman.ActLaunchApprovedPlan})
	// Granted the follow-up too: the next turn's, not this one's.
	bk.configure(t, func(c map[string]any) { c["permissions"].(map[string]any)["act"] = everyGrant })
	look(t, []string{brakeman.ActLaunchApprovedPlan})
	// Every grant taken away: at once.
	bk.configure(t, func(c map[string]any) { c["permissions"].(map[string]any)["act"] = []string{} })
	look(t, nil)

	status, _, out := guideOf(t, bk.testEnv, adminToken, "/api/guide?section=operations")
	ops := map[string]map[string]any{}
	for _, x := range out["operations"].([]any) {
		op := x.(map[string]any)
		ops[op["name"].(string)] = op
	}
	for name, path := range map[string]string{"act": "/api/dispatch/tickets/{key}/launch", brakeman.ActLaunchApprovedPlan: "/api/dispatch/tickets/{key}/launch", brakeman.ActPrepareFollowup: "/api/dispatch/tickets/{key}/plans",
		brakeman.ActRequestRepair: "/api/dispatch/tickets/{key}/reviews"} {
		op := ops[name]
		if status != http.StatusOK || op == nil || op["scope"] != "act" || op["path"] != path || op["receipt"] != "dispatch" {
			t.Errorf("the admin's %s: %v", name, op)
		}
	}
	// route_decision is DR1's routing route, with the ledger's receipt and
	// the decision's rev.
	if op := ops[brakeman.ActRouteDecision]; op == nil || op["scope"] != "act" || op["path"] != "/api/decisions/{did}/route" || op["receipt"] != "ledger" || op["revision"] != "decision" {
		t.Errorf("the admin's %s: %v", brakeman.ActRouteDecision, op)
	}
}

// The overview's may list and the operations section are what the
// routes admit, for this caller, now: one check (admits) makes the list
// and refuses the call. Over the callers (the admin, a session, Brakeman
// turns with a grant and without, with terminals on and off, with
// Dispatch hidden), each operation listed is one its route takes past its
// gates, and each left out that the caller's scopes reach is one its
// route refuses: the admin an agent's own (403 agent_only), a turn a read
// of a class it may not see (403 not_visible). A turn granted only
// request_repair, which needs both Dispatch and the runs, is listed it and
// the act tool with both visible, neither with either hidden, and its route
// refuses it not_visible then. A run's member is listed the ticket's two
// exactly when its ticket's routes answer it a ticket: a run launched
// from a ticket, a run linked to one, neither for a run with none or one
// whose launching ticket is deleted (a comparison's lane is
// TestALaneIsListedTicketReadForItsLaunchAlone's). The routes are called,
// not the list compared with itself.
func TestTheOverviewListsWhatTheRoutesAdmit(t *testing.T) {
	fill := strings.NewReplacer("{run}", "run-none", "{task}", "t", "{n}", "1", "{id}", "1", "{k}", "1", "{command}", "c", "{key}", "CND-1")
	mayOf := func(t *testing.T, e *testEnv, token, path string) []string {
		t.Helper()
		status, _, out := guideOf(t, e, token, path)
		if status != http.StatusOK {
			t.Fatalf("overview %d", status)
		}
		var may []string
		for _, x := range out["overview"].(map[string]any)["may"].([]any) {
			may = append(may, x.(string))
		}
		_, _, sec := guideOf(t, e, token, path+"?section=operations")
		if !slices.Equal(may, opNames(t, sec)) {
			t.Fatalf("may %v, the section %v", may, opNames(t, sec))
		}
		return may
	}
	gated := func(status int, out map[string]any, code string) bool {
		e, _ := out["error"].(map[string]any)
		return status == http.StatusForbidden && e["code"] == code
	}

	t.Run("the admin", func(t *testing.T) {
		e := newTestEnv(t, nil)
		sid := e.createSession("cat")
		may := mayOf(t, e, adminToken, "/api/guide")
		for _, op := range Operations() {
			if !op.agentOnly {
				continue
			}
			if slices.Contains(may, op.Name) {
				t.Errorf("the admin is listed %s, an agent's own", op.Name)
			}
			status, _, out := e.callAs(t, adminToken, op.Method, strings.ReplaceAll(op.Path, "{id}", sid), []byte(`{}`))
			if !gated(status, out, "agent_only") {
				t.Errorf("%s with the admin's token: %d %v", op.Name, status, out)
			}
		}
		for _, name := range []string{"run_stop", "task_context", "brakeman_request", "observe_screen", "act"} {
			if !slices.Contains(may, name) {
				t.Errorf("the admin is not listed %s", name)
			}
		}
	})

	t.Run("a session", func(t *testing.T) {
		e := newTestEnv(t, nil)
		sid := e.createSession("cat")
		may := mayOf(t, e, e.agentToken(sid), "/api/sessions/"+sid+"/guide")
		if slices.ContainsFunc(may, func(n string) bool {
			return strings.HasPrefix(n, "observe") || n == "act" || n == "brief_submit" || n == "run_stop"
		}) {
			t.Fatalf("a session's may: %v", may)
		}
	})

	t.Run("a run's member's ticket", func(t *testing.T) {
		l := newLaunchTest(t, nil, nil)
		e := l.testEnv
		launched := entityOf(t, l.launch(t, e, nil, http.StatusCreated, ""), "launch")["runId"].(string)
		lsid := runningAttempt(t, e, launched, "build", 1)["sessionId"].(string)
		e.newDispatchTicket(t, "CND", "Linked", nil)
		linked, ksid := legacyRun(t, e, l.repo)
		linkRun(t, e, "CND-2", linked, http.StatusCreated, "")
		_, nsid, _ := e.memberSession(t, "Unticketed")
		// key is the ticket the routes answer, "" for 404 no_ticket.
		check := func(t *testing.T, what, sid, key string) {
			t.Helper()
			tok := e.agentToken(sid)
			may := mayOf(t, e, tok, "/api/sessions/"+sid+"/guide")
			_, _, out := guideOf(t, e, tok, "/api/sessions/"+sid+"/guide")
			kinds := fmt.Sprint(out["overview"].(map[string]any)["caller"].(map[string]any)["kinds"])
			status, raw, read := e.callAs(t, tok, http.MethodGet, "/api/sessions/"+sid+"/ticket", nil)
			body, _ := json.Marshal(map[string]any{"opId": opID(), "body": "Seen."})
			cstatus, craw, cout := e.callAs(t, tok, http.MethodPost, "/api/sessions/"+sid+"/ticket/comments", body)
			listed := key != ""
			t.Logf("%s: ticket_read listed %v, ticket_comment listed %v, kinds %s; its read %d, its comment %d",
				what, slices.Contains(may, "ticket_read"), slices.Contains(may, "ticket_comment"), kinds, status, cstatus)
			if slices.Contains(may, "ticket_read") != listed || slices.Contains(may, "ticket_comment") != listed || strings.Contains(kinds, "ticket_member") != listed {
				t.Errorf("%s: listed %v, want %v: may %v, kinds %s", what, !listed, listed, may, kinds)
			}
			switch {
			case listed && (status != http.StatusOK || entityOf(t, read, "ticket")["key"] != key):
				t.Errorf("%s's read: %d %s, want %s", what, status, raw, key)
			case listed && cstatus != http.StatusCreated:
				t.Errorf("%s's comment: %d %s", what, cstatus, craw)
			case !listed && (status != http.StatusNotFound || errCodeOf(read) != "no_ticket" || cstatus != http.StatusNotFound || errCodeOf(cout) != "no_ticket"):
				t.Errorf("%s's routes: %d %s, %d %s, want 404 no_ticket", what, status, raw, cstatus, craw)
			}
		}
		check(t, "a run launched from a ticket", lsid, "CND-1")
		check(t, "a run linked to a ticket", ksid, "CND-2")
		check(t, "a run with neither", nsid, "")
		l.expect(t, "POST", "/api/dispatch/tickets/CND-1/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
		check(t, "a run whose launching ticket is deleted", lsid, "")
	})

	for _, c := range []struct {
		name                         string
		granted, terminals, dispatch bool
		// repair grants request_repair alone in launch_approved_plan's
		// place; runs is the runs' visibility.
		repair, runs bool
	}{
		{"a turn granted, terminals on", true, true, true, false, true},
		{"a turn granted nothing", false, true, true, false, true},
		{"a turn granted, terminals off", true, false, true, false, true},
		{"a turn granted, Dispatch hidden", true, false, false, false, true},
		{"a turn granted request_repair, both visible", true, true, true, true, true},
		{"a turn granted request_repair, the runs hidden", true, true, true, true, false},
		{"a turn granted request_repair, Dispatch hidden", true, true, false, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			bk := brakemanEnv(t, nil)
			bk.enable(t, "bk", func(cfg map[string]any) {
				act := []any{}
				switch {
				case c.granted && c.repair:
					act = []any{brakeman.ActRequestRepair}
				case c.granted:
					act = []any{brakeman.ActLaunchApprovedPlan}
				}
				cfg["permissions"].(map[string]any)["act"] = act
				v := cfg["visibility"].(map[string]any)
				v["terminals"], v["dispatch"], v[brakeman.ClassRuns] = c.terminals, c.dispatch, c.runs
			})
			req := bk.briefing(t)
			sid, tok, _ := bk.turnSession(t, req)
			awaitRunMember(t, bk, sid)
			may := mayOf(t, bk.testEnv, tok, "/api/sessions/"+sid+"/guide")
			granted := brakeman.ActLaunchApprovedPlan
			if c.repair {
				granted = brakeman.ActRequestRepair
			}
			want := c.granted && c.dispatch && (c.runs || !c.repair)
			if slices.Contains(may, "act") != want || slices.Contains(may, granted) != want {
				t.Errorf("act and %s listed %v %v, want %v: %v", granted, slices.Contains(may, "act"), slices.Contains(may, granted), want, may)
			}
			if c.repair {
				// The route: refused not_visible when not listed, past
				// that gate when listed (the run is none: refused later).
				body, _ := json.Marshal(map[string]any{"opId": "g-1", "candidate": candName("demo-0123abcd", "build", 1, strings.Repeat("a", 40)),
					"verdict": dispatch.VerdictRequestChanges, "summary": "Handle the error.", "findings": []any{changeOf("Handle the error.")}})
				status, _, out := bk.callAsTurn(t, tok, sid, http.MethodPost, "/api/dispatch/tickets/CND-1/reviews", body)
				if refused := gated(status, out, "not_visible"); want == refused {
					t.Errorf("request_repair listed %v, its route: %d %v", want, status, out)
				}
			}
			if !slices.Contains(may, "brakeman_request") || !slices.Contains(may, "brief_submit") {
				t.Errorf("the turn's own: %v", may)
			}
			// Every read: listed, its route takes it past its gate; not,
			// its route refuses it not_visible.
			for _, op := range Operations() {
				if op.Scope != "observe" || op.Method != http.MethodGet || !strings.HasPrefix(op.Name, "observe_") {
					continue
				}
				status, _, out := bk.callAsTurn(t, tok, sid, op.Method, fill.Replace(op.Path), nil)
				listed := slices.Contains(may, op.Name)
				if refused := gated(status, out, "not_visible"); listed == refused {
					t.Errorf("%s listed %v, its route: %d %v", op.Name, listed, status, out)
				}
			}
			if slices.Contains(may, "observe_screen") != c.terminals {
				t.Errorf("observe_screen listed with terminals %v: %v", c.terminals, may)
			}
			if slices.Contains(may, "observe_tickets") != c.dispatch {
				t.Errorf("observe_tickets listed with Dispatch %v: %v", c.dispatch, may)
			}
		})
	}
}
