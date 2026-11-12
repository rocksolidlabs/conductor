package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The decide grant's routes (docs/plans/decide-grant.md, PR-1): a
// principal exttest's FakeIdentity gives the decide grant, on a server
// with that provider registered. Every test waits on the ledger's record
// or holds a step at a seam of the server's (decideSeam, actSeam) or of
// the provider's (gatedIdentity), never on a clock.

// The passes the fixture's provider admits: invented strings.
const (
	passAna         = "pass.decide.ana-7f3c"
	passBob         = "pass.decide.bob-91d0"
	passHostAna     = "pass.host-decide.ana-2b6e"
	passHostOnly    = "pass.host.ana-c4a8"
	passAdmin       = "pass.admin.nate-5e1f"
	passAdminDecide = "pass.admin-decide.nate-0d97"
	passUnknown     = "pass.unknown-88ab"
)

// gatedIdentity is a FakeIdentity whose recheck of a token, the second ask
// about it since recheckAs (a response's authentication being the first),
// can answer another principal (then) and wait at a gate (hold) first,
// telling reached as it starts to wait.
type gatedIdentity struct {
	*exttest.FakeIdentity

	mu      sync.Mutex
	asked   map[string]int
	then    map[string]*ext.Principal
	hold    map[string]chan struct{}
	reached chan string
}

func (g *gatedIdentity) Authenticate(ctx context.Context, tok string) (ext.Principal, bool, error) {
	g.mu.Lock()
	g.asked[tok]++
	n, then, hold := g.asked[tok], g.then[tok], g.hold[tok]
	g.mu.Unlock()
	if n != 2 {
		return g.FakeIdentity.Authenticate(ctx, tok)
	}
	if hold != nil {
		g.reached <- tok
		select {
		case <-hold:
		case <-ctx.Done():
			return ext.Principal{}, false, ctx.Err()
		}
	}
	if then != nil {
		return *then, then.Subject != "", nil
	}
	return g.FakeIdentity.Authenticate(ctx, tok)
}

// recheckAs has the provider answer tok's recheck with p (a zero p: not
// admitted), after waiting at a gate when held: the gate is returned.
func (g *gatedIdentity) recheckAs(tok string, p ext.Principal, held bool) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked[tok], g.then[tok] = 0, &p
	if !held {
		return nil
	}
	gate := make(chan struct{})
	g.hold[tok] = gate
	return gate
}

// decideFixture is a decisionFixture whose crew has a person (zelda, the
// architecture owner) and whose identity provider admits the passes.
type decideFixture struct {
	*decisionFixture
	id     *gatedIdentity
	seeded int // the runs seed made
}

func newDecideFixture(t *testing.T, mutate func(*config.Config)) *decideFixture {
	t.Helper()
	fake := exttest.NewFakeIdentity("cloud")
	principal := func(subject string, gs ...ext.Grant) ext.Principal {
		return ext.Principal{Subject: subject, Name: strings.ToUpper(subject[:1]) + subject[1:], Grants: gs}
	}
	fake.Add(passAna, principal("ana", ext.GrantDecide))
	fake.Add(passBob, principal("bob", ext.GrantDecide))
	fake.Add(passHostAna, principal("ana", ext.GrantHost, ext.GrantDecide))
	fake.Add(passHostOnly, principal("ana", ext.GrantHost))
	fake.Add(passAdmin, principal("nate", ext.GrantAdmin))
	fake.Add(passAdminDecide, principal("nate", ext.GrantAdmin, ext.GrantDecide))
	id := &gatedIdentity{FakeIdentity: fake, asked: map[string]int{}, then: map[string]*ext.Principal{}, hold: map[string]chan struct{}{}, reached: make(chan string, 8)}
	e := newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{readyCat}, withIdentity(id))
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	if e.srv.cfg.Switchyard.Enabled {
		return &decideFixture{decisionFixture: &decisionFixture{testEnv: e}, id: id}
	}
	e.gitRepo(t)
	c := managedCrew(e.root)
	c.People = []crew.Person{{Name: "zelda", Responsibilities: []string{crew.RespArchitecture}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	srv := e.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	sid := runningAttempt(t, e, run.ID, "build", 1)["sessionId"].(string)
	return &decideFixture{decisionFixture: &decisionFixture{testEnv: e, run: run.ID, sid: sid, tok: e.agentToken(sid)}, id: id}
}

// open asks an approval of the attempt and answers its id.
func (f *decideFixture) open(t *testing.T, requestID string) string {
	t.Helper()
	return entityOf(t, f.ask(t, approvalBody(requestID), http.StatusCreated, ""), "decision")["id"].(string)
}

// rev is decision did's rev in the ledger.
func (f *decideFixture) rev(t *testing.T, did string) int {
	t.Helper()
	return f.record(t, did).Rev
}

// record is decision did's record in the ledger.
func (f *decideFixture) record(t *testing.T, did string) ledger.DecisionRecord {
	t.Helper()
	n, _ := ledger.ParseDecisionID(did)
	rec, ok, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil || !ok {
		t.Fatalf("%s: %v %v", did, ok, err)
	}
	return rec
}

// routeTo routes did as the admin to `to` at its rev now, expecting the
// status, and answers the reply.
func (f *decideFixture) routeTo(t *testing.T, did, opID string, to map[string]any, status int, code string) map[string]any {
	t.Helper()
	return f.expect(t, http.MethodPost, "/api/decisions/"+did+"/route", map[string]any{"opId": opID, "to": to, "expectedRev": f.rev(t, did)}, status, code)
}

var (
	toGrant    = map[string]any{"kind": "grant", "grant": "decide"}
	toOperator = map[string]any{"kind": "operator"}
)

// give gives did to the decide grant.
func (f *decideFixture) give(t *testing.T, did string) {
	t.Helper()
	f.routeTo(t, did, "give-"+fmt.Sprint(f.rev(t, did)), toGrant, http.StatusOK, "")
}

// takeBack takes the decide grant back from did.
func (f *decideFixture) takeBack(t *testing.T, did string) {
	t.Helper()
	f.routeTo(t, did, "back-"+fmt.Sprint(f.rev(t, did)), toOperator, http.StatusOK, "")
}

// readAs reads did as pass.
func (f *decideFixture) readAs(t *testing.T, pass, did string) (int, []byte) {
	t.Helper()
	status, data, _ := f.callAs(t, pass, http.MethodGet, "/api/decisions/"+did, nil)
	return status, data
}

// strictly decodes data into v, refusing a field v does not name.
func strictly(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%.300s: %v", data, err)
	}
}

// view is pass's decide view of did, which must be answered 200.
func (f *decideFixture) view(t *testing.T, pass, did string) proto.DecideView {
	t.Helper()
	status, data := f.readAs(t, pass, did)
	if status != http.StatusOK {
		t.Fatalf("%s's read of %s: %d %.300s", pass, did, status, data)
	}
	var v proto.DecideView
	strictly(t, data, &v)
	return v
}

// list is pass's decide list, its bytes and the list.
func (f *decideFixture) list(t *testing.T, pass string) ([]byte, proto.DecideList) {
	t.Helper()
	status, data, _ := f.callAs(t, pass, http.MethodGet, "/api/me/decisions", nil)
	if status != http.StatusOK {
		t.Fatalf("%s's list: %d %.300s", pass, status, data)
	}
	var l proto.DecideList
	strictly(t, data, &l)
	return data, l
}

// respondAs sends pass's response to did.
func (f *decideFixture) respondAs(t *testing.T, pass, did, responseID string, choice, rev int) (int, []byte, map[string]any) {
	t.Helper()
	return f.callAs(t, pass, http.MethodPost, "/api/decisions/"+did+"/respond", map[string]any{"responseId": responseID, "choice": choice, "expectedRev": rev})
}

// ids are a list's ids.
func ids(l proto.DecideList) []string {
	out := []string{}
	for _, e := range l.Decisions {
		out = append(out, e.ID)
	}
	return out
}

// seedRequest is an approval gate's request as the ledger keeps it, its
// for as given ("" for none).
func seedRequest(forJSON string) string {
	r := `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"authorizes":"deploy may start","subject":{"kind":"plan","plan":"pl-1","digest":"d1"}`
	if forJSON != "" {
		r += `,"for":` + forJSON
	}
	return r + "}"
}

const grantFor = `{"label":"grant:decide","person":""}`

