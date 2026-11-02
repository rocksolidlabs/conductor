package api

import (
	"encoding/json"
	"fmt"
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

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
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
	if c := ov["caller"].(map[string]any); c["kind"] != "admin" || fmt.Sprint(c["scopes"]) != "[admin session task ticket observe]" {
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
		if op.Scope != "admin" && !registered[key] {
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
	callers := []guideCaller{adminCaller(), {Kind: "session", Session: long, Kinds: []string{"run_member", "attempt", "ticket_member"}, Run: long, Member: strings.Repeat("m", 40), Scopes: []string{"session", "task", "ticket"}, local: true}}
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
		content["host"] = h
		replies, err := encodeGuide(c, content)
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
// managed run the task tools too, and no add_member; a member of a run a
// ticket holds the ticket's two. The admin reads a session's guide as the
// session's.
func TestASessionsGuideListsItsScopesOnly(t *testing.T) {
	// A session in no run may form a crew; one in a run may add to it and
	// read it, and forms none (409 in_a_run); one in a managed run only
	// reads it, the run making its members itself (409 managed_run).
	plainOps := []string{"report", "set_state", "ask", "form_crew", "link", "guide"}
	memberOps := []string{"report", "set_state", "ask", "add_member", "run_status", "link"}
	attemptOps := []string{"report", "set_state", "ask", "run_status", "link"}
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
		check(t, e, sid, e.agentToken(sid), []string{"run_member", "attempt"}, slices.Concat(attemptOps, taskOps, []string{"guide"}))
		// What the guide leaves out, the route refuses.
		status, data, out := e.callAs(t, e.agentToken(sid), http.MethodPost, "/api/sessions/"+sid+"/run/members", []byte(`{"name":"extra","agentId":"ready-cat","prompt":"","start":{"when":"immediately"}}`))
		if status != http.StatusConflict || errCodeOf(out) != "managed_run" {
			t.Fatalf("add_member in a managed run: %d %s", status, data)
		}
	})
	t.Run("ticket", func(t *testing.T) {
		e := dispatchEnv(t)
		run, sid, tok := e.memberSession(t, "Ticketed")
		check(t, e, sid, tok, []string{"run_member"}, slices.Concat(memberOps, []string{"guide"}))
		e.newDispatchProject(t, "GDE", nil)
		key := e.newDispatchTicket(t, "GDE", "Guided", nil)["key"].(string)
		e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
		check(t, e, sid, tok, []string{"run_member", "ticket_member"}, slices.Concat(memberOps, ticketOps, []string{"guide"}))
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
			if v.Field(i).CanSet() {
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
		"run_pause": crew.Command{}, "run_unpause": crew.Command{}, "task_cancel": crew.Command{}, "task_retry": crew.Command{}, "task_accept": crew.Command{},
		"ticket_create": ticketCreateBody{}, "ticket_edit": ticketEditBody{}, "ticket_comment_admin": commentBody{}, "ticket_link": linkBody{},
		"crew_create": crewInput{}, "crew_launch": launchBody{},
		"brakeman_config": brakemanConfigBody{}, "brakeman_ask": brakemanAskBody{}, "brakeman_cancel": brakemanCancelBody{},
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
	server := []string{"id", "createdAt", "updatedAt", "manifestRevision"}
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

	tried := map[string]bool{}
	for _, c := range []struct {
		e          *testEnv
		token, sid string
	}{{plain, plain.agentToken(psid), psid}, {plain, mtok, msid}, {e, e.agentToken(asid), asid}, {e, adminToken, ""}} {
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
		paths := strings.NewReplacer("{id}", id, "{run}", run.ID, "{task}", "build", "{key}", key)
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
			status, data, out := c.e.callAs(t, c.token, op.Method, path, []byte(over))
			if status != http.StatusRequestEntityTooLarge && !strings.Contains(apiMessage(out), errBodyTooLarge.Error()) {
				t.Errorf("%s: %d bytes, one past its bound, answers %d %.200s", op.Name, len(over), status, data)
			}
			status, data, out = c.e.callAs(t, c.token, op.Method, path, []byte(at))
			if status != http.StatusBadRequest || !strings.Contains(apiMessage(out), `unknown field "boundProbe"`) {
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