// seed opens n approval gates of request on runs of their own the engine
// does not hold, at most 60 a run (ledger.MaxOpenPerRun), oldest first,
// and answers their ids.
func (f *decideFixture) seed(t *testing.T, n int, request string) []string {
	t.Helper()
	var out []string
	for len(out) < n {
		run := fmt.Sprintf("seed%04d-0000aaaa", f.seeded)
		f.seeded++
		if _, err := f.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
			if err := tx.PutRun(ledger.RunRow{ID: run, CrewID: "seed", Crew: "{}", State: ledger.RunStopped, CreatedAt: tx.Now()}); err != nil {
				return err
			}
			for i := 0; i < 60 && len(out) < n; i++ {
				row, _, err := tx.OpenDecision(ledger.DecisionRow{RunID: run, Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: fmt.Sprintf("g%d", i), Plan: "pl-1", PlanDigest: "d1",
					Fingerprint: fmt.Sprintf("fp-%s-%d", run, i), Request: request, Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
				if err != nil {
					return err
				}
				out = append(out, row.ID())
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// rawWrite runs stmt on the server's ledger file through a connection of
// its own: a row's text no writer of the ledger's would keep.
func (f *decideFixture) rawWrite(t *testing.T, stmt string, args ...any) {
	t.Helper()
	path := filepath.Join(f.srv.cfg.DataDir, "runs", ledger.File)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

// numberOf is a decision id's n.
func numberOf(did string) int64 {
	n, _ := ledger.ParseDecisionID(did)
	return n
}

// apiPatterns are the patterns this package's non-test sources register
// (every string literal of the shape "METHOD /path", the constants and
// the Dispatch routes' table among them), so a route added later is
// walked too.
func apiPatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE) /[^" ]*)"`)
	seen := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) < 100 || !slices.Contains(out, "GET /api/events") || !slices.Contains(out, decisionRoutePattern) {
		t.Fatalf("the patterns found: %d", len(out))
	}
	return out
}

// The decide grant admits its four routes alone: every pattern the
// package registers, asked with a decide-only pass, a host-and-decide
// pass, a host-only pass, a pass the provider does not know and no
// credential. Outside the four, the decide-only pass is answered exactly
// as the unknown one (and as no credential where the route needs one:
// the events stream, the inbox, the operator's list), the host-and-decide
// pass exactly as the host-only one; the four answer both decide passes
// alike. The WebSocket routes are asked by real handshakes too: the
// decide-only pass refused (4401, or 401 before the upgrade) as the
// unknown one, the host-and-decide pass as the host-only one.
func TestDecideGrantAdmitsDecisionRoutesOnly(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	four := map[string]bool{"GET /api/me": true, "GET /api/me/decisions": true, "GET /api/decisions/{did}": true, "POST /api/decisions/{did}/respond": true}
	asNone := map[string]bool{"GET /api/events": true, "GET /api/inbox": true, "GET /api/inbox/item": true, "GET /api/inbox/track": true, "GET /api/decisions": true}
	values := map[string]string{"did": did, "id": f.sid, "run": f.run}
	wildcard := regexp.MustCompile(`\{([a-zA-Z]+)(\.\.\.)?\}`)
	call := func(method, path, tok string) (int, string) {
		var body io.Reader
		if method != http.MethodGet {
			body = strings.NewReader("{}")
		}
		req, err := http.NewRequest(method, f.http.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	fill := func(path string) string {
		return wildcard.ReplaceAllStringFunc(path, func(w string) string {
			if v, ok := values[wildcard.FindStringSubmatch(w)[1]]; ok {
				return v
			}
			return "x1"
		})
	}
	// What a WebSocket route answers a pass by a real handshake: refused
	// with its status, let in, or closed with its code after the hello.
	shake := func(path, tok string) string {
		ctx, cancel := context.WithTimeout(t.Context(), testWait)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, strings.Replace(f.http.URL, "http://", "ws://", 1)+path+"?token="+url.QueryEscape(tok), nil)
		if err != nil {
			if resp == nil {
				t.Fatalf("%s: %v", path, err)
			}
			return fmt.Sprintf("refused %d", resp.StatusCode)
		}
		defer c.CloseNow()
		if path == "/ws/host" {
			return "let in" // a host's connection waits for its own first message
		}
		_ = c.Write(ctx, websocket.MessageBinary, proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1}))
		if _, _, err := c.Read(ctx); err != nil {
			return fmt.Sprintf("closed %d", websocket.CloseStatus(err))
		}
		return "let in"
	}
	walked := 0
	for _, pattern := range apiPatterns(t) {
		path, ok := strings.CutPrefix(pattern, "GET /ws/")
		if !ok {
			continue
		}
		path = fill("/ws/" + path)
		walked++
		ds, us := shake(path, passAna), shake(path, passUnknown)
		if ds != us || ds != fmt.Sprintf("closed %d", proto.CloseUnauthorized) && ds != "refused 401" {
			t.Errorf("%s by handshake: decide-only %s, an unknown pass %s", pattern, ds, us)
		}
		if hs, hos := shake(path, passHostAna), shake(path, passHostOnly); hs != hos {
			t.Errorf("%s by handshake: host-and-decide %s, host-only %s", pattern, hs, hos)
		}
	}
	if walked < 3 {
		t.Fatalf("%d WebSocket routes walked", walked)
	}
	for _, pattern := range apiPatterns(t) {
		method, path, _ := strings.Cut(pattern, " ")
		path = fill(path)
		ds, db := call(method, path, passAna)
		hs, hb := call(method, path, passHostAna)
		if four[pattern] {
			if pattern == "GET /api/me" {
				if ds != http.StatusOK || hs != http.StatusOK || !strings.Contains(db, `"decide"`) || !strings.Contains(hb, `"decide"`) {
					t.Errorf("%s: %d %s, %d %s", pattern, ds, db, hs, hb)
				}
				continue
			}
			if ds != hs || db != hb || ds == http.StatusUnauthorized {
				t.Errorf("%s: the decide passes answered %d %.200s and %d %.200s", pattern, ds, db, hs, hb)
			}
			continue
		}
		// What a pass answers, and whether it answers so every time.
		ref := func(tok string) (int, string, bool) {
			s1, b1 := call(method, path, tok)
			s2, b2 := call(method, path, tok)
			return s1, b1, s1 == s2 && b1 == b2
		}
		us, ub, stable := ref(passUnknown)
		if ds != us || stable && db != ub {
			t.Errorf("%s: decide-only %d %.200s, an unknown pass %d %.200s", pattern, ds, db, us, ub)
		}
		hos, hob, hostable := ref(passHostOnly)
		if hs != hos || hostable && hb != hob {
			t.Errorf("%s: host-and-decide %d %.200s, host-only %d %.200s", pattern, hs, hb, hos, hob)
		}
		if asNone[pattern] {
			if ns, nb := call(method, path, ""); ds != ns || db != nb || ds != http.StatusUnauthorized {
				t.Errorf("%s: decide-only %d %.200s, no credential %d %.200s", pattern, ds, db, ns, nb)
			}
		}
	}
}

// GET /api/me answers every principal: the chain's tokens, a link, a
// provider's principal by each grant and with host and decide; none 401;
// a subject and a name of characters JSON escapes six bytes wide fit
// proto.MaxMeReply.
func TestMeAnswersEveryPrincipal(t *testing.T) {
	f := newDecideFixture(t, nil)
	wide := ext.Principal{Subject: strings.Repeat("<", ext.MaxSubject), Name: strings.Repeat("&", ext.MaxPrincipalName), Grants: []ext.Grant{ext.GrantAdmin, ext.GrantHost, ext.GrantDecide}}
	f.id.Add("pass.wide-1", wide)
	_, lo := f.do(http.MethodPost, "/api/sessions/"+f.sid+"/links", adminToken, map[string]any{"role": "view"})
	link, _ := lo["token"].(string)
	for _, tc := range []struct {
		tok, want string
	}{
		{adminToken, `{"grants":["admin","host"]}`},
		{"test-host-token", `{"grants":["host"]}`},
		{link, `{"grants":[]}`},
		{passAdmin, `{"subject":"nate","name":"Nate","grants":["admin","host"]}`},
		{passHostOnly, `{"subject":"ana","name":"Ana","grants":["host"]}`},
		{passAna, `{"subject":"ana","name":"Ana","grants":["decide"]}`},
		{passHostAna, `{"subject":"ana","name":"Ana","grants":["host","decide"]}`},
	} {
		status, data, _ := f.callAs(t, tc.tok, http.MethodGet, "/api/me", nil)
		if status != http.StatusOK || strings.TrimSpace(string(data)) != tc.want {
			t.Errorf("%.12s: %d %s, want %s", tc.tok, status, data, tc.want)
		}
	}
	for _, tok := range []string{"", passUnknown} {
		f.expectAs(t, tok, http.MethodGet, "/api/me", nil, http.StatusUnauthorized, "unauthorized")
	}
	status, data, _ := f.callAs(t, "pass.wide-1", http.MethodGet, "/api/me", nil)
	if status != http.StatusOK || len(data) > proto.MaxMeReply || !bytes.Contains(data, []byte(`\`+`u003c`)) {
		t.Fatalf("the widest reply: %d, %d bytes", status, len(data))
	}
}

// The route to the grant asks only whether an identity provider is
// registered: with none, 409 no_identity and no row; with one, the
// target and the row.
func TestRouteToGrantNeedsAProvider(t *testing.T) {
	plain := newDecisionFixture(t, nil, nil)
	plain.ask(t, approvalBody("r1"), http.StatusCreated, "")
	out := plain.expect(t, http.MethodPost, "/api/decisions/dr-1/route", map[string]any{"opId": "g1", "to": toGrant, "expectedRev": 1}, http.StatusConflict, "no_identity")
	if !strings.Contains(apiMessage(out), "no identity provider is registered") {
		t.Fatalf("the refusal: %v", out)
	}
	rec, _, _ := plain.srv.ledger.Decision(t.Context(), 1)
	if rec.Rev != 1 || crew.GivenToGrant(rec.Request) || len(rec.History) != 2 {
		t.Fatalf("the refused route wrote: %+v", rec.History)
	}
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	if rec := f.record(t, did); rec.Rev != 2 || !crew.GivenToGrant(rec.Request) || rec.History[len(rec.History)-1].Kind != ledger.HistoryRouted {
		t.Fatalf("the route to the grant: %+v", rec.DecisionRow)
	}
}

// A decide principal reads one decision's question and its own receipts,
// nothing else: none of the markers its request's evidence, its history,
// another responder or other changes carry, nor the run's or the
// session's id, open and once answered.
func TestDecideReadCarriesNothingElse(t *testing.T) {
	f := newDecideFixture(t, nil)
	body := approvalBody("r1")
	body["evidence"] = []string{"session:" + f.sid, "run:" + f.run, "task:" + f.run + "/build"}
	did := entityOf(t, f.ask(t, body, http.StatusCreated, ""), "decision")["id"].(string)
	other := f.open(t, "r2")
	f.routeTo(t, did, "p1", map[string]any{"kind": "person", "name": "zelda"}, http.StatusOK, "")
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "marker-comment-6b1d", "expectedRev": f.rev(t, did)}, http.StatusOK, "")
	f.give(t, did)
	f.give(t, other)
	f.expect(t, http.MethodPost, "/api/decisions/"+other+"/comment", map[string]any{"opId": "c2", "kind": "comment", "text": "marker-other-0e7a", "expectedRev": f.rev(t, other)}, http.StatusOK, "")
	// Another subject's refusal (a stale rev), kept with its name.
	f.callAs(t, passBob, http.MethodPost, "/api/decisions/"+did+"/respond", map[string]any{"responseId": "b1", "choice": 0, "expectedRev": 1})
	f.createSession("cat")
	markers := []string{"marker-comment-6b1d", "marker-other-0e7a", "Bob", "bob", "zelda", "CND-1", f.sid, f.run, other, "evidence", "history", "admin"}
	clean := func(what string, data []byte) {
		t.Helper()
		for _, m := range markers {
			if bytes.Contains(data, []byte(m)) {
				t.Errorf("%s holds %q: %s", what, m, data)
			}
		}
	}
	status, data := f.readAs(t, passAna, did)
	if status != http.StatusOK {
		t.Fatalf("the read: %d %s", status, data)
	}
	clean("the open view", data)
	if status, out, _ := f.respondAs(t, passAna, did, "a1", 0, f.rev(t, did)); status != http.StatusCreated {
		t.Fatalf("the answer: %d %s", status, out)
	} else {
		clean("the answer's reply", out)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	status, data = f.readAs(t, passAna, did)
	if status != http.StatusOK {
		t.Fatalf("the read once answered: %d %s", status, data)
	}
	clean("the outcome view", data)
	var v proto.DecideView
	strictly(t, data, &v)
	if v.State != "" || v.Question != "" || v.Outcome != proto.OutcomeApplied || len(v.Own) != 1 || v.Own[0].ResponseID != "a1" {
		t.Fatalf("the outcome view: %+v", v)
	}
}

// jsonNames are the JSON names of a struct type's fields.
func jsonNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	return out
}

// Every field a decide read returns is a row of the plan's tables (The
// decide view, The decide list), so a field added without them fails.
func TestDecideViewIsTheTable(t *testing.T) {
	view := []string{"id", "rev", "state", "type", "question", "context", "options", "recommendation", "uncertainty", "authorizes", "createdAt", "own", "outcome", "outcomeCode"}
	for _, tc := range []struct {
		typ  reflect.Type
		want []string
	}{
		{reflect.TypeFor[proto.DecideView](), view},
		{reflect.TypeFor[proto.DecideReceipt](), []string{"k", "responseId", "status", "code", "rev", "at", "choice"}},
		{reflect.TypeFor[proto.DecideOption](), []string{"label", "tradeoffs"}},
		{reflect.TypeFor[proto.DecideEntry](), view[:2]},
		{reflect.TypeFor[proto.DecideList](), []string{"decisions", "cut"}},
		{reflect.TypeFor[proto.DecideReply](), []string{"receipt", "decision"}},
		{reflect.TypeFor[proto.DecideRefusal](), []string{"error", "receipt"}},
		{reflect.TypeFor[proto.MeReply](), []string{"subject", "name", "grants"}},
	} {
		if got := jsonNames(tc.typ); !slices.Equal(got, tc.want) {
			t.Errorf("%s's fields %v, the table's %v", tc.typ.Name(), got, tc.want)
		}
	}
}

// An assignment given to the grant reads the engine's two options, Done
// at 0 and Not done at 1, and takes a choice of 1.
func TestDecideViewOfAnAssignment(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := entityOf(t, f.ask(t, map[string]any{"requestId": "r1", "type": "assignment", "question": "Run the migration on staging"}, http.StatusCreated, ""), "decision")["id"].(string)
	f.give(t, did)
	v := f.view(t, passAna, did)
	if v.Type != ledger.DecisionAssignment || len(v.Options) != 2 || v.Options[0].Label != "Done" || v.Options[1].Label != "Not done" {
		t.Fatalf("the assignment's view: %+v", v)
	}
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 1, v.Rev); status != http.StatusCreated {
		t.Fatalf("Not done: %d %s", status, data)
	}
	if rec := f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied)); rec["answer"] != "Not done" {
		t.Fatalf("the answer: %v", rec["answer"])
	}
}

// holdAt holds the decide seam pick names once, telling at when it is
// reached; the gate it returns releases it.
func (f *decideFixture) holdAt(t *testing.T, list bool) (at, gate chan struct{}) {
	t.Helper()
	at, gate = make(chan struct{}), make(chan struct{})
	var once sync.Once
	hold := func() { once.Do(func() { close(at); <-gate }) }
	if list {
		f.srv.decideSeam.Store(&decideSeams{afterList: hold})
	} else {
		f.srv.decideSeam.Store(&decideSeams{afterRead: hold})
	}
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	return at, gate
}

// reached waits for at, failing t when the step never gets there.
func reached(t *testing.T, at chan struct{}, what string) {
	t.Helper()
	select {
	case <-at:
	case <-time.After(testWait):
		t.Fatalf("%s never reached its seam", what)
	}
}

// A read held after its read of the record while the grant is taken back
// and a comment committed answers the view of the rev it judged; the next
// read is 404.
func TestDecideReadJudgesTheRowItReturns(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	at, gate := f.holdAt(t, false)
	type reply struct {
		status int
		data   []byte
	}
	done := make(chan reply, 1)
	go func() {
		status, data, _ := f.callAs(t, passAna, http.MethodGet, "/api/decisions/"+did, nil)
		done <- reply{status, data}
	}()
	reached(t, at, "the read")
	f.takeBack(t, did)
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "later", "expectedRev": f.rev(t, did)}, http.StatusOK, "")
	close(gate)
	got := <-done
	var v proto.DecideView
	strictly(t, got.data, &v)
	if got.status != http.StatusOK || v.Rev != 2 || v.State != ledger.DecisionOpen {
		t.Fatalf("the held read: %d %+v", got.status, v)
	}
	f.srv.decideSeam.Store(nil)
	if status, data := f.readAs(t, passAna, did); status != http.StatusNotFound {
		t.Fatalf("the next read: %d %s", status, data)
	}
}

// Admission ends with the answer, for a holder with no response of its
// own (404), the responder reading the outcome alone; and with a
// take-back through the ledger alone, its run stopped, for every holder.
func TestDecideAdmissionEnds(t *testing.T) {
	f := newDecideFixture(t, nil)
	first, second := f.open(t, "r1"), f.open(t, "r2")
	f.give(t, first)
	f.give(t, second)
	if status, data, _ := f.respondAs(t, passBob, first, "b1", 0, 2); status != http.StatusCreated {
		t.Fatalf("bob's answer: %d %s", status, data)
	}
	f.decisionWhen(t, first, "applied", inState(ledger.DecisionApplied))
	if status, data := f.readAs(t, passAna, first); status != http.StatusNotFound || !bytes.Contains(data, []byte(`"no such decision"`)) {
		t.Fatalf("ana's read of bob's answered decision: %d %s", status, data)
	}
	if status, _ := f.readAs(t, passAna, "dr-999"); status != http.StatusNotFound {
		t.Fatal("a decision that does not exist")
	}
	v := f.view(t, passBob, first)
	if v.State != "" || v.Question != "" || v.Options != nil || v.CreatedAt != nil || v.Outcome != proto.OutcomeApplied || len(v.Own) != 1 {
		t.Fatalf("bob's outcome view: %+v", v)
	}
	f.expect(t, http.MethodPost, "/api/runs/"+f.run+"/stop", nil, http.StatusOK, "")
	f.takeBack(t, second)
	for _, pass := range []string{passAna, passBob, passHostAna} {
		if status, _ := f.readAs(t, pass, second); status != http.StatusNotFound {
			t.Fatalf("%s read %s once taken back: %d", pass, second, status)
		}
	}
}

// Each send of one subject keeps its own receipt under its responseId
// (two tabs): the second tab's, sent while the first's answer is not yet
// committed, refused and kept; the same body again is 200 repeat with its
// saved receipt, its rev the send's and the view beside it the decision
// as it is now; after a kept stale_decision the same id with the new rev
// is response_conflict and a new id is accepted.
func TestDecideOwnCarriesTheResponseID(t *testing.T) {
	f := newDecideFixture(t, nil)
	did, other := f.open(t, "r1"), f.open(t, "r2")
	f.give(t, did)
	f.give(t, other)
	// The second tab's send held after its read of the open decision,
	// while the first tab's answer commits.
	at, gate := f.holdAt(t, false)
	second := make(chan int, 1)
	go func() {
		status, _, _ := f.respondAs(t, passAna, did, "tab-b", 1, 2)
		second <- status
	}()
	reached(t, at, "the second tab")
	f.srv.decideSeam.Store(nil)
	status, first, _ := f.respondAs(t, passAna, did, "tab-a", 0, 2)
	if status != http.StatusCreated {
		close(gate)
		t.Fatalf("the first tab: %d %s", status, first)
	}
	close(gate)
	if status := <-second; status != http.StatusConflict {
		t.Fatalf("the second tab: %d", status)
	}
	var r1 proto.DecideReply
	strictly(t, first, &r1)
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	v := f.view(t, passAna, did)
	if len(v.Own) != 2 || v.Own[0].ResponseID != "tab-a" || v.Own[0].Status != "accepted" || v.Own[1].ResponseID != "tab-b" || v.Own[1].Code != crew.CodeDecisionAnswered {
		t.Fatalf("own: %+v", v.Own)
	}
	status, again, _ := f.respondAs(t, passAna, did, "tab-a", 0, 2)
	var r2 proto.DecideReply
	strictly(t, again, &r2)
	if status != http.StatusOK || r2.Receipt.K != r1.Receipt.K || !r2.Receipt.At.Equal(r1.Receipt.At) || r2.Receipt.ResponseID != "tab-a" {
		t.Fatalf("the same body again: %d %s, first %s", status, again, first)
	}
	// The repeat is the past act: its status and rev as sent, the view the
	// decision now (applied since, its rev moved, the outcome alone).
	now := f.rev(t, did)
	if r2.Receipt.Status != "accepted" || r2.Receipt.Rev != r1.Receipt.Rev || r2.Decision == nil || r2.Decision.Rev != now || now <= r2.Receipt.Rev ||
		r2.Decision.State != "" || r2.Decision.Outcome != proto.OutcomeApplied {
		t.Fatalf("the repeat's receipt and view: %s (rev now %d)", again, now)
	}
	if status, data, out := f.respondAs(t, passAna, other, "s1", 0, 1); status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision {
		t.Fatalf("a stale send: %d %s", status, data)
	}
	if status, data, out := f.respondAs(t, passAna, other, "s1", 0, 2); status != http.StatusConflict || codeOf(out) != crew.CodeResponseConflict {
		t.Fatalf("the same id at the new rev: %d %s", status, data)
	}
	if status, data, _ := f.respondAs(t, passAna, other, "s2", 0, 2); status != http.StatusCreated {
		t.Fatalf("a new id: %d %s", status, data)
	}
}

// Of a decision answered or final whose outcome it reads, a decide
// principal's send of one of its own responses again is answered from its
// saved receipt; any other send is refused decision_answered or
// decision_closed with the server's words, no receipt, nothing written.
func TestDecideOutcomeTakesOnlyItsOwnSendAgain(t *testing.T) {
	f := newDecideFixture(t, nil)
	answered, cancelled := f.open(t, "r1"), f.open(t, "r2")
	f.give(t, answered)
	f.give(t, cancelled)
	if status, data, _ := f.respondAs(t, passAna, answered, "a1", 0, 2); status != http.StatusCreated {
		t.Fatalf("the answer: %d %s", status, data)
	}
	f.decisionWhen(t, answered, "applied", inState(ledger.DecisionApplied))
	// A kept refusal of its own (a stale rev), then the operator's cancel.
	if status, data, out := f.respondAs(t, passAna, cancelled, "s1", 0, 1); status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision {
		t.Fatalf("the stale send: %d %s", status, data)
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+cancelled+"/cancel", map[string]any{"commandId": "x1", "why": "no", "expectedRev": f.rev(t, cancelled)}, http.StatusOK, "")
	for _, tc := range []struct{ did, code string }{{answered, crew.CodeDecisionAnswered}, {cancelled, crew.CodeDecisionClosed}} {
		rows := len(f.record(t, tc.did).Responses)
		for _, id := range []string{"n1", "n2"} {
			status, data, _ := f.respondAs(t, passAna, tc.did, id, 0, f.rev(t, tc.did))
			var r proto.DecideRefusal
			strictly(t, data, &r)
			if status != http.StatusConflict || r.Error.Code != tc.code || r.Error.Message != decideWords(tc.code, tc.did) || r.Receipt != nil {
				t.Fatalf("a new send to %s: %d %s", tc.did, status, data)
			}
		}
		if got := len(f.record(t, tc.did).Responses); got != rows {
			t.Fatalf("%s's responses: %d, were %d", tc.did, got, rows)
		}
	}
	if status, data, _ := f.respondAs(t, passAna, answered, "a1", 0, 2); status != http.StatusOK {
		t.Fatalf("its answer again: %d %s", status, data)
	}
	status, data, out := f.respondAs(t, passAna, cancelled, "s1", 0, 1)
	var r proto.DecideRefusal
	strictly(t, data, &r)
	if status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision || r.Receipt == nil || r.Receipt.ResponseID != "s1" {
		t.Fatalf("its refused send again: %d %s", status, data)
	}
	// bob, who sent nothing to either, reads neither and sends to neither.
	for _, did := range []string{answered, cancelled} {
		if status, _, _ := f.respondAs(t, passBob, did, "b1", 0, f.rev(t, did)); status != http.StatusNotFound {
			t.Fatalf("bob's send to %s: %d", did, status)
		}
	}
	// What is its own is the caller's response alone: of a decision bob
	// answered, ana, whose own refusal is kept, sends under bob's id.
	both := f.open(t, "r3")
	f.give(t, both)
	if status, data, out := f.respondAs(t, passAna, both, "s3", 0, 1); status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision {
		t.Fatalf("ana's stale send: %d %s", status, data)
	}
	f.respond(t, passBob, both, "b3", 0, 2, http.StatusCreated, "")
	f.decisionWhen(t, both, "applied", inState(ledger.DecisionApplied))
	rows := len(f.record(t, both).Responses)
	status, data, _ = f.respondAs(t, passAna, both, "b3", 0, 2)
	var under proto.DecideRefusal
	strictly(t, data, &under)
	if status != http.StatusConflict || under.Error.Code != crew.CodeDecisionAnswered || under.Error.Message != decideWords(crew.CodeDecisionAnswered, both) || under.Receipt != nil {
		t.Fatalf("ana's send under bob's id: %d %s", status, data)
	}
	if got := len(f.record(t, both).Responses); got != rows {
		t.Fatalf("%s's responses: %d, were %d", both, got, rows)
	}
}

// The response id a decision router's answer is kept under is refused to
// a decide principal, 400 invalid in the server's words, nothing written:
// the page mints ids of its own.
func TestDecideRouterIDIsRefused(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	status, data, _ := f.respondAs(t, passAna, did, crew.RouterResponseID, 0, 2)
	var r proto.DecideRefusal
	strictly(t, data, &r)
	if status != http.StatusBadRequest || r.Error.Code != crew.CodeInvalid || r.Error.Message != decideWords(crew.CodeInvalid, did) || r.Receipt != nil {
		t.Fatalf("the router's id: %d %s", status, data)
	}
	if rec := f.record(t, did); len(rec.Responses) != 0 || rec.Rev != 2 {
		t.Fatalf("the refused send wrote: %+v", rec.Responses)
	}
}

// A refusal past the decision's bound of kept refusals carries no receipt:
// nothing of it was kept, and its send is judged again.
func TestDecideRefusalNotKeptCarriesNoReceipt(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	for i := range ledger.MaxDecisionRefusals {
		if status, data, out := f.respondAs(t, passBob, did, fmt.Sprintf("b%d", i), 0, 1); status != http.StatusConflict || codeOf(out) != crew.CodeStaleDecision {
			t.Fatalf("bob's stale send %d: %d %s", i, status, data)
		}
	}
	status, data, _ := f.respondAs(t, passAna, did, "a1", 0, 1)
	var r proto.DecideRefusal
	strictly(t, data, &r)
	if status != http.StatusConflict || r.Error.Code != crew.CodeStaleDecision || r.Receipt != nil {
		t.Fatalf("the refusal past the bound: %d %s", status, data)
	}
	if v := f.view(t, passAna, did); len(v.Own) != 0 || v.Outcome != "" {
		t.Fatalf("ana's view: %+v", v)
	}
}

// One list of a subject's is read at a time: another of the same subject
// while one is held after its ledger read is 429, nothing read; another
// subject's is answered meanwhile; once the first is answered the same
// subject lists again.
func TestDecideListOneAtATime(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	at, gate := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	f.srv.decideSeam.Store(&decideSeams{afterList: func() {
		if first.CompareAndSwap(false, true) {
			close(at)
			<-gate
		}
	}})
	t.Cleanup(func() { f.srv.decideSeam.Store(nil) })
	done := make(chan int, 1)
	go func() {
		status, _, _ := f.callAs(t, passAna, http.MethodGet, "/api/me/decisions", nil)
		done <- status
	}()
	reached(t, at, "the first list")
	for _, pass := range []string{passAna, passHostAna} {
		if status, data, out := f.callAs(t, pass, http.MethodGet, "/api/me/decisions", nil); status != http.StatusTooManyRequests || codeOf(out) != "rate_limited" || bytes.Contains(data, []byte(did)) {
			close(gate)
			t.Fatalf("ana's second list (%s): %d %s", pass, status, data)
		}
	}
	if _, l := f.list(t, passBob); !slices.Equal(ids(l), []string{did}) {
		close(gate)
		t.Fatalf("bob's list meanwhile: %+v", l)
	}
	close(gate)
	if status := <-done; status != http.StatusOK {
		t.Fatalf("the first list: %d", status)
	}
	if _, l := f.list(t, passAna); !slices.Equal(ids(l), []string{did}) {
		t.Fatalf("ana's list after: %+v", l)
	}
}

// A list the ledger cannot read (its open decisions' index missing) is 503
// in the server's words, naming no decision; a decision's read goes on.
func TestDecideListUnavailable(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	f.list(t, passAna)
	f.rawWrite(t, "DROP INDEX decisions_open")
	status, data, out := f.callAs(t, passAna, http.MethodGet, "/api/me/decisions", nil)
	if status != http.StatusServiceUnavailable || codeOf(out) != "ledger_unavailable" || apiMessage(out) != "the decisions could not be read" || bytes.Contains(data, []byte("dr-")) {
		t.Fatalf("the list without its index: %d %s", status, data)
	}
	f.view(t, passAna, did)
}

// The way in a decide route admits by is the one a response's authority
// records, both from how the principal authenticated: an identity's
// subject for a provider's principal, the server's own chain otherwise;
// nothing a request carries beside its credential changes either.
func TestDecideWayInIsTheAuthoritys(t *testing.T) {
	f := newDecideFixture(t, nil)
	for _, tok := range []string{passAna, passHostAna, passAdmin, passAdminDecide, adminToken, "test-host-token", passUnknown} {
		req := httptest.NewRequest(http.MethodGet, "/api/me/decisions?subject=eve&via=identity", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Conductor-Subject", "eve")
		p := f.srv.authenticate(req, false)
		who, auth := admittee(p), f.srv.authorityOf(withPrincipal(req, p))
		if who.Via != auth.Via || who.Subject != auth.Subject || who.Subject == "eve" {
			t.Errorf("%.12s: admitted as %s %q, its authority %s %q", tok, who.Via, who.Subject, auth.Via, auth.Subject)
		}
		if wantIdentity := strings.HasPrefix(tok, "pass.") && tok != passUnknown; (who.Via == crew.ViaIdentity) != wantIdentity {
			t.Errorf("%.12s: way in %s", tok, who.Via)
		}
	}
}

// The refusals a decide principal is answered carry the server's fixed
// words of their codes and its own receipt reduced, never another
// responder's name or subject, within proto.MaxDecideRefusalWords.
func TestDecideRefusalsNameNobody(t *testing.T) {
	f := newDecideFixture(t, nil)
	answered, stale, taken := f.open(t, "r1"), f.open(t, "r2"), f.open(t, "r3")
	for _, did := range []string{answered, stale, taken} {
		f.give(t, did)
	}
	// Each change committed between the route's read and its transaction:
	// bob's answer, then a take-back.
	between := func(did string, change func()) []byte {
		t.Helper()
		at, gate := f.holdAt(t, false)
		done := make(chan []byte, 1)
		go func() {
			_, data, _ := f.respondAs(t, passAna, did, "a-"+did, 0, 2)
			done <- data
		}()
		reached(t, at, "the response")
		f.srv.decideSeam.Store(nil)
		change()
		close(gate)
		return <-done
	}
	decisionAnswered := between(answered, func() {
		f.respond(t, passBob, answered, "b1", 0, 2, http.StatusCreated, "")
	})
	notApprover := between(taken, func() { f.takeBack(t, taken) })
	_, staleDecision, _ := f.respondAs(t, passAna, stale, "a2", 0, 1)
	of := map[string]string{crew.CodeDecisionAnswered: answered, crew.CodeStaleDecision: stale, crew.CodeNotApprover: taken}
	for code, data := range map[string][]byte{crew.CodeDecisionAnswered: decisionAnswered, crew.CodeStaleDecision: staleDecision, crew.CodeNotApprover: notApprover} {
		var r proto.DecideRefusal
		strictly(t, data, &r)
		if r.Error.Code != code || r.Error.Message != decideWords(code, of[code]) || len(r.Error.Message) > proto.MaxDecideRefusalWords || r.Receipt == nil || r.Receipt.Code != code || r.Receipt.Status != "refused" {
			t.Errorf("%s: %s", code, data)
		}
		for _, m := range []string{"Bob", "bob", "admin", "user:"} {
			if bytes.Contains(data, []byte(m)) {
				t.Errorf("%s names %q: %s", code, m, data)
			}
		}
	}
	for _, code := range []string{crew.CodeDecisionAnswered, crew.CodeDecisionClosed, crew.CodeStaleDecision, crew.CodeStalePolicy, crew.CodeNotApprover, crew.CodeResponseConflict,
		crew.CodeNotHeld, crew.CodeInvalid, crew.CodeDecisionLimit, crew.CodeDecisionMoved, "anything else"} {
		if w := decideWords(code, ledger.DecisionID(1<<63-1)); len(w) > proto.MaxDecideRefusalWords {
			t.Errorf("%s's words are %d bytes", code, len(w))
		}
	}
}

// The recheck admits the same subject still holding the decide grant: the
// answer applied. One now without it, another subject, a provider late
// through the guard: superseded, the view not_used. A take-back sent at
// the answered rev while the recheck is held at the provider's gate is
// decision_answered, and once released the answer is applied.
func TestConfirmAuthorityAdmitsDecide(t *testing.T) {
	f := newDecideFixture(t, nil)
	answer := func(did string) {
		t.Helper()
		if status, data, _ := f.respondAs(t, passAna, did, "a-"+did, 0, f.rev(t, did)); status != http.StatusCreated {
			t.Fatalf("the answer to %s: %d %s", did, status, data)
		}
	}
	did := f.open(t, "r1")
	f.give(t, did)
	answer(did)
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	for i, then := range []ext.Principal{
		{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantHost}},
		{Subject: "eve", Name: "Eve", Grants: []ext.Grant{ext.GrantDecide}},
		{},
	} {
		did := f.open(t, fmt.Sprintf("n%d", i))
		f.give(t, did)
		gate := f.id.recheckAs(passAna, then, then.Subject == "")
		if gate != nil {
			// Late: the gate is never opened before the guard's own wait ends.
			go func() { <-f.id.reached }()
		}
		answer(did)
		f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
		if v := f.view(t, passAna, did); v.Outcome != proto.OutcomeNotUsed || v.OutcomeCode != "" {
			t.Fatalf("%d: the view %+v", i, v)
		}
		if gate != nil {
			close(gate)
		}
		f.id.mu.Lock()
		f.id.then[passAna], f.id.hold[passAna] = nil, nil
		f.id.mu.Unlock()
	}
	// Held at the gate: the take-back meets the answer.
	long := time.Hour
	extWaiting.Store(&long)
	t.Cleanup(func() { extWaiting.Store(nil) })
	did = f.open(t, "held")
	f.give(t, did)
	gate := f.id.recheckAs(passAna, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}}, true)
	done := make(chan int, 1)
	go func() {
		status, _, _ := f.respondAs(t, passAna, did, "a-held", 0, 2)
		done <- status
	}()
	select {
	case <-f.id.reached:
	case <-time.After(testWait):
		close(gate)
		t.Fatal("the recheck never reached the provider")
	}
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/route", map[string]any{"opId": "back-1", "to": toOperator, "expectedRev": 3}, http.StatusConflict, crew.CodeDecisionAnswered)
	close(gate)
	if status := <-done; status != http.StatusCreated {
		t.Fatalf("the held answer: %d", status)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
}

// A pass the provider stops admitting is 401 at the next list, read and
// response, nothing cached; the grant gives no connection a recheck would
// watch (GET /api/events is the admin's).
func TestDecideReadsAskTheProvider(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	f.list(t, passAna)
	f.view(t, passAna, did)
	before := f.id.Calls()
	f.id.Remove(passAna)
	f.expectAs(t, passAna, http.MethodGet, "/api/me/decisions", nil, http.StatusUnauthorized, "unauthorized")
	f.expectAs(t, passAna, http.MethodGet, "/api/decisions/"+did, nil, http.StatusUnauthorized, "unauthorized")
	if status, data, _ := f.respondAs(t, passAna, did, "a1", 0, 2); status != http.StatusUnauthorized {
		t.Fatalf("the response: %d %s", status, data)
	}
	if n := f.id.Calls() - before; n != 3 {
		t.Fatalf("the provider was asked %d times, want once a request", n)
	}
	f.expectAs(t, passBob, http.MethodGet, "/api/events", nil, http.StatusUnauthorized, "unauthorized")
}

// One table of records (target, state, an own response) against every
// principal: the read's answer, the list's, the respond route's check and
// the response's transaction agree with crew.AdmissionOf on every row,
// AdmissionOf over the row alone is AdmissionOf over the record for an
// open one, and the recheck's verdict is crew.Admits over the entries the
// response's transaction judged, restricted to the grant its authority
// names.
func TestAdmitsIsTheOneCheck(t *testing.T) {
	f := newDecideFixture(t, nil)
	type row struct {
		did         string
		given, open bool
		own         string // the response of its own ana sent, "" for none
	}
	var rows []row
	// Open: given and not; answered by ana and by bob; one cancelled.
	for i, given := range []bool{true, false} {
		did := f.open(t, fmt.Sprintf("o%d", i))
		if given {
			f.give(t, did)
		}
		rows = append(rows, row{did: did, given: given, open: true})
	}
	for i, who := range []string{passAna, passBob} {
		did := f.open(t, fmt.Sprintf("a%d", i))
		f.give(t, did)
		if status, data, _ := f.respondAs(t, who, did, "r-"+did, 0, 2); status != http.StatusCreated {
			t.Fatalf("the answer: %d %s", status, data)
		}
		f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
		own := ""
		if who == passAna {
			own = "r-" + did
		}
		rows = append(rows, row{did: did, given: true, own: own})
	}
	cancelled := f.open(t, "c1")
	f.give(t, cancelled)
	f.expect(t, http.MethodPost, "/api/decisions/"+cancelled+"/cancel", map[string]any{"commandId": "x1", "why": "no", "expectedRev": 2}, http.StatusOK, "")
	rows = append(rows, row{did: cancelled, given: true})
	principals := map[string]principal{}
	for _, pass := range []string{passAna, passHostAna, passHostOnly, passAdmin, passAdminDecide, passUnknown} {
		principals[pass] = f.srv.tokenPrincipal(&http.Request{Header: http.Header{}, URL: &url.URL{}}, pass)
	}
	principals["workbench"] = f.srv.tokenPrincipal(&http.Request{Header: http.Header{}, URL: &url.URL{}}, adminToken)
	principals["no credential"] = principal{}
	_, list := f.list(t, passAna)
	for name, p := range principals {
		for _, r := range rows {
			rec := f.record(t, r.did)
			a := decideAdmission(rec, p)
			if alone := decideAdmission(ledger.DecisionRecord{DecisionRow: rec.DecisionRow}, p); rec.State == ledger.DecisionOpen && alone != a {
				t.Errorf("%s %s: over the row %d, over the record %d", name, r.did, alone, a)
			}
			if !p.decider() {
				continue
			}
			pass := name
			status, _ := f.readAs(t, pass, r.did)
			if (status == http.StatusOK) != (a == crew.AdmitOpen || a == crew.AdmitOutcome) || (status == http.StatusNotFound) != (a == crew.AdmitNone) {
				t.Errorf("%s reads %s: %d, admission %d", name, r.did, status, a)
			}
			if pass == passAna && slices.Contains(ids(list), r.did) != (a == crew.AdmitOpen) {
				t.Errorf("ana's list and %s's admission %d disagree: %v", r.did, a, ids(list))
			}
		}
	}
	// A decision router reaches no route (no credential is a router's): its
	// admission is AdmitNone on every row. internal/crew's TestAdmission
	// holds a router's rows whole.
	for _, r := range rows {
		if a := crew.AdmissionOf(f.record(t, r.did), crew.Admittee{Via: crew.ViaRouter}); a != crew.AdmitNone {
			t.Errorf("a router's admission to %s: %d", r.did, a)
		}
	}
	// The respond route's check and the transaction's, for each decide
	// principal on every row, judged at once before its send: AdmitNone is
	// 404, AdmitOutcome the route's 409 with nothing written, AdmitOpen
	// taken (and the next principal then reads its outcome).
	want := map[crew.Admission]int{crew.AdmitNone: http.StatusNotFound, crew.AdmitOutcome: http.StatusConflict, crew.AdmitOpen: http.StatusCreated}
	for i, pass := range []string{passAna, passHostAna} {
		for _, r := range rows {
			rec := f.record(t, r.did)
			a := decideAdmission(rec, principals[pass])
			status, data, _ := f.respondAs(t, pass, r.did, fmt.Sprintf("z%d-%s", i, r.did), 0, rec.Rev)
			if status != want[a] || a != crew.AdmitOpen && len(f.record(t, r.did).Responses) != len(rec.Responses) {
				t.Errorf("%.12s answers %s, admission %d: %d %s", pass, r.did, a, status, data)
			}
		}
	}
	// The recheck's verdict is Admits over the entries the response was
	// judged by, for the grant its authority names.
	rec := f.record(t, rows[2].did)
	entries := crew.EntriesOf(rec)
	for _, tc := range []struct {
		then ext.Principal
		want bool
	}{
		{ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}}, true},
		{ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantHost, ext.GrantDecide}}, true},
		{ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantHost}}, false},
		{ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantAdmin}}, false},
	} {
		who := admittee(f.srv.grantPrincipal(tc.then))
		who.Grants = slices.DeleteFunc(who.Grants, func(g string) bool { return g != string(ext.GrantDecide) })
		if got := crew.Admits(entries, who); got != tc.want {
			t.Errorf("Admits for %v: %v, want %v", tc.then.Grants, got, tc.want)
		}
		did := f.open(t, "v-"+string(tc.then.Grants[len(tc.then.Grants)-1])+fmt.Sprint(len(tc.then.Grants)))
		f.give(t, did)
		f.id.recheckAs(passAna, tc.then, false)
		if status, data, _ := f.respondAs(t, passAna, did, "w-"+did, 0, 2); status != http.StatusCreated {
			t.Fatalf("the answer: %d %s", status, data)
		}
		state := ledger.DecisionSuperseded
		if tc.want {
			state = ledger.DecisionApplied
		}
		f.decisionWhen(t, did, state, inState(state))
		f.id.mu.Lock()
		f.id.then[passAna] = nil
		f.id.mu.Unlock()
	}
}

// The list carries the open decisions given to the grant alone, at their
// revs, newest first, no cut, nothing else: not answered ones (by this
// subject and another), a cancelled one, open ones not given (the
// operator's, an HM1 person's), one taken back, one whose policy cannot
// be read; whatever else changes meanwhile carries no marker into it.
func TestDecideListCarriesOnlyAdmittedDecisions(t *testing.T) {
	f := newDecideFixture(t, nil)
	byAna, byBob, cancelled, back := f.open(t, "r1"), f.open(t, "r2"), f.open(t, "r3"), f.open(t, "r4")
	for _, did := range []string{byAna, byBob, cancelled, back} {
		f.give(t, did)
	}
	f.respondAs(t, passAna, byAna, "a1", 0, 2)
	f.respondAs(t, passBob, byBob, "b1", 0, 2)
	f.expect(t, http.MethodPost, "/api/decisions/"+cancelled+"/cancel", map[string]any{"commandId": "x1", "why": "marker-why-91aa", "expectedRev": 2}, http.StatusOK, "")
	f.takeBack(t, back)
	for _, did := range []string{byAna, byBob} {
		f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	}
	open, operators, persons := f.open(t, "r5"), f.open(t, "r6"), f.open(t, "r7")
	f.give(t, open)
	f.routeTo(t, persons, "p1", map[string]any{"kind": "person", "name": "zelda"}, http.StatusOK, "")
	f.expect(t, http.MethodPost, "/api/decisions/"+operators+"/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "marker-comment-2d4e", "expectedRev": 1}, http.StatusOK, "")
	seeded := f.seed(t, 2, seedRequest(grantFor))
	f.rawWrite(t, "UPDATE decisions SET policy = '{' WHERE n = ?", numberOf(seeded[0]))
	f.createSession("cat")
	want := []string{seeded[1], open}
	for _, pass := range []string{passAna, passHostAna} {
		data, l := f.list(t, pass)
		if !slices.Equal(ids(l), want) || l.Cut || l.Decisions[1].Rev != f.rev(t, open) || l.Decisions[0].Rev != 1 {
			t.Fatalf("%s's list: %s, want %v", pass, data, want)
		}
		for _, m := range []string{"marker-why-91aa", "marker-comment-2d4e", "zelda", "question", f.run, f.sid} {
			if bytes.Contains(data, []byte(m)) {
				t.Fatalf("the list holds %q: %s", m, data)
			}
		}
	}
}

// A list read held after its ledger read while the operator takes dr-1
// back lists it at the rev it judged; the next lists dr-2 alone and dr-1's
// read is 404; dr-2 answered by another holder leaves the list; a third
// given to the grant joins it at its new rev.
func TestDecideListTakeBackBetweenReads(t *testing.T) {
	f := newDecideFixture(t, nil)
	first, second, third := f.open(t, "r1"), f.open(t, "r2"), f.open(t, "r3")
	f.give(t, first)
	f.give(t, second)
	at, gate := f.holdAt(t, true)
	done := make(chan []byte, 1)
	go func() {
		_, data, _ := f.callAs(t, passAna, http.MethodGet, "/api/me/decisions", nil)
		done <- data
	}()
	reached(t, at, "the list")
	f.takeBack(t, first)
	close(gate)
	var held proto.DecideList
	strictly(t, <-done, &held)
	if !slices.Equal(ids(held), []string{second, first}) || held.Decisions[1].Rev != 2 {
		t.Fatalf("the held list: %+v", held)
	}
	f.srv.decideSeam.Store(nil)
	if _, l := f.list(t, passAna); !slices.Equal(ids(l), []string{second}) {
		t.Fatalf("the next list: %+v", l)
	}
	if status, _ := f.readAs(t, passAna, first); status != http.StatusNotFound {
		t.Fatalf("the read of %s: %d", first, status)
	}
	f.respondAs(t, passBob, second, "b1", 0, 2)
	f.decisionWhen(t, second, "applied", inState(ledger.DecisionApplied))
	f.give(t, third)
	if _, l := f.list(t, passAna); !slices.Equal(ids(l), []string{third}) || l.Decisions[0].Rev != 2 {
		t.Fatalf("the list after: %+v", l)
	}
}

// 200 open decisions given to the grant are all listed, no cut; a 201st
// lists the 200 newest, cut, the oldest absent; each reply within
// proto.MaxDecideList, every entry whole.
func TestDecideListBound(t *testing.T) {
	f := newDecideFixture(t, nil)
	seeded := f.seed(t, proto.MaxDecideListEntries, seedRequest(grantFor))
	data, l := f.list(t, passAna)
	if len(l.Decisions) != proto.MaxDecideListEntries || l.Cut || len(data) > proto.MaxDecideList || l.Decisions[0].ID != seeded[len(seeded)-1] {
		t.Fatalf("200: %d listed, cut %v, %d bytes", len(l.Decisions), l.Cut, len(data))
	}
	newest := f.seed(t, 1, seedRequest(grantFor))[0]
	data, l = f.list(t, passAna)
	if len(l.Decisions) != proto.MaxDecideListEntries || !l.Cut || len(data) > proto.MaxDecideList || l.Decisions[0].ID != newest || slices.Contains(ids(l), seeded[0]) {
		t.Fatalf("201: %d listed, cut %v, %d bytes", len(l.Decisions), l.Cut, len(data))
	}
}

// The limit and the cut count admitted rows only: refused rows below the
// read's bound leave the reply byte for byte as it was, an undecodable
// request among them (its read 404); 200 admitted beside refused rows are
// all listed, no cut, and so behind 201 refused newer ones; with the
// read's bound at ten, ten refused rows newer than an admitted one leave
// it out, cut.
func TestDecideListCountsAdmittedRowsOnly(t *testing.T) {
	f := newDecideFixture(t, nil)
	admitted := f.seed(t, 3, seedRequest(grantFor))
	before, _ := f.list(t, passAna)
	f.seed(t, 2, seedRequest(""))
	f.seed(t, 1, seedRequest(`{"label":"person:zelda","person":"zelda"}`))
	broken := f.seed(t, 1, seedRequest(grantFor))[0]
	f.rawWrite(t, "UPDATE decisions SET request = '{' WHERE n = ?", numberOf(broken))
	if after, _ := f.list(t, passAna); !bytes.Equal(before, after) {
		t.Fatalf("refused rows changed the reply:\n%s\n%s", before, after)
	}
	if status, _ := f.readAs(t, passAna, broken); status != http.StatusNotFound {
		t.Fatalf("the undecodable row's read: %d", status)
	}
	admitted = append(admitted, f.seed(t, proto.MaxDecideListEntries-len(admitted), seedRequest(grantFor))...)
	if _, l := f.list(t, passAna); len(l.Decisions) != proto.MaxDecideListEntries || l.Cut || l.Decisions[proto.MaxDecideListEntries-1].ID != admitted[0] {
		t.Fatalf("200 admitted beside refused rows: %d, cut %v", len(l.Decisions), l.Cut)
	}
	f.seed(t, proto.MaxDecideListEntries+1, seedRequest(""))
	if _, l := f.list(t, passAna); len(l.Decisions) != proto.MaxDecideListEntries || l.Cut || l.Decisions[0].ID != admitted[len(admitted)-1] {
		t.Fatalf("behind 201 refused rows: %d, cut %v", len(l.Decisions), l.Cut)
	}
	f.srv.decideSeam.Store(&decideSeams{scanLimit: 10})
	if _, l := f.list(t, passAna); len(l.Decisions) != 0 || !l.Cut {
		t.Fatalf("at a read's bound of ten: %+v", l)
	}
}

// A request given to the grant in text the decoder takes but SQL reads
// otherwise (for given twice, its members capitalised or escaped): the
// list holds it exactly when its read answers the decide view.
func TestDecideListAndReadAgree(t *testing.T) {
	f := newDecideFixture(t, nil)
	person := `{"label":"person:zelda","person":"zelda"}`
	base := `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}`
	variants := []string{
		base + `,"for":` + person + `,"for":` + grantFor + `}`,
		base + `,"for":` + grantFor + `,"for":` + person + `}`,
		base + `,"for":` + person + `,"for":{"label":"grant:decide"}}`,
		base + `,"For":{"Label":"grant:decide"}}`,
		base + `,"` + `\` + `u0066or":{"label":"grant:decide"}}`, // the key escaped
		base + `,"for":{"label":"grant:decide"}}`,
		base + `,"for":{"label":"grant:decide","person":"zelda"}}`,
	}
	dids := f.seed(t, len(variants), seedRequest(grantFor))
	for i, v := range variants {
		f.rawWrite(t, "UPDATE decisions SET request = ? WHERE n = ?", v, numberOf(dids[i]))
	}
	_, l := f.list(t, passAna)
	listedN := 0
	for i, did := range dids {
		status, _ := f.readAs(t, passAna, did)
		if slices.Contains(ids(l), did) != (status == http.StatusOK) || (status == http.StatusNotFound) == slices.Contains(ids(l), did) {
			t.Errorf("variant %d: listed %v, read %d", i, slices.Contains(ids(l), did), status)
		}
		if status == http.StatusOK {
			listedN++
		}
	}
	if listedN == 0 || listedN == len(variants) {
		t.Fatalf("%d of %d variants admitted: the cases do not tell the readers apart", listedN, len(variants))
	}
}

// The list refuses everyone but a decide principal: the admin (the
// workbench token, a provider's admin with decide) 403 not_decide, a
// host-only pass and no credential 401; on a switchyard, on the list, the
// read and the respond route, a host-only pass and no credential the 401
// they get on main, the admin and both decide principals 403 switchyard.
func TestDecideListRefusesOthers(t *testing.T) {
	f := newDecideFixture(t, nil)
	for _, tok := range []string{adminToken, passAdminDecide} {
		f.expectAs(t, tok, http.MethodGet, "/api/me/decisions", nil, http.StatusForbidden, "not_decide")
	}
	for _, tok := range []string{passHostOnly, "", passUnknown} {
		f.expectAs(t, tok, http.MethodGet, "/api/me/decisions", nil, http.StatusUnauthorized, "unauthorized")
	}
	_, a, _ := f.callAs(t, passAna, http.MethodGet, "/api/me/decisions", nil)
	_, h, _ := f.callAs(t, passHostAna, http.MethodGet, "/api/me/decisions", nil)
	if !bytes.Equal(a, h) {
		t.Fatalf("the decide passes' lists differ: %s %s", a, h)
	}
	y := newDecideFixture(t, func(c *config.Config) {
		c.Switchyard.Enabled = true
	})
	for _, path := range []string{"GET /api/me/decisions", "GET /api/decisions/dr-1", "POST /api/decisions/dr-1/respond"} {
		method, p, _ := strings.Cut(path, " ")
		var body any
		if method == http.MethodPost {
			body = []byte(`{}`)
		}
		for _, tok := range []string{passHostOnly, ""} {
			_, data, out := y.callAs(t, tok, method, p, body)
			if codeOf(out) != "unauthorized" || apiMessage(out) != "workbench token required" {
				t.Errorf("%s as %q on a switchyard: %s", path, tok, data)
			}
		}
		for _, tok := range []string{adminToken, passAna, passHostAna} {
			if status, data, out := y.callAs(t, tok, method, p, body); status != http.StatusForbidden || codeOf(out) != "switchyard" {
				t.Errorf("%s as %.12s on a switchyard: %d %s", path, tok, status, data)
			}
		}
	}
}

// Each decide reply is written whole within its bound, its newline
// counted, and refused one byte past it, never cut; a response's body is taken at its bound and refused one
// past it in the server's words; a pass is read from the header alone,
// never from a query.
func TestDecideBoundsAtTheRoutes(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	entry := proto.DecideEntry{ID: "dr-1", Rev: 1}
	one, _ := json.Marshal(proto.DecideList{Decisions: []proto.DecideEntry{entry}})
	whole := append(one, '\n')
	for _, tc := range []struct {
		max, status int
	}{{len(whole), http.StatusOK}, {len(whole) - 1, http.StatusInternalServerError}} {
		rec := httptest.NewRecorder()
		srv.writeBounded(rec, http.StatusOK, proto.DecideList{Decisions: []proto.DecideEntry{entry}}, tc.max)
		if rec.Code != tc.status || tc.status == http.StatusOK && (rec.Body.Len() != tc.max || !bytes.Equal(rec.Body.Bytes(), whole)) || tc.status != http.StatusOK && bytes.Contains(rec.Body.Bytes(), []byte("dr-1")) {
			t.Errorf("at %d bytes: %d %s", tc.max, rec.Code, rec.Body.Bytes())
		}
	}
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	body := func(n int) []byte {
		b := []byte(`{"responseId":"big","choice":0,"expectedRev":2,"rationale":""}`)
		pad := n - len(b)
		return append(b[:len(b)-2], append(bytes.Repeat([]byte("x"), pad), '"', '}')...)
	}
	if status, data, out := f.callAs(t, passAna, http.MethodPost, "/api/decisions/"+did+"/respond", body(maxDecisionRespondBody+1)); status != http.StatusBadRequest || apiMessage(out) != "the body is not a response" {
		t.Fatalf("one past the body's bound: %d %.200s", status, data)
	}
	if status, data, out := f.callAs(t, passAna, http.MethodPost, "/api/decisions/"+did+"/respond", body(maxDecisionRespondBody)); status == http.StatusBadRequest && apiMessage(out) == "the body is not a response" {
		t.Fatalf("a body at its bound: %d %.200s", status, data)
	}
	for _, path := range []string{"/api/me", "/api/me/decisions", "/api/decisions/" + did} {
		f.expectAs(t, "", http.MethodGet, path+"?token="+passAna, nil, http.StatusUnauthorized, "unauthorized")
	}
	if status, data, out := f.callAs(t, passAna, http.MethodGet, "/api/me/decisions?after=dr-1", nil); status != http.StatusBadRequest || apiMessage(out) != "this route takes no query" {
		t.Fatalf("a query: %d %s", status, data)
	}
}

// A holder's response carries five members alone (responseId, choice,
// text, rationale, expectedRev): a body with any other, a name among them
// in any spelling the decoder takes, is 400 invalid in the server's words,
// nothing written, the same whether the decision is given to the grant or
// not. Its answer is shown under the name its sign-in carries (the fake
// provider's "Ana"): in the operator's read and the receipt's words, the
// line the asking session is given and what a later responder is told.
// The same body again is a repeat, answered from its receipt.
func TestDecideAnswerIsShownUnderItsSignIn(t *testing.T) {
	f := newDecideFixture(t, nil)
	did, other := f.open(t, "r1"), f.open(t, "r2")
	f.give(t, did)
	rev := f.rev(t, did)
	for _, extra := range []string{
		`"byName":"the operator"`, `"ByName":"the operator"`, `"BYNAME":"the operator"`,
		`"byName":"Bob","byName":"the operator"`, `"` + `\` + `u0062yName":"the operator"`,
		`"actor":"user:nate"`, `"for":"zelda"`,
	} {
		body := []byte(fmt.Sprintf(`{"responseId":"a1","choice":0,"expectedRev":%d,%s}`, rev, extra))
		var given []byte
		for _, d := range []string{did, other} {
			status, data, out := f.callAs(t, passAna, http.MethodPost, "/api/decisions/"+d+"/respond", body)
			if status != http.StatusBadRequest || codeOf(out) != crew.CodeInvalid || apiMessage(out) != "the body is not a response" || given != nil && !bytes.Equal(data, given) {
				t.Errorf("%s to %s: %d %s", extra, d, status, data)
			}
			given = data
		}
	}
	for _, d := range []string{did, other} {
		if n := len(f.record(t, d).Responses); n != 0 {
			t.Errorf("%s holds %d responses after the refused bodies", d, n)
		}
	}
	status, sent, _ := f.respondAs(t, passAna, did, "a2", 0, rev)
	if status != http.StatusCreated {
		t.Errorf("the answer: %d %s", status, sent)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	_, data, _ := f.callAs(t, adminToken, http.MethodGet, "/api/decisions/"+did, nil)
	var read struct {
		Decision crew.Decision `json:"decision"`
	}
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatal(err)
	}
	for _, rc := range read.Decision.Responses {
		if rc.Status == "accepted" && (rc.ByName != "Ana" || rc.Actor != "user:ana" || rc.Words != "Recorded: "+did+" answered by Ana") {
			t.Errorf("the operator reads the answer by %q (%s): %q", rc.ByName, rc.Actor, rc.Words)
		}
	}
	d, _ := f.srv.registry.Get(f.sid)
	var line string
	waitUntil(t, "the answer's line in the asking session", func() bool {
		for _, en := range d.(*session.Local).Activity() {
			if strings.HasPrefix(en.Message, "Decision "+did+" ") {
				line = en.Message
				return true
			}
		}
		return false
	})
	if !strings.Contains(line, " answered by Ana: ") {
		t.Errorf("the asking session is given %q", line)
	}
	_, later, out := f.callAs(t, adminToken, http.MethodPost, "/api/decisions/"+did+"/respond", map[string]any{"responseId": "op1", "choice": 1, "expectedRev": f.rev(t, did)})
	if codeOf(out) != crew.CodeDecisionAnswered || !strings.HasPrefix(apiMessage(out), did+" was answered by Ana at ") {
		t.Errorf("a later responder is told: %s", later)
	}
	status, again, _ := f.respondAs(t, passAna, did, "a2", 0, rev)
	var r1, r2 proto.DecideReply
	_ = json.Unmarshal(sent, &r1)
	_ = json.Unmarshal(again, &r2)
	if status != http.StatusOK || r2.Receipt.K != r1.Receipt.K || r2.Receipt.Status != "accepted" {
		t.Errorf("the same body again: %d %s, first %s", status, again, sent)
	}
}

// One answer for every decision a holder is not admitted to: on the read
// and on the respond route, a decision never given to the grant, one given
// and taken back, one answered by another holder, one answered by the
// operator, one that does not exist, ids out of form and ids of other
// kinds of record are answered one status, one body and the same headers.
func TestDecideNotFoundIsOneAnswer(t *testing.T) {
	f := newDecideFixture(t, nil)
	notGiven, taken, byBob, byOperator := f.open(t, "r1"), f.open(t, "r2"), f.open(t, "r3"), f.open(t, "r4")
	f.give(t, taken)
	f.takeBack(t, taken)
	for _, did := range []string{byBob, byOperator} {
		f.give(t, did)
	}
	f.respond(t, passBob, byBob, "b1", 0, f.rev(t, byBob), http.StatusCreated, "")
	f.respond(t, adminToken, byOperator, "o1", 0, f.rev(t, byOperator), http.StatusCreated, "")
	call := func(method, path string, body []byte) string {
		t.Helper()
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, f.http.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+passAna)
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		h := resp.Header.Clone()
		h.Del("Date")
		hb, _ := json.Marshal(h)
		return fmt.Sprintf("%d %s %s", resp.StatusCode, hb, data)
	}
	cases := []string{notGiven, taken, byBob, byOperator, "dr-999999", "dr-0", "dr-01", "dr-1x", "dr-" + strings.Repeat("9", 25), "zz-1", "pl-1", "CND-1", f.run, f.sid}
	for _, route := range []string{"read", "respond"} {
		var first string
		for _, id := range cases {
			got := ""
			if route == "read" {
				got = call(http.MethodGet, "/api/decisions/"+id, nil)
			} else {
				got = call(http.MethodPost, "/api/decisions/"+id+"/respond", []byte(`{"responseId":"a9","choice":0,"expectedRev":5}`))
			}
			if first == "" {
				first = got
				if !strings.HasPrefix(got, "404 ") || !strings.Contains(got, `"no such decision"`) {
					t.Fatalf("%s %s: %s", route, id, got)
				}
			} else if got != first {
				t.Errorf("%s %s: %s, the first %s", route, id, got, first)
			}
		}
	}
}

// The four routes take decidePerMinute requests of one subject a minute,
// whichever of its passes sends them: the last taken, the next refused 429
// in the server's words on each route, nothing read or written; another
// subject's taken meanwhile; the admin's never under the bound; a minute
// later on the tests' clock the subject's next taken again.
func TestDecideRequestsAreBoundPerSubject(t *testing.T) {
	f := newDecideFixture(t, nil)
	did := f.open(t, "r1")
	f.give(t, did)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	rl := f.srv.decideLimiter
	rl.mu.Lock()
	rl.now = func() time.Time { return time.Unix(0, clock.Load()) }
	rl.mu.Unlock()
	type route struct {
		method, path string
		body         any
	}
	reads := []route{{http.MethodGet, "/api/me", nil}, {http.MethodGet, "/api/me/decisions", nil}, {http.MethodGet, "/api/decisions/" + did, nil}}
	for i := range decidePerMinute {
		r, pass := reads[i%len(reads)], []string{passAna, passHostAna}[i%2]
		if status, data, _ := f.callAs(t, pass, r.method, r.path, r.body); status != http.StatusOK {
			t.Fatalf("request %d, %s: %d %s", i+1, r.path, status, data)
		}
	}
	all := slices.Concat(reads, []route{{http.MethodPost, "/api/decisions/" + did + "/respond", map[string]any{"responseId": "a1", "choice": 0, "expectedRev": 2}}})
	for _, r := range all {
		for _, pass := range []string{passAna, passHostAna} {
			status, data, out := f.callAs(t, pass, r.method, r.path, r.body)
			if status != http.StatusTooManyRequests || codeOf(out) != "rate_limited" || apiMessage(out) != "too many requests of yours: ask again in a minute" || bytes.Contains(data, []byte(did)) {
				t.Errorf("past the bound, %s: %d %s", r.path, status, data)
			}
		}
	}
	if n := len(f.record(t, did).Responses); n != 0 {
		t.Fatalf("a refused send wrote %d responses", n)
	}
	f.list(t, passBob)
	f.view(t, passBob, did)
	for i := range decidePerMinute + 1 {
		tok := []string{passAdmin, passAdminDecide}[i%2]
		if status, data, _ := f.callAs(t, tok, http.MethodGet, []string{"/api/me", "/api/decisions/" + did}[i%2], nil); status != http.StatusOK {
			t.Fatalf("the admin's request %d: %d %.200s", i+1, status, data)
		}
	}
	clock.Add(int64(time.Minute))
	f.list(t, passAna)
	if status, data, _ := f.respondAs(t, passHostAna, did, "a1", 0, 2); status != http.StatusCreated {
		t.Fatalf("a minute later: %d %s", status, data)
	}
}
