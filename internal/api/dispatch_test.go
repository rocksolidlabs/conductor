package api

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The Dispatch routes (docs/plans/dispatch.md, PR-5 and PR-6), through
// the real store in a temp data directory. Each test has a server and a
// store of its own, and runs beside the others after the package's
// sequential tests: a store's every commit is synced to the disk, and the
// largest fixtures take hundreds of them.

// dispatchOps mints the tests' operation ids.
var dispatchOps atomic.Int64

// opID is a new operation id.
func opID() string { return fmt.Sprintf("op-%d", dispatchOps.Add(1)) }

// dispatchTemplate is the database file of a new Dispatch store, made
// once by the test binary, its directory gone at once.
var dispatchTemplate = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "dispatch-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ds, err := dispatch.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := ds.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, dispatch.File))
})

// seedDispatch puts a copy of a new Dispatch store where the server opens
// its own under dataDir, as a restart finds one. Making a store's schema
// is most of what opening a new one costs (a tenth of a second under the
// race detector), which some four hundred test environments would pay
// each; a server that finds the schema made only checks it.
func seedDispatch(t *testing.T, dataDir string) {
	t.Helper()
	b, err := dispatchTemplate()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "dispatch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dispatch.File), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// dispatchEnv is a test server whose Dispatch store is open for writing,
// made by the server in a data directory of the test's.
func dispatchEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, func(cfg *config.Config) { cfg.DataDir = filepath.Join(t.TempDir(), "data") })
	if e.srv.dispatch == nil {
		t.Fatal("no Dispatch store")
	}
	if ro, why := e.srv.dispatch.ReadOnly(); ro {
		t.Fatalf("Dispatch's store is read-only: %s", why)
	}
	return e
}

// encodeNoHTML is v as JSON with HTML escaping off, as the CLI encodes a
// body.
func encodeNoHTML(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// call sends body with the admin's token (JSON with HTML escaping off, or
// the bytes given as they are) and returns the reply's status, its bytes
// and the reply decoded.
func (e *testEnv) call(t *testing.T, method, path string, body any) (int, []byte, map[string]any) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		rd = bytes.NewReader(encodeNoHTML(t, body))
	}
	req, err := http.NewRequest(method, e.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%s %s: the reply is not JSON: %v: %.200s", method, path, err, data)
		}
	}
	return resp.StatusCode, data, out
}

// expect sends body as call does and fails the test unless the reply has
// the status and, for an error, the code; it returns the reply decoded.
func (e *testEnv) expect(t *testing.T, method, path string, body any, status int, code string) map[string]any {
	t.Helper()
	got, data, out := e.call(t, method, path, body)
	if got != status || (code != "" && errorCode(out) != code) {
		t.Fatalf("%s %s: %d %.300s, want %d %s", method, path, got, data, status, code)
	}
	return out
}

// entityOf is the entity a reply names.
func entityOf(t *testing.T, out map[string]any, name string) map[string]any {
	t.Helper()
	v, ok := out[name].(map[string]any)
	if !ok {
		t.Fatalf("no %s in %v", name, out)
	}
	return v
}

// newDispatchProject makes a project through the route, with the fields
// given beside its key and name, and returns it.
func (e *testEnv) newDispatchProject(t *testing.T, key string, fields map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"opId": opID(), "key": key, "name": key}
	maps.Copy(body, fields)
	return entityOf(t, e.expect(t, "POST", "/api/dispatch/projects", body, http.StatusCreated, ""), "project")
}

// newDispatchTicket makes a ticket of the project through the route, with
// the fields given beside its title, and returns it.
func (e *testEnv) newDispatchTicket(t *testing.T, project, title string, fields map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"opId": opID(), "project": project, "title": title}
	maps.Copy(body, fields)
	return entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets", body, http.StatusCreated, ""), "ticket")
}

// adminOp is an operation of the admin's, by the display name given, for
// a test that writes through the store.
func adminOp(by string) dispatch.Op { return dispatch.Op{Actor: adminActor, ID: opID(), By: by} }

// doneOf is a check that fails t unless an operation of the store's was
// done, and gives its receipt: done := doneOf(t); done(ds.AddPerson(…)).
func doneOf(t *testing.T) func(dispatch.Receipt, error) dispatch.Receipt {
	return func(rc dispatch.Receipt, err error) dispatch.Receipt {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if rc.Status != dispatch.StatusDone {
			t.Fatalf("refused: %+v", rc)
		}
		return rc
	}
}

// dispatchRequests is a request of every Dispatch route, by its pattern,
// as a client makes it: what the token and the switchyard tests send.
var dispatchRequests = map[string]struct {
	path string
	body any
}{
	"GET /api/dispatch/projects":                               {"/api/dispatch/projects", nil},
	"POST /api/dispatch/projects":                              {"/api/dispatch/projects", map[string]any{"opId": "a1", "key": "CND", "name": "Conductor"}},
	"GET /api/dispatch/projects/{key}":                         {"/api/dispatch/projects/CND", nil},
	"POST /api/dispatch/projects/{key}/edit":                   {"/api/dispatch/projects/CND/edit", map[string]any{"opId": "a2", "name": "x"}},
	"POST /api/dispatch/projects/{key}/archive":                {"/api/dispatch/projects/CND/archive", map[string]any{"opId": "a3"}},
	"POST /api/dispatch/projects/{key}/unarchive":              {"/api/dispatch/projects/CND/unarchive", map[string]any{"opId": "a4"}},
	"POST /api/dispatch/projects/{key}/labels":                 {"/api/dispatch/projects/CND/labels", map[string]any{"opId": "b1", "name": "api", "color": "blue"}},
	"POST /api/dispatch/projects/{key}/labels/{id}/edit":       {"/api/dispatch/projects/CND/labels/0123456789abcdef/edit", map[string]any{"opId": "b2", "name": "x"}},
	"POST /api/dispatch/projects/{key}/labels/{id}/delete":     {"/api/dispatch/projects/CND/labels/0123456789abcdef/delete", map[string]any{"opId": "b3"}},
	"POST /api/dispatch/projects/{key}/milestones":             {"/api/dispatch/projects/CND/milestones", map[string]any{"opId": "b4", "name": "1.0"}},
	"POST /api/dispatch/projects/{key}/milestones/{id}/edit":   {"/api/dispatch/projects/CND/milestones/0123456789abcdef/edit", map[string]any{"opId": "b5", "state": "closed"}},
	"POST /api/dispatch/projects/{key}/milestones/{id}/delete": {"/api/dispatch/projects/CND/milestones/0123456789abcdef/delete", map[string]any{"opId": "b6"}},
	"GET /api/dispatch/people":                                 {"/api/dispatch/people", nil},
	"POST /api/dispatch/people":                                {"/api/dispatch/people", map[string]any{"opId": "a5", "name": "Alice"}},
	"GET /api/dispatch/tickets":                                {"/api/dispatch/tickets", nil},
	"POST /api/dispatch/tickets":                               {"/api/dispatch/tickets", map[string]any{"opId": "a6", "project": "CND", "title": "t"}},
	"GET /api/dispatch/tickets/{key}":                          {"/api/dispatch/tickets/CND-1", nil},
	"POST /api/dispatch/tickets/{key}/edit":                    {"/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "a7", "set": map[string]any{"title": "x"}}},
	"POST /api/dispatch/tickets/{key}/accept":                  {"/api/dispatch/tickets/CND-1/accept", map[string]any{"opId": "a8"}},
	"POST /api/dispatch/tickets/{key}/unaccept":                {"/api/dispatch/tickets/CND-1/unaccept", map[string]any{"opId": "a9"}},
	"POST /api/dispatch/tickets/{key}/delete":                  {"/api/dispatch/tickets/CND-1/delete", map[string]any{"opId": "a10"}},
	"POST /api/dispatch/tickets/{key}/undelete":                {"/api/dispatch/tickets/CND-1/undelete", map[string]any{"opId": "a11"}},
	"GET /api/dispatch/tickets/{key}/history":                  {"/api/dispatch/tickets/CND-1/history", nil},
	"GET /api/dispatch/tickets/{key}/plans":                    {"/api/dispatch/tickets/CND-1/plans", nil},
	"POST /api/dispatch/tickets/{key}/plans":                   {"/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": "c1", "crew": "team"}},
	"GET /api/dispatch/plans/{id}":                             {"/api/dispatch/plans/0123456789abcdef", nil},
	"POST /api/dispatch/plans/{id}/approve":                    {"/api/dispatch/plans/0123456789abcdef/approve", map[string]any{"opId": "c2", "note": "ok"}},
	"POST /api/dispatch/plans/{id}/withdraw":                   {"/api/dispatch/plans/0123456789abcdef/withdraw", map[string]any{"opId": "c3"}},
	"GET /api/dispatch/tickets/{key}/comments":                 {"/api/dispatch/tickets/CND-1/comments", nil},
	"POST /api/dispatch/tickets/{key}/comments":                {"/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": "b7", "body": "x"}},
	"POST /api/dispatch/tickets/{key}/comments/{id}/edit":      {"/api/dispatch/tickets/CND-1/comments/0123456789abcdef/edit", map[string]any{"opId": "b8", "body": "y"}},
	"POST /api/dispatch/tickets/{key}/comments/{id}/delete":    {"/api/dispatch/tickets/CND-1/comments/0123456789abcdef/delete", map[string]any{"opId": "b9"}},
	"POST /api/dispatch/tickets/{key}/relations":               {"/api/dispatch/tickets/CND-1/relations", map[string]any{"opId": "b10", "kind": "relates", "to": "CND-2"}},
	"POST /api/dispatch/tickets/{key}/relations/{id}/delete":   {"/api/dispatch/tickets/CND-1/relations/0123456789abcdef/delete", map[string]any{"opId": "b11"}},
	"POST /api/dispatch/tickets/{key}/links":                   {"/api/dispatch/tickets/CND-1/links", map[string]any{"opId": "b12", "kind": "delivery", "url": "https://example.com/pull/1"}},
	"POST /api/dispatch/tickets/{key}/links/{id}/delete":       {"/api/dispatch/tickets/CND-1/links/0123456789abcdef/delete", map[string]any{"opId": "b13"}},
	// The upload's body is multipart (uploadOf, dispatch_attachments_test.go):
	// every walk here is answered before a body is read, but the bounds'
	// walk, which sends one of its shape.
	"POST /api/dispatch/tickets/{key}/attachments":             {"/api/dispatch/tickets/CND-1/attachments", nil},
	"POST /api/dispatch/tickets/{key}/attachments/{id}/delete": {"/api/dispatch/tickets/CND-1/attachments/0123456789abcdef/delete", map[string]any{"opId": "b17"}},
	"GET /api/dispatch/attachments/{id}":                       {"/api/dispatch/attachments/0123456789abcdef", nil},
	"GET /api/dispatch/links":                                  {"/api/dispatch/links?run=team-0000aaaa", nil},
	"GET /api/dispatch/views":                                  {"/api/dispatch/views?project=CND", nil},
	"POST /api/dispatch/views":                                 {"/api/dispatch/views", map[string]any{"opId": "b14", "name": "Mine", "kind": "list", "query": map[string]any{}}},
	"POST /api/dispatch/views/{id}/edit":                       {"/api/dispatch/views/0123456789abcdef/edit", map[string]any{"opId": "b15", "name": "x"}},
	"POST /api/dispatch/views/{id}/delete":                     {"/api/dispatch/views/0123456789abcdef/delete", map[string]any{"opId": "b16"}},
	"GET /api/dispatch/ops/{opId}":                             {"/api/dispatch/ops/a1", nil},
}

// eachDispatchRoute calls f with every Dispatch route the server has, by
// its method and a request of it, and fails the test for a route
// dispatchRequests has no request of, so a route added later is covered.
func eachDispatchRoute(t *testing.T, srv *Server, f func(method, path string, body any)) {
	t.Helper()
	routes := srv.dispatchRoutes()
	if len(routes) != len(dispatchRequests) {
		t.Fatalf("%d routes, %d requests", len(routes), len(dispatchRequests))
	}
	for _, r := range routes {
		req, ok := dispatchRequests[r.pattern]
		if !ok {
			t.Fatalf("no request of %s", r.pattern)
		}
		f(strings.Fields(r.pattern)[0], req.path, req.body)
	}
}

// Every Dispatch route is the admin's (Owner decisions, 17): no token, a
// wrong one, a host token and a share token, a controller's link
// included, are refused 401, and nothing is written.
func TestDispatchRoutesAreTheAdminsAlone(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	// Every refusal is the token's: the per-client limiter would answer
	// some with 429.
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	_, share, err := e.srv.links.Create("s1", session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	eachDispatchRoute(t, e.srv, func(method, path string, body any) {
		for _, token := range []string{"", "wrong", "test-host-token", share} {
			if resp, out := e.do(method, path, token, body); resp.StatusCode != http.StatusUnauthorized || errorCode(out) != "unauthorized" {
				t.Errorf("%s %s with token %.12q: %d %v", method, path, token, resp.StatusCode, out)
			}
		}
	})
	ctx := context.Background()
	if page, err := e.srv.dispatch.Projects(ctx, true, 0, ""); err != nil || len(page.Projects) != 0 {
		t.Fatalf("a refused call made a project: %+v %v", page, err)
	}
	if people, err := e.srv.dispatch.People(ctx); err != nil || len(people) != 0 {
		t.Fatalf("a refused call added a person: %v %v", people, err)
	}
}

// A switchyard keeps no tickets: every Dispatch route answers 403
// switchyard, and no store is opened in its data directory.
func TestDispatchOnASwitchyard(t *testing.T) {
	t.Parallel()
	e := switchyardEnv(t, true)
	eachDispatchRoute(t, e.srv, func(method, path string, body any) {
		if resp, out := e.do(method, path, adminToken, body); resp.StatusCode != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Errorf("%s %s: %d %v", method, path, resp.StatusCode, out)
		}
	})
	if e.srv.dispatch != nil {
		t.Fatal("a switchyard opened Dispatch's store")
	}
	if _, err := os.Stat(filepath.Join(e.srv.store.Dir(), "dispatch")); !os.IsNotExist(err) {
		t.Fatalf("a switchyard made Dispatch's directory: %v", err)
	}
}

// Without a data directory there is no store: every route answers 503
// store_unavailable, as the crews routes do. (With one, the environment's
// store, a copy of a new one, opens for writing as a store made there
// would.)
func TestDispatchWithoutADataDirectory(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	if e.srv.dispatch == nil {
		t.Fatal("no Dispatch store in a data directory")
	}
	if ro, why := e.srv.dispatch.ReadOnly(); ro {
		t.Fatalf("the environment's store is read-only: %s", why)
	}
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "Ada"}, http.StatusCreated, "")
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	none := e.serve(srv)
	eachDispatchRoute(t, srv, func(method, path string, body any) {
		if resp, out := none.do(method, path, adminToken, body); resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "store_unavailable" {
			t.Errorf("%s %s: %d %v", method, path, resp.StatusCode, out)
		}
	})
}

// A store that cannot be opened at all (its file a symbolic link) leaves
// the server running without it: every route answers 503
// dispatch_unavailable, unavailable rather than absent.
func TestDispatchStoreThatCannotOpen(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, func(cfg *config.Config) {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		dir := filepath.Join(cfg.DataDir, "dispatch")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "elsewhere.sqlite"), filepath.Join(dir, dispatch.File)); err != nil {
			t.Fatal(err)
		}
	})
	if e.srv.dispatch != nil {
		t.Fatal("Dispatch opened a store on a symbolic link")
	}
	eachDispatchRoute(t, e.srv, func(method, path string, body any) {
		if resp, out := e.do(method, path, adminToken, body); resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "dispatch_unavailable" {
			t.Errorf("%s %s: %d %v", method, path, resp.StatusCode, out)
		}
	})
	if resp, _ := e.do("GET", "/api/crews", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the rest of the API: %d", resp.StatusCode)
	}
}

// openRaw opens the store's database file beside the store, for a test
// that plants what no route makes yet.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A database a newer Conductor wrote opens read-only: every read answers
// as it can, and every write is 503 dispatch_unavailable with why, the
// store left as it was.
func TestDispatchReadOnlyStoreAnswersReadsAndRefusesWrites(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	data := filepath.Join(t.TempDir(), "data")
	dir := filepath.Join(data, "dispatch")
	ds, err := dispatch.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	done(ds.CreateProject(ctx, adminOp(""), dispatch.NewProject{Key: "CND", Name: "Kept"}))
	done(ds.CreateTicket(ctx, adminOp(""), dispatch.NewTicket{Project: "CND", Title: "Kept too"}))
	if err := ds.Close(); err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, filepath.Join(dir, dispatch.File))
	if _, err := db.Exec("UPDATE schema_version SET version = ?", dispatch.Version+1); err != nil {
		t.Fatal(err)
	}
	db.Close()

	e := newTestEnv(t, func(cfg *config.Config) { cfg.DataDir = data })
	if e.srv.dispatch == nil {
		t.Fatal("no store")
	}
	ro, why := e.srv.dispatch.ReadOnly()
	if !ro || !strings.Contains(why, "newer Conductor") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	out := e.expect(t, "GET", "/api/dispatch/projects", nil, http.StatusOK, "")
	if ps := out["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["name"] != "Kept" {
		t.Fatalf("projects: %v", out)
	}
	if tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket"); tk["title"] != "Kept too" {
		t.Fatalf("ticket: %v", tk)
	}
	if out := e.expect(t, "GET", "/api/dispatch/tickets?project=CND", nil, http.StatusOK, ""); out["total"] != 1.0 {
		t.Fatalf("list: %v", out)
	}
	eachDispatchRoute(t, e.srv, func(method, path string, body any) {
		if method != "POST" {
			return
		}
		resp, out := e.do(method, path, adminToken, body)
		if resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "dispatch_unavailable" ||
			!strings.Contains(out["error"].(map[string]any)["message"].(string), "newer Conductor") {
			t.Errorf("%s %s: %d %v", method, path, resp.StatusCode, out)
		}
	})
	if tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket"); tk["rev"] != 1.0 {
		t.Fatalf("a refused write changed the ticket: %v", tk)
	}
}

// The project routes: a create, its repeat and a conflicting one, a key
// taken, the read with what its pages offer, an edit with and without the
// revision expected, the archive and its refusals, the list with and
// without the archived ones and its counts, and the bodies the store
// would not look at.
func TestDispatchProjectRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	create := map[string]any{"opId": "p1", "byName": "Nate", "key": "CND", "name": "Conductor", "description": "The <app> & more"}
	status, raw, out := e.call(t, "POST", "/api/dispatch/projects", create)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	rc, p := receiptOf(t, out), entityOf(t, out, "project")
	if rc["status"] != "done" || rc["intent"] != "project.create" || rc["key"] != "CND" || rc["id"] != p["id"] || rc["repeat"] != nil {
		t.Fatalf("receipt: %v", rc)
	}
	if p["key"] != "CND" || p["description"] != "The <app> & more" || !slices.Equal(p["closeRequires"].([]any), []any{"acceptance"}) || p["rev"] != rc["rev"] {
		t.Fatalf("project: %v", p)
	}
	// Replies are written with HTML escaping off.
	if !bytes.Contains(raw, []byte(`"The <app> & more"`)) {
		t.Fatalf("the reply escapes HTML: %s", raw)
	}
	// The same again answers the receipt, and makes nothing.
	out = e.expect(t, "POST", "/api/dispatch/projects", create, http.StatusOK, "")
	if again := receiptOf(t, out); again["repeat"] != true || again["id"] != rc["id"] {
		t.Fatalf("repeat: %v", again)
	}
	// The same id with other content is a conflict.
	conflict := maps.Clone(create)
	conflict["name"] = "Other"
	if rc := receiptOf(t, e.expect(t, "POST", "/api/dispatch/projects", conflict, http.StatusConflict, "op_conflict")); rc["status"] != "failed" {
		t.Fatalf("conflict: %v", rc)
	}
	// Another operation with the key: key_taken, kept as a failed receipt.
	taken := maps.Clone(create)
	taken["opId"] = "p2"
	if rc := receiptOf(t, e.expect(t, "POST", "/api/dispatch/projects", taken, http.StatusConflict, "key_taken")); rc["refusal"] != "key_taken" {
		t.Fatalf("key taken: %v", rc)
	}
	e.expect(t, "POST", "/api/dispatch/projects", taken, http.StatusConflict, "key_taken")
	// A key the store refuses, and a field past its bound.
	e.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": opID(), "key": "cnd", "name": "x"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": opID(), "key": "LONG", "name": strings.Repeat("n", dispatch.MaxProjectName+1)}, http.StatusConflict, "bound")
	// What the store would not look at.
	for _, body := range []any{
		map[string]any{"opId": opID(), "key": "BAD", "name": "x", "bogus": true},
		map[string]any{"opId": "an id with spaces", "key": "BAD", "name": "x"},
		map[string]any{"opId": "", "key": "BAD", "name": "x"},
		map[string]any{"opId": opID(), "expectedRev": -1, "key": "BAD", "name": "x"},
		map[string]any{"opId": opID(), "byName": "a\x01b", "key": "BAD", "name": "x"},
		[]byte(`{"opId":"x1","key":"BAD","name":"x"} {}`),
		[]byte(``),
	} {
		e.expect(t, "POST", "/api/dispatch/projects", body, http.StatusBadRequest, "invalid_request")
	}

	// The project's read, with what its pages offer.
	out = e.expect(t, "GET", "/api/dispatch/projects/CND", nil, http.StatusOK, "")
	if entityOf(t, out, "project")["name"] != "Conductor" || len(out["labels"].([]any)) != 0 || len(out["milestones"].([]any)) != 0 || len(out["views"].([]any)) != 0 {
		t.Fatalf("read: %v", out)
	}
	e.expect(t, "GET", "/api/dispatch/projects/NOPE", nil, http.StatusNotFound, "not_found")
	e.expect(t, "GET", "/api/dispatch/projects/cnd", nil, http.StatusBadRequest, "invalid_request")

	// An edit, at the revision it expects; one behind it is refused.
	rev := p["rev"]
	out = e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "expectedRev": rev, "name": "Conductor 2"}, http.StatusOK, "")
	if p2 := entityOf(t, out, "project"); p2["name"] != "Conductor 2" || p2["rev"] != rev.(float64)+1 || receiptOf(t, out)["rev"] != p2["rev"] {
		t.Fatalf("edit: %v", out)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "expectedRev": rev, "name": "Lost"}, http.StatusConflict, "stale_revision")
	e.expect(t, "POST", "/api/dispatch/projects/NOPE/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "closeRequires": []string{"verified"}}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/C/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusBadRequest, "invalid_request")

	// Archived: every write under it refused, archived again refused.
	if a := entityOf(t, e.expect(t, "POST", "/api/dispatch/projects/CND/archive", map[string]any{"opId": opID()}, http.StatusOK, ""), "project"); a["archivedAt"] == nil {
		t.Fatalf("archive: %v", a)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/archive", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusConflict, "archived")
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "CND", "title": "x"}, http.StatusConflict, "archived")

	// The list leaves the archived out unless asked, and counts each
	// project's open and done tickets.
	e.newDispatchProject(t, "OPEN", map[string]any{"closeRequires": []string{}})
	for _, st := range []string{"backlog", "in_progress", "done", "cancelled"} {
		e.newDispatchTicket(t, "OPEN", st, map[string]any{"status": st})
	}
	out = e.expect(t, "GET", "/api/dispatch/projects", nil, http.StatusOK, "")
	if ps := out["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["key"] != "OPEN" || out["next"] != nil {
		t.Fatalf("list: %v", out)
	}
	open := out["projects"].([]any)[0].(map[string]any)
	if open["open"] != 2.0 || open["done"] != 1.0 || open["tracker"] != "native" {
		t.Fatalf("counts: %v", open)
	}
	for _, field := range []string{"description", "cwd", "trackerUrl", "closeRequires"} {
		if _, has := open[field]; has {
			t.Fatalf("a summary carries its %s: %v", field, open)
		}
	}
	out = e.expect(t, "GET", "/api/dispatch/projects?archived=1", nil, http.StatusOK, "")
	if ps := out["projects"].([]any); len(ps) != 2 || ps[0].(map[string]any)["key"] != "CND" || ps[0].(map[string]any)["archivedAt"] == nil {
		t.Fatalf("list with the archived: %v", out)
	}
	for _, q := range []string{"?limit=201", "?limit=-1", "?limit=x", "?cursor=nope", "?archived=maybe", "?bogus=1", "?limit=1&limit=2"} {
		e.expect(t, "GET", "/api/dispatch/projects"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	// A page of one goes on from its cursor.
	out = e.expect(t, "GET", "/api/dispatch/projects?archived=1&limit=1", nil, http.StatusOK, "")
	next, _ := out["next"].(string)
	if next == "" {
		t.Fatalf("no next: %v", out)
	}
	out = e.expect(t, "GET", "/api/dispatch/projects?archived=true&limit=1&cursor="+next, nil, http.StatusOK, "")
	if ps := out["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["key"] != "OPEN" || out["next"] != nil {
		t.Fatalf("second page: %v", out)
	}

	// Unarchived, it is written again.
	if u := entityOf(t, e.expect(t, "POST", "/api/dispatch/projects/CND/unarchive", map[string]any{"opId": opID()}, http.StatusOK, ""), "project"); u["archivedAt"] != nil {
		t.Fatalf("unarchive: %v", u)
	}
	e.expect(t, "POST", "/api/dispatch/projects/CND/unarchive", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/CND/edit", map[string]any{"opId": opID(), "name": "Conductor"}, http.StatusOK, "")
}

// The people: the directory read by name, a name added, and one held
// already refused.
func TestDispatchPeopleRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	if out := e.expect(t, "GET", "/api/dispatch/people", nil, http.StatusOK, ""); len(out["people"].([]any)) != 0 {
		t.Fatalf("people: %v", out)
	}
	out := e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": "n1", "name": "Bob"}, http.StatusCreated, "")
	if receiptOf(t, out)["id"] != "Bob" || entityOf(t, out, "person")["name"] != "Bob" {
		t.Fatalf("add: %v", out)
	}
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": "n1", "name": "Bob"}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "Bob"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": ""}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/people", map[string]any{"opId": opID(), "name": "Alice"}, http.StatusCreated, "")
	out = e.expect(t, "GET", "/api/dispatch/people", nil, http.StatusOK, "")
	var names []string
	for _, p := range out["people"].([]any) {
		names = append(names, p.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"Alice", "Bob"}) {
		t.Fatalf("people: %v", out)
	}
}

// The ticket routes: a create with its key and close state, the read, an
// edit with and without the revision, the close policy's refusal naming
// what is missing, acceptance and its refusal without an owner, a parent
// cycle, a delete and what a tombstone answers, its undelete, the history
// and the comments.
func TestDispatchTicketRoutes(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	e.newDispatchProject(t, "CND", nil)
	status, raw, out := e.call(t, "POST", "/api/dispatch/tickets", map[string]any{
		"opId": "t1", "byName": "Nate", "project": "CND", "title": "Ship <it>", "description": "Body", "acceptance": "It ships", "priority": 2,
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	rc, tk := receiptOf(t, out), entityOf(t, out, "ticket")
	if rc["key"] != "CND-1" || rc["id"] != tk["id"] || tk["key"] != "CND-1" || tk["title"] != "Ship <it>" || tk["createdBy"] != "Nate" || tk["priority"] != 2.0 {
		t.Fatalf("create: %v", out)
	}
	if !bytes.Contains(raw, []byte(`"Ship <it>"`)) {
		t.Fatalf("the reply escapes HTML: %.300s", raw)
	}
	closeOf := func(tk map[string]any) map[string]any { return tk["close"].(map[string]any) }
	if c := closeOf(tk); !slices.Equal(c["requires"].([]any), []any{"acceptance"}) || !slices.Equal(c["missing"].([]any), []any{"acceptance"}) {
		t.Fatalf("close: %v", c)
	}
	// The read answers the same ticket.
	if got := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket"); got["rev"] != tk["rev"] || got["description"] != "Body" {
		t.Fatalf("read: %v", got)
	}
	e.expect(t, "GET", "/api/dispatch/tickets/CND-99", nil, http.StatusNotFound, "not_found")
	for _, key := range []string{"cnd-1", "CND-0", "CND-01", "CND"} {
		e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusBadRequest, "invalid_request")
	}
	// A create naming what is not there, or not a project's key.
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "NOPE", "title": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "CND", "title": "x", "parent": "CND-9"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "nope", "title": "x"}, http.StatusBadRequest, "invalid_request")
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "CND", "title": "x", "status": "shipped"}, http.StatusConflict, "invalid")

	// An edit of several fields at once, at the revision expected.
	edit := func(key string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/edit", body, status, code)
	}
	rev := tk["rev"]
	out = edit("CND-1", map[string]any{"expectedRev": rev, "set": map[string]any{"title": "Ship it", "priority": 3}, "add": map[string]any{"assignees": []string{"person:Ada"}}}, http.StatusOK, "")
	tk = entityOf(t, out, "ticket")
	if tk["title"] != "Ship it" || tk["priority"] != 3.0 || !slices.Equal(tk["assignees"].([]any), []any{"person:Ada"}) || tk["rev"] != rev.(float64)+1 {
		t.Fatalf("edit: %v", tk)
	}
	edit("CND-1", map[string]any{"expectedRev": rev, "set": map[string]any{"title": "Lost"}}, http.StatusConflict, "stale_revision")
	edit("CND-1", map[string]any{"set": map[string]any{"title": "x"}, "bogus": 1}, http.StatusBadRequest, "invalid_request")
	// Done is refused until the policy is met, the receipt naming what it lacks.
	out = edit("CND-1", map[string]any{"set": map[string]any{"status": "done"}}, http.StatusConflict, "close_policy")
	if rc := receiptOf(t, out); !slices.Equal(rc["missing"].([]any), []any{"acceptance"}) {
		t.Fatalf("close_policy: %v", rc)
	}
	// Acceptance is the owner's: refused without one.
	e.expect(t, "POST", "/api/dispatch/tickets/CND-1/accept", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")
	edit("CND-1", map[string]any{"set": map[string]any{"owner": "Alice"}}, http.StatusOK, "")
	out = e.expect(t, "POST", "/api/dispatch/tickets/CND-1/accept", map[string]any{"opId": opID(), "byName": "Nate", "note": "Looks <right>"}, http.StatusOK, "")
	tk = entityOf(t, out, "ticket")
	if tk["acceptedFor"] != "Alice" || tk["acceptedBy"] != "Nate" || tk["acceptedNote"] != "Looks <right>" || closeOf(tk)["met"].(map[string]any)["acceptance"] != true {
		t.Fatalf("accept: %v", tk)
	}
	if tk = entityOf(t, edit("CND-1", map[string]any{"set": map[string]any{"status": "done"}}, http.StatusOK, ""), "ticket"); tk["status"] != "done" || tk["closedAt"] == nil {
		t.Fatalf("done: %v", tk)
	}
	if tk = entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CND-1/unaccept", map[string]any{"opId": opID()}, http.StatusOK, ""), "ticket"); tk["acceptedAt"] != nil || tk["status"] != "done" {
		t.Fatalf("unaccept: %v", tk)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/CND-1/unaccept", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")

	// A parent that would make a cycle.
	e.newDispatchTicket(t, "CND", "Child", map[string]any{"parent": "CND-1"})
	edit("CND-1", map[string]any{"set": map[string]any{"parent": "CND-2"}}, http.StatusConflict, "cycle")

	// A delete: a tombstone, read as such, listed only when asked, and
	// every write on it but its undelete not_found.
	if tk = entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CND-2/delete", map[string]any{"opId": opID()}, http.StatusOK, ""), "ticket"); tk["deletedAt"] == nil {
		t.Fatalf("delete: %v", tk)
	}
	if tk = entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CND-2", nil, http.StatusOK, ""), "ticket"); tk["deletedAt"] == nil || tk["key"] != "CND-2" {
		t.Fatalf("a tombstone's read: %v", tk)
	}
	if rc := receiptOf(t, edit("CND-2", map[string]any{"set": map[string]any{"title": "x"}}, http.StatusNotFound, "not_found")); rc["status"] != "failed" {
		t.Fatalf("edit of a tombstone: %v", rc)
	}
	for _, route := range []string{"accept", "unaccept", "delete"} {
		e.expect(t, "POST", "/api/dispatch/tickets/CND-2/"+route, map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	}
	keysOf := func(out map[string]any) []string {
		var keys []string
		for _, tk := range out["tickets"].([]any) {
			keys = append(keys, tk.(map[string]any)["key"].(string))
		}
		slices.Sort(keys)
		return keys
	}
	if keys := keysOf(e.expect(t, "GET", "/api/dispatch/tickets?project=CND", nil, http.StatusOK, "")); !slices.Equal(keys, []string{"CND-1"}) {
		t.Fatalf("list: %v", keys)
	}
	if keys := keysOf(e.expect(t, "GET", "/api/dispatch/tickets?project=CND&deleted=1", nil, http.StatusOK, "")); !slices.Equal(keys, []string{"CND-1", "CND-2"}) {
		t.Fatalf("list with the tombstones: %v", keys)
	}
	if tk = entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CND-2/undelete", map[string]any{"opId": opID()}, http.StatusOK, ""), "ticket"); tk["deletedAt"] != nil || tk["parent"].(map[string]any)["key"] != "CND-1" {
		t.Fatalf("undelete: %v", tk)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/CND-2/undelete", map[string]any{"opId": opID()}, http.StatusConflict, "invalid")

	// The history: the fields as the store recorded them, paged.
	out = e.expect(t, "GET", "/api/dispatch/tickets/CND-1/history", nil, http.StatusOK, "")
	changes := out["changes"].([]any)
	var fields []string
	for _, c := range changes {
		if f, _ := c.(map[string]any)["field"].(string); f != "" && !slices.Contains(fields, f) {
			fields = append(fields, f)
		}
	}
	for _, want := range []string{"title", "description", "acceptance", "priority", "owner", "assignees", "status", "accepted_at", "closed_at"} {
		if !slices.Contains(fields, want) {
			t.Fatalf("the history has no %s: %v", want, fields)
		}
	}
	first := changes[0].(map[string]any)
	if first["op"] != "create" || first["actor"] != adminActor || first["by"] != "Nate" || first["opId"] != "t1" {
		t.Fatalf("the first change: %v", first)
	}
	out = e.expect(t, "GET", "/api/dispatch/tickets/CND-1/history?limit=2", nil, http.StatusOK, "")
	if len(out["changes"].([]any)) != 2 || out["next"] == nil {
		t.Fatalf("a page of two: %v", out)
	}
	after := fmt.Sprint(int64(out["next"].(float64)))
	out = e.expect(t, "GET", "/api/dispatch/tickets/CND-1/history?after="+after, nil, http.StatusOK, "")
	if len(out["changes"].([]any)) != len(changes)-2 {
		t.Fatalf("the rest: %d of %d", len(out["changes"].([]any)), len(changes))
	}
	for _, q := range []string{"?limit=501", "?limit=-1", "?after=-1", "?after=x", "?page=2"} {
		e.expect(t, "GET", "/api/dispatch/tickets/CND-1/history"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	e.expect(t, "GET", "/api/dispatch/tickets/CND-9/history", nil, http.StatusNotFound, "not_found")

	// The comments beyond the ticket's page (their add is the next
	// change's route: here, the store's).
	for i := range 3 {
		done(e.srv.dispatch.AddComment(ctx, adminOp("Nate"), "CND-1", fmt.Sprintf("Comment %d", i+1)))
	}
	out = e.expect(t, "GET", "/api/dispatch/tickets/CND-1/comments?after=1&limit=1", nil, http.StatusOK, "")
	if cs := out["comments"].([]any); len(cs) != 1 || cs[0].(map[string]any)["n"] != 2.0 || cs[0].(map[string]any)["body"] != "Comment 2" {
		t.Fatalf("comments: %v", out)
	}
	if cs := e.expect(t, "GET", "/api/dispatch/tickets/CND-1/comments", nil, http.StatusOK, "")["comments"].([]any); len(cs) != 3 {
		t.Fatalf("comments: %v", cs)
	}
	for _, q := range []string{fmt.Sprintf("?limit=%d", maxCommentPage+1), "?limit=-1", "?after=-1", "?limit=x"} {
		e.expect(t, "GET", "/api/dispatch/tickets/CND-1/comments"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	e.expect(t, "GET", "/api/dispatch/tickets/CND-9/comments", nil, http.StatusNotFound, "not_found")
}

// listOf is the entities a reply's list holds, each decoded.
func listOf(t *testing.T, out map[string]any, name string) []map[string]any {
	t.Helper()
	items, ok := out[name].([]any)
	if !ok {
		t.Fatalf("no list %s in %v", name, out)
	}
	var list []map[string]any
	for _, item := range items {
		list = append(list, item.(map[string]any))
	}
	return list
}

// The comment routes: a comment posted with its number, its author the
// admin by the display name, its repeat and a conflicting one; a second
// numbered after it; an edit at the comment's revision and one behind
// it; a delete leaving a tombstone in its place; the ticket's read and
// history showing each; and what each refuses.
func TestDispatchCommentRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "CMR", nil)
	tk := e.newDispatchTicket(t, "CMR", "Talked about", nil)
	e.newDispatchTicket(t, "CMR", "Elsewhere", nil)
	// The expected revision of an add is the ticket's.
	add := map[string]any{"opId": "c1", "expectedRev": tk["rev"], "byName": "Nate", "body": "First, <b> & more"}
	status, raw, out := e.call(t, "POST", "/api/dispatch/tickets/CMR-1/comments", add)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, raw)
	}
	rc, c := receiptOf(t, out), entityOf(t, out, "comment")
	if rc["intent"] != "comment.add" || rc["id"] != c["id"] || rc["rev"] != 1.0 || c["n"] != 1.0 || c["author"] != adminActor || c["by"] != "Nate" ||
		c["body"] != "First, <b> & more" || c["ticketId"] != tk["id"] || c["rev"] != 1.0 || c["editedAt"] != nil || c["deletedAt"] != nil {
		t.Fatalf("add: %v", out)
	}
	if !bytes.Contains(raw, []byte(`"First, <b> & more"`)) {
		t.Fatalf("the reply escapes HTML: %s", raw)
	}
	first := c["id"].(string)
	// The same again answers its receipt and adds nothing; other content
	// under its id is a conflict.
	if out := e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", add, http.StatusOK, ""); receiptOf(t, out)["repeat"] != true || entityOf(t, out, "comment")["id"] != first {
		t.Fatalf("repeat: %v", out)
	}
	conflict := maps.Clone(add)
	conflict["body"] = "Other"
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", conflict, http.StatusConflict, "op_conflict")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "expectedRev": 9, "body": "Late"}, http.StatusConflict, "stale_revision")
	second := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "body": "Second"}, http.StatusCreated, ""), "comment")
	if second["n"] != 2.0 || second["by"] != "" {
		t.Fatalf("the second comment: %v", second)
	}

	// An edit at the comment's revision; one behind it is refused, and so
	// is the body it holds already.
	edit := func(key, id string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/comments/"+id+"/edit", body, status, code)
	}
	c = entityOf(t, edit("CMR-1", first, map[string]any{"expectedRev": 1, "byName": "Nate", "body": "First, edited"}, http.StatusOK, ""), "comment")
	if c["body"] != "First, edited" || c["editedAt"] == nil || c["rev"] != 2.0 || c["n"] != 1.0 {
		t.Fatalf("edit: %v", c)
	}
	edit("CMR-1", first, map[string]any{"expectedRev": 1, "body": "Lost"}, http.StatusConflict, "stale_revision")
	edit("CMR-1", first, map[string]any{"body": "First, edited"}, http.StatusConflict, "invalid")
	edit("CMR-1", first, map[string]any{"body": ""}, http.StatusConflict, "invalid")
	edit("CMR-1", first, map[string]any{"body": strings.Repeat("b", dispatch.MaxCommentBody+1)}, http.StatusConflict, "bound")
	// Under another ticket's key the comment is not found.
	edit("CMR-2", first, map[string]any{"body": "Moved"}, http.StatusNotFound, "not_found")

	// A delete leaves a tombstone in its place, which no write reaches.
	c = entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments/"+first+"/delete", map[string]any{"opId": opID(), "expectedRev": 2}, http.StatusOK, ""), "comment")
	if c["deletedAt"] == nil || c["n"] != 1.0 || c["rev"] != 3.0 || c["body"] != "First, edited" {
		t.Fatalf("delete: %v", c)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments/"+first+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	edit("CMR-1", first, map[string]any{"body": "Revived"}, http.StatusNotFound, "not_found")
	// The ticket's read: both comments in their places, the first a
	// tombstone, numbered as before.
	cs := listOf(t, entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CMR-1", nil, http.StatusOK, ""), "ticket"), "comments")
	if len(cs) != 2 || cs[0]["id"] != first || cs[0]["n"] != 1.0 || cs[0]["deletedAt"] == nil ||
		cs[1]["id"] != second["id"] || cs[1]["n"] != 2.0 || cs[1]["deletedAt"] != nil {
		t.Fatalf("the ticket's comments: %v", cs)
	}
	// The ticket's history holds the comment's body as it was set, then
	// as it was edited, and its delete.
	var bodies []string
	deleted := false
	for _, ch := range listOf(t, e.expect(t, "GET", "/api/dispatch/tickets/CMR-1/history", nil, http.StatusOK, ""), "changes") {
		if ch["entity"] != "comment" || ch["entityId"] != first {
			continue
		}
		switch ch["field"] {
		case "body":
			bodies = append(bodies, ch["value"].(string))
		case "deleted_at":
			deleted = true
		}
	}
	if !slices.Equal(bodies, []string{"First, <b> & more", "First, edited"}) || !deleted {
		t.Fatalf("the comment's history: %v, deleted %v", bodies, deleted)
	}

	// What each refuses: a ticket missing or deleted, a body the store
	// refuses, a key, an id or a body it would not look at.
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-9/comments", map[string]any{"opId": opID(), "body": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "body": ""}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "body": "a\x01b"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "body": strings.Repeat("b", dispatch.MaxCommentBody+1)}, http.StatusConflict, "bound")
	e.expect(t, "POST", "/api/dispatch/tickets/cmr-1/comments", map[string]any{"opId": opID(), "body": "x"}, http.StatusBadRequest, "invalid_request")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments", map[string]any{"opId": opID(), "body": "x", "author": "agent:x"}, http.StatusBadRequest, "invalid_request")
	for _, id := range []string{"nope", "0123456789ABCDEF", first + "0"} {
		edit("CMR-1", id, map[string]any{"body": "x"}, http.StatusBadRequest, "invalid_request")
		e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments/"+id+"/delete", map[string]any{"opId": opID()}, http.StatusBadRequest, "invalid_request")
	}
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-1/comments/0123456789abcdef/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/CMR-2/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	if rc := receiptOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CMR-2/comments", map[string]any{"opId": opID(), "body": "x"}, http.StatusNotFound, "not_found")); rc["status"] != "failed" {
		t.Fatalf("a comment on a deleted ticket: %v", rc)
	}
}

// The label and milestone routes: each made, edited at its revision and
// refused one behind it, and deleted to a tombstone that leaves every
// ticket that had it, a deleted one among them, each ticket's revision
// bumped and one change row on each; a label's name free again after
// it; and what each refuses.
func TestDispatchLabelAndMilestoneRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "LBL", nil)
	e.newDispatchProject(t, "OTH", nil)
	status, raw, out := e.call(t, "POST", "/api/dispatch/projects/LBL/labels", map[string]any{"opId": "l1", "byName": "Nate", "name": "api <v1>", "color": "blue"})
	if status != http.StatusCreated {
		t.Fatalf("a label's create: %d %s", status, raw)
	}
	rc, lb := receiptOf(t, out), entityOf(t, out, "label")
	if rc["intent"] != "label.create" || rc["id"] != lb["id"] || lb["name"] != "api <v1>" || lb["color"] != "blue" || lb["rev"] != 1.0 || lb["deletedAt"] != nil {
		t.Fatalf("a label's create: %v", out)
	}
	label := lb["id"].(string)
	labelPath := "/api/dispatch/projects/LBL/labels/" + label
	// A name a live label holds, whatever its case; a colour none of the
	// twelve; a name past its bound.
	e.expect(t, "POST", "/api/dispatch/projects/LBL/labels", map[string]any{"opId": opID(), "name": "API <V1>", "color": "red"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/LBL/labels", map[string]any{"opId": opID(), "name": "web", "color": "#ff0000"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/LBL/labels", map[string]any{"opId": opID(), "name": strings.Repeat("n", dispatch.MaxLabelName+1), "color": "red"}, http.StatusConflict, "bound")
	// An edit at its revision; one behind, one that changes nothing.
	lb = entityOf(t, e.expect(t, "POST", labelPath+"/edit", map[string]any{"opId": opID(), "expectedRev": 1, "name": "api", "color": "teal"}, http.StatusOK, ""), "label")
	if lb["name"] != "api" || lb["color"] != "teal" || lb["rev"] != 2.0 {
		t.Fatalf("a label's edit: %v", lb)
	}
	e.expect(t, "POST", labelPath+"/edit", map[string]any{"opId": opID(), "expectedRev": 1, "name": "lost"}, http.StatusConflict, "stale_revision")
	e.expect(t, "POST", labelPath+"/edit", map[string]any{"opId": opID(), "name": "api"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/OTH/labels/"+label+"/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusNotFound, "not_found")

	// Two tickets have the label, the second of them deleted, and a third
	// has not: the delete leaves the two, and the third is untouched.
	e.newDispatchTicket(t, "LBL", "One", map[string]any{"labels": []string{label}})
	e.newDispatchTicket(t, "LBL", "Two", map[string]any{"labels": []string{label}})
	e.newDispatchTicket(t, "LBL", "Three", nil)
	e.expect(t, "POST", "/api/dispatch/tickets/LBL-2/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	read := func(key string) map[string]any {
		t.Helper()
		return entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")
	}
	revs := map[string]any{}
	for _, key := range []string{"LBL-1", "LBL-2", "LBL-3"} {
		revs[key] = read(key)["rev"]
	}
	lb = entityOf(t, e.expect(t, "POST", labelPath+"/delete", map[string]any{"opId": opID(), "expectedRev": 2}, http.StatusOK, ""), "label")
	if lb["deletedAt"] == nil || lb["rev"] != 3.0 || lb["name"] != "api" {
		t.Fatalf("a label's delete: %v", lb)
	}
	for _, key := range []string{"LBL-1", "LBL-2"} {
		if tk := read(key); len(tk["labels"].([]any)) != 0 || tk["rev"] != revs[key].(float64)+1 {
			t.Fatalf("%s after the label's delete: %v", key, tk)
		}
		removed := 0
		for _, ch := range listOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key+"/history", nil, http.StatusOK, ""), "changes") {
			if ch["field"] == "labels" && ch["op"] == "remove" && ch["value"] == label {
				removed++
			}
		}
		if removed != 1 {
			t.Fatalf("%s's history holds %d removes of the label", key, removed)
		}
	}
	if tk := read("LBL-3"); tk["rev"] != revs["LBL-3"] {
		t.Fatalf("a ticket without the label moved: %v", tk)
	}
	// Its name is free; the tombstone takes no write.
	again := entityOf(t, e.expect(t, "POST", "/api/dispatch/projects/LBL/labels", map[string]any{"opId": opID(), "name": "api", "color": "gray"}, http.StatusCreated, ""), "label")
	e.expect(t, "POST", labelPath+"/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", labelPath+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")

	// A milestone: made, edited, and deleted off every ticket naming it.
	status, raw, out = e.call(t, "POST", "/api/dispatch/projects/LBL/milestones", map[string]any{"opId": "m1", "name": "1.0", "description": "The first <cut>", "target": "2026-12-31"})
	if status != http.StatusCreated {
		t.Fatalf("a milestone's create: %d %s", status, raw)
	}
	rc, ms := receiptOf(t, out), entityOf(t, out, "milestone")
	if rc["intent"] != "milestone.create" || rc["id"] != ms["id"] || ms["name"] != "1.0" || ms["description"] != "The first <cut>" || ms["target"] != "2026-12-31" ||
		ms["state"] != "open" || ms["rev"] != 1.0 {
		t.Fatalf("a milestone's create: %v", out)
	}
	milestone := ms["id"].(string)
	milestonePath := "/api/dispatch/projects/LBL/milestones/" + milestone
	for _, body := range []map[string]any{{"name": "x", "target": "31/12/2026"}, {"name": "x", "state": "done"}, {"name": ""}} {
		body["opId"] = opID()
		e.expect(t, "POST", "/api/dispatch/projects/LBL/milestones", body, http.StatusConflict, "invalid")
	}
	e.expect(t, "POST", "/api/dispatch/projects/LBL/milestones", map[string]any{"opId": opID(), "name": "x", "description": strings.Repeat("d", dispatch.MaxMilestoneDescription+1)}, http.StatusConflict, "bound")
	ms = entityOf(t, e.expect(t, "POST", milestonePath+"/edit", map[string]any{"opId": opID(), "expectedRev": 1, "state": "closed", "target": ""}, http.StatusOK, ""), "milestone")
	if ms["state"] != "closed" || ms["target"] != "" || ms["name"] != "1.0" || ms["rev"] != 2.0 {
		t.Fatalf("a milestone's edit: %v", ms)
	}
	e.expect(t, "POST", milestonePath+"/edit", map[string]any{"opId": opID(), "expectedRev": 1, "name": "lost"}, http.StatusConflict, "stale_revision")
	e.expect(t, "POST", milestonePath+"/edit", map[string]any{"opId": opID(), "state": "closed"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/projects/OTH/milestones/"+milestone+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/LBL-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"milestone": milestone}}, http.StatusOK, "")
	e.newDispatchTicket(t, "LBL", "Four", map[string]any{"milestone": milestone})
	for _, key := range []string{"LBL-1", "LBL-3", "LBL-4"} {
		revs[key] = read(key)["rev"]
	}
	ms = entityOf(t, e.expect(t, "POST", milestonePath+"/delete", map[string]any{"opId": opID()}, http.StatusOK, ""), "milestone")
	if ms["deletedAt"] == nil || ms["rev"] != 3.0 {
		t.Fatalf("a milestone's delete: %v", ms)
	}
	for _, key := range []string{"LBL-1", "LBL-4"} {
		if tk := read(key); tk["milestoneId"] != "" || tk["rev"] != revs[key].(float64)+1 {
			t.Fatalf("%s after the milestone's delete: %v", key, tk)
		}
	}
	if tk := read("LBL-3"); tk["rev"] != revs["LBL-3"] {
		t.Fatalf("a ticket without the milestone moved: %v", tk)
	}
	e.expect(t, "POST", milestonePath+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", milestonePath+"/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusNotFound, "not_found")
	// The project's read offers the live ones alone.
	out = e.expect(t, "GET", "/api/dispatch/projects/LBL", nil, http.StatusOK, "")
	if ls := listOf(t, out, "labels"); len(ls) != 1 || ls[0]["id"] != again["id"] || len(listOf(t, out, "milestones")) != 0 {
		t.Fatalf("the project's read: %v", out)
	}

	// What both refuse: a project missing or archived, a key, an id or a
	// body the store would not look at.
	for _, kind := range []string{"labels", "milestones"} {
		body := func() map[string]any { return map[string]any{"opId": opID(), "name": "x", "color": "red"} }
		if kind == "milestones" {
			body = func() map[string]any { return map[string]any{"opId": opID(), "name": "x"} }
		}
		e.expect(t, "POST", "/api/dispatch/projects/NOPE/"+kind, body(), http.StatusNotFound, "not_found")
		e.expect(t, "POST", "/api/dispatch/projects/lbl/"+kind, body(), http.StatusBadRequest, "invalid_request")
		bogus := body()
		bogus["bogus"] = true
		e.expect(t, "POST", "/api/dispatch/projects/LBL/"+kind, bogus, http.StatusBadRequest, "invalid_request")
		for _, route := range []string{"edit", "delete"} {
			e.expect(t, "POST", "/api/dispatch/projects/LBL/"+kind+"/xyz/"+route, map[string]any{"opId": opID()}, http.StatusBadRequest, "invalid_request")
		}
	}
	e.expect(t, "POST", "/api/dispatch/projects/OTH/archive", map[string]any{"opId": opID()}, http.StatusOK, "")
	e.expect(t, "POST", "/api/dispatch/projects/OTH/labels", map[string]any{"opId": opID(), "name": "x", "color": "red"}, http.StatusConflict, "archived")
	e.expect(t, "POST", "/api/dispatch/projects/OTH/milestones", map[string]any{"opId": opID(), "name": "x"}, http.StatusConflict, "archived")
}

// The relation routes: a blocks added, answered with the ticket and the
// relation among its own, the other ticket blocked by it; the same again,
// a cycle, the ticket itself, another project's ticket, one missing and a
// kind that is none refused; a removal through the other end's key
// answered with that ticket, no longer blocked; and a relation added
// again after its removal a new one.
func TestDispatchRelationRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "REL", nil)
	e.newDispatchProject(t, "OTR", nil)
	a := e.newDispatchTicket(t, "REL", "A", nil)
	b := e.newDispatchTicket(t, "REL", "B", nil)
	e.newDispatchTicket(t, "REL", "C", nil)
	e.newDispatchTicket(t, "OTR", "Elsewhere", nil)
	relate := func(key string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/relations", body, status, code)
	}
	read := func(key string) map[string]any {
		t.Helper()
		return entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket")
	}
	out := relate("REL-1", map[string]any{"expectedRev": a["rev"], "byName": "Nate", "kind": "blocks", "to": "REL-2"}, http.StatusCreated, "")
	rc, tk := receiptOf(t, out), entityOf(t, out, "ticket")
	rels := listOf(t, tk, "relations")
	if rc["intent"] != "relation.add" || tk["key"] != "REL-1" || tk["rev"] != a["rev"].(float64)+1 || rc["rev"] != tk["rev"] || len(rels) != 1 ||
		rels[0]["id"] != rc["id"] || rels[0]["kind"] != "blocks" || rels[0]["fromId"] != a["id"] || rels[0]["toId"] != b["id"] ||
		rels[0]["createdBy"] != "Nate" || rels[0]["other"].(map[string]any)["key"] != "REL-2" {
		t.Fatalf("a relation's add: %v", out)
	}
	relation := rc["id"].(string)
	if other := read("REL-2"); other["blocked"] != true || other["rev"] != b["rev"].(float64)+1 || len(other["relations"].([]any)) != 1 {
		t.Fatalf("the blocked end: %v", other)
	}
	relate("REL-1", map[string]any{"expectedRev": a["rev"], "kind": "relates", "to": "REL-3"}, http.StatusConflict, "stale_revision")
	relate("REL-1", map[string]any{"kind": "blocks", "to": "REL-2"}, http.StatusConflict, "invalid")
	relate("REL-2", map[string]any{"kind": "blocks", "to": "REL-3"}, http.StatusCreated, "")
	relate("REL-3", map[string]any{"kind": "blocks", "to": "REL-1"}, http.StatusConflict, "cycle")
	relate("REL-1", map[string]any{"kind": "blocks", "to": "REL-1"}, http.StatusConflict, "cycle")
	relate("REL-1", map[string]any{"kind": "relates", "to": "REL-1"}, http.StatusConflict, "invalid")
	relate("REL-1", map[string]any{"kind": "relates", "to": "OTR-1"}, http.StatusConflict, "invalid")
	relate("REL-1", map[string]any{"kind": "relates", "to": "REL-9"}, http.StatusNotFound, "not_found")
	relate("REL-1", map[string]any{"kind": "depends", "to": "REL-3"}, http.StatusConflict, "invalid")
	// A relates reads the same from either end: its reverse is held.
	relate("REL-3", map[string]any{"kind": "relates", "to": "REL-1"}, http.StatusCreated, "")
	relate("REL-1", map[string]any{"kind": "relates", "to": "REL-3"}, http.StatusConflict, "invalid")

	// Removed through the other end's key: the reply is that ticket, no
	// longer blocked.
	out = e.expect(t, "POST", "/api/dispatch/tickets/REL-2/relations/"+relation+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	if rc, tk = receiptOf(t, out), entityOf(t, out, "ticket"); rc["intent"] != "relation.remove" || tk["key"] != "REL-2" || tk["blocked"] != false || rc["rev"] != tk["rev"] {
		t.Fatalf("a relation's removal: %v", out)
	}
	for _, r := range listOf(t, tk, "relations") {
		if r["id"] == relation {
			t.Fatalf("the relation removed is still REL-2's: %v", r)
		}
	}
	if rels := listOf(t, read("REL-1"), "relations"); len(rels) != 1 || rels[0]["kind"] != "relates" {
		t.Fatalf("REL-1 after the removal: %v", rels)
	}
	// Removed again, through a ticket that is neither end, an id that is
	// not one.
	e.expect(t, "POST", "/api/dispatch/tickets/REL-1/relations/"+relation+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	bc := listOf(t, read("REL-2"), "relations")[0]["id"].(string)
	e.expect(t, "POST", "/api/dispatch/tickets/REL-1/relations/"+bc+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/REL-1/relations/nope/delete", map[string]any{"opId": opID()}, http.StatusBadRequest, "invalid_request")
	// Added again after its removal, it is a new relation.
	if again := receiptOf(t, relate("REL-1", map[string]any{"kind": "blocks", "to": "REL-2"}, http.StatusCreated, "")); again["id"] == relation {
		t.Fatalf("a relation added again kept its id: %v", again)
	}
	// A deleted ticket takes no relation, at either end.
	e.expect(t, "POST", "/api/dispatch/tickets/REL-3/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	relate("REL-1", map[string]any{"kind": "relates", "to": "REL-3"}, http.StatusNotFound, "not_found")
	relate("REL-3", map[string]any{"kind": "relates", "to": "REL-2"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/REL-1/relations", map[string]any{"opId": opID(), "kind": "relates", "to": "REL-2", "from": "REL-1"}, http.StatusBadRequest, "invalid_request")
}

// The link routes: a delivery link without a ref takes its URL as one and
// meets a delivery close policy; the same identity again refused, another
// URL another link; a run link to a run nobody has refused not_found, to
// a recorded run linked, and to the same run from a second ticket refused
// run_linked with the first named in the receipt; a link removed, removed
// again refused, and added again a new one.
func TestDispatchLinkRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	run := crew.Run{ID: "linked-0000beef", CrewID: "linked", Name: "Linked", StartedAt: time.Now().UTC().Add(-time.Hour), State: crew.RunFinished}
	if err := e.srv.records.Save(run); err != nil {
		t.Fatal(err)
	}
	e.newDispatchProject(t, "LNK", map[string]any{"closeRequires": []string{"delivery"}})
	a := e.newDispatchTicket(t, "LNK", "Shipped", nil)
	e.newDispatchTicket(t, "LNK", "Also", nil)
	link := func(key string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/tickets/"+key+"/links", body, status, code)
	}
	linkOf := func(tk map[string]any, id string) map[string]any {
		t.Helper()
		for _, l := range listOf(t, tk, "links") {
			if l["id"] == id {
				return l
			}
		}
		return nil
	}
	pr := "https://github.com/rocksolidlabs/conductor/pull/1"
	out := link("LNK-1", map[string]any{"expectedRev": a["rev"], "byName": "Nate", "kind": "delivery", "url": pr, "note": "Merged <today>"}, http.StatusCreated, "")
	rc, tk := receiptOf(t, out), entityOf(t, out, "ticket")
	delivery := rc["id"].(string)
	l := linkOf(tk, delivery)
	if rc["intent"] != "link.add" || rc["rev"] != tk["rev"] || tk["rev"] != a["rev"].(float64)+1 || l == nil || l["kind"] != "delivery" || l["ref"] != pr ||
		l["url"] != pr || l["note"] != "Merged <today>" || l["createdBy"] != "Nate" || tk["close"].(map[string]any)["met"].(map[string]any)["delivery"] != true {
		t.Fatalf("a delivery link's add: %v", out)
	}
	// Its identity, the kind and the ref, is taken; another URL is another
	// link.
	link("LNK-1", map[string]any{"kind": "delivery", "url": pr}, http.StatusConflict, "invalid")
	link("LNK-1", map[string]any{"kind": "delivery", "ref": pr, "url": "https://example.com/other"}, http.StatusConflict, "invalid")
	link("LNK-1", map[string]any{"kind": "delivery", "url": pr + "/files"}, http.StatusCreated, "")
	link("LNK-1", map[string]any{"expectedRev": a["rev"], "kind": "delivery", "url": pr + "/commits"}, http.StatusConflict, "stale_revision")
	// A delivery or an external link names its URL, http or https; a run
	// link names its run; no other kind is one.
	for _, body := range []map[string]any{
		{"kind": "delivery"}, {"kind": "external", "ref": "ABC-1"}, {"kind": "external", "url": "ftp://example.com/x"},
		{"kind": "run"}, {"kind": "plan", "url": pr},
	} {
		link("LNK-1", body, http.StatusConflict, "invalid")
	}
	out = link("LNK-1", map[string]any{"kind": "external", "ref": "ABC-1", "url": "https://tracker.example.com/ABC-1"}, http.StatusCreated, "")
	if l := linkOf(entityOf(t, out, "ticket"), receiptOf(t, out)["id"].(string)); l == nil || l["ref"] != "ABC-1" {
		t.Fatalf("an external link with its ref: %v", out)
	}
	// A run link names a run the engine holds or has a record of, and a
	// run belongs to one ticket.
	if rc := receiptOf(t, link("LNK-1", map[string]any{"kind": "run", "ref": "nobody-0000dead"}, http.StatusNotFound, "not_found")); rc["status"] != "failed" || rc["refusal"] != "not_found" {
		t.Fatalf("a run nobody has: %v", rc)
	}
	runLink := receiptOf(t, link("LNK-1", map[string]any{"kind": "run", "ref": run.ID}, http.StatusCreated, ""))["id"].(string)
	if rc := receiptOf(t, link("LNK-2", map[string]any{"kind": "run", "ref": run.ID}, http.StatusConflict, "run_linked")); rc["key"] != "LNK-1" || rc["status"] != "failed" {
		t.Fatalf("a run linked to another ticket: %v", rc)
	}
	if tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/links?run="+run.ID, nil, http.StatusOK, ""), "ticket"); tk["key"] != "LNK-1" {
		t.Fatalf("the run's ticket: %v", tk)
	}

	// Removed: gone from the ticket; removed again, through another
	// ticket's key, an id that is not one, refused.
	out = e.expect(t, "POST", "/api/dispatch/tickets/LNK-1/links/"+delivery+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	if rc, tk := receiptOf(t, out), entityOf(t, out, "ticket"); rc["intent"] != "link.remove" || tk["key"] != "LNK-1" || linkOf(tk, delivery) != nil {
		t.Fatalf("a link's removal: %v", out)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/LNK-1/links/"+delivery+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/LNK-2/links/"+runLink+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/LNK-1/links/nope/delete", map[string]any{"opId": opID()}, http.StatusBadRequest, "invalid_request")
	// Added again after its removal, a link is a new one; a run unlinked
	// is free for another ticket.
	if again := receiptOf(t, link("LNK-1", map[string]any{"kind": "delivery", "url": pr}, http.StatusCreated, "")); again["id"] == delivery {
		t.Fatalf("a link added again kept its id: %v", again)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/LNK-1/links/"+runLink+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	link("LNK-2", map[string]any{"kind": "run", "ref": run.ID}, http.StatusCreated, "")
	link("LNK-1", map[string]any{"kind": "external", "url": pr, "title": "x"}, http.StatusBadRequest, "invalid_request")
	link("LNK-9", map[string]any{"kind": "external", "url": pr}, http.StatusNotFound, "not_found")
}

// The view routes: a view of a project made with its query the JSON it
// is, kept compacted, and one of every project; the list, every view or a
// project's own and every project's; an edit at its revision and one
// behind it; a query that is not a list's or past 8 KiB refused, one of
// spaces past 8 KiB compacted under it taken; a delete to a tombstone no
// longer listed; a view of an archived project read and not written; and
// what the routes refuse.
func TestDispatchViewRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	ctx := context.Background()
	vw := e.newDispatchProject(t, "VW", nil)
	e.newDispatchProject(t, "OTH", nil)
	if views := listOf(t, e.expect(t, "GET", "/api/dispatch/views", nil, http.StatusOK, ""), "views"); len(views) != 0 {
		t.Fatalf("views: %v", views)
	}
	body := []byte(`{"opId":"v1","byName":"Nate","project":"VW","name":"Mine <now>","kind":"board",
		"query": { "status": ["in_progress", "in_review"], "sort": "priority", "desc": true, "group": "owner" }}`)
	status, raw, out := e.call(t, "POST", "/api/dispatch/views", body)
	if status != http.StatusCreated {
		t.Fatalf("a view's create: %d %s", status, raw)
	}
	rc, v := receiptOf(t, out), entityOf(t, out, "view")
	query, _ := v["query"].(map[string]any)
	if rc["intent"] != "view.create" || rc["id"] != v["id"] || v["projectId"] != vw["id"] || v["name"] != "Mine <now>" || v["kind"] != "board" || v["rev"] != 1.0 ||
		query == nil || !slices.Equal(query["status"].([]any), []any{"in_progress", "in_review"}) || query["sort"] != "priority" || query["desc"] != true || query["group"] != "owner" {
		t.Fatalf("a view's create: %v", out)
	}
	mine := v["id"].(string)
	// Kept as the JSON it is, compacted.
	if kept, err := e.srv.dispatch.View(ctx, mine); err != nil || string(kept.Query) != `{"status":["in_progress","in_review"],"sort":"priority","desc":true,"group":"owner"}` {
		t.Fatalf("the query kept: %s %v", kept.Query, err)
	}
	create := func(body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/views", body, status, code)
	}
	if every := entityOf(t, create(map[string]any{"name": "Everything", "kind": "list", "query": map[string]any{}}, http.StatusCreated, ""), "view"); every["projectId"] != "" {
		t.Fatalf("a view of every project: %v", every)
	}
	theirs := entityOf(t, create(map[string]any{"project": "OTH", "name": "Theirs", "kind": "roadmap", "query": map[string]any{"horizon": []string{"now"}}}, http.StatusCreated, ""), "view")["id"].(string)
	names := func(query string) []string {
		t.Helper()
		var names []string
		for _, v := range listOf(t, e.expect(t, "GET", "/api/dispatch/views"+query, nil, http.StatusOK, ""), "views") {
			names = append(names, v["name"].(string))
		}
		return names
	}
	for query, want := range map[string][]string{
		"": {"Everything", "Mine <now>", "Theirs"}, "?project=": {"Everything", "Mine <now>", "Theirs"},
		"?project=VW": {"Everything", "Mine <now>"}, "?project=OTH": {"Everything", "Theirs"},
	} {
		if got := names(query); !slices.Equal(got, want) {
			t.Errorf("views%s: %v, want %v", query, got, want)
		}
	}

	// An edit at its revision; one behind, one that changes nothing.
	edit := func(id string, body map[string]any, status int, code string) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", "/api/dispatch/views/"+id+"/edit", body, status, code)
	}
	v = entityOf(t, edit(mine, map[string]any{"expectedRev": 1, "name": "Mine", "query": map[string]any{"status": []string{"done"}}}, http.StatusOK, ""), "view")
	if q, _ := v["query"].(map[string]any); v["name"] != "Mine" || v["kind"] != "board" || v["rev"] != 2.0 || q == nil || !slices.Equal(q["status"].([]any), []any{"done"}) || len(q) != 1 {
		t.Fatalf("a view's edit: %v", v)
	}
	edit(mine, map[string]any{"expectedRev": 1, "name": "Lost"}, http.StatusConflict, "stale_revision")
	edit(mine, map[string]any{"name": "Mine"}, http.StatusConflict, "invalid")
	// A query that is not a list's: a string of one, null, a field it does
	// not have, a value outside its vocabulary; a kind that is none.
	for _, bad := range []any{`{"status":["done"]}`, nil, map[string]any{"colour": "red"}, map[string]any{"status": []string{"shipped"}}, []any{}} {
		edit(mine, map[string]any{"query": bad}, http.StatusConflict, "invalid")
		create(map[string]any{"name": "Bad", "kind": "list", "query": bad}, http.StatusConflict, "invalid")
	}
	edit(mine, map[string]any{"kind": "table"}, http.StatusConflict, "invalid")
	create(map[string]any{"name": "No query", "kind": "list"}, http.StatusConflict, "invalid")
	// A query past 8 KiB as kept is refused bound; one of spaces past it,
	// compacted under it, is taken.
	long := []byte(`{"opId":"` + opID() + `","name":"Long","kind":"list","query":{"q":"` + strings.Repeat("w", dispatch.MaxViewQuery) + `"}}`)
	e.expect(t, "POST", "/api/dispatch/views", long, http.StatusConflict, "bound")
	e.expect(t, "POST", "/api/dispatch/views/"+mine+"/edit", []byte(`{"opId":"`+opID()+`","query":{"q":"`+strings.Repeat("w", dispatch.MaxViewQuery)+`"}}`), http.StatusConflict, "bound")
	spaced := []byte(`{"opId":"` + opID() + `","name":"Spaced","kind":"list","query":{"sort":"key"` + strings.Repeat(" ", dispatch.MaxViewQuery) + `}}`)
	if v := entityOf(t, e.expect(t, "POST", "/api/dispatch/views", spaced, http.StatusCreated, ""), "view"); v["query"].(map[string]any)["sort"] != "key" {
		t.Fatalf("a query of spaces: %v", v)
	}

	// A delete: a tombstone, listed no more, which takes no write.
	if v = entityOf(t, e.expect(t, "POST", "/api/dispatch/views/"+mine+"/delete", map[string]any{"opId": opID(), "expectedRev": 2}, http.StatusOK, ""), "view"); v["deletedAt"] == nil || v["rev"] != 3.0 {
		t.Fatalf("a view's delete: %v", v)
	}
	if got := names("?project=VW"); !slices.Equal(got, []string{"Everything", "Spaced"}) {
		t.Fatalf("the views after the delete: %v", got)
	}
	e.expect(t, "POST", "/api/dispatch/views/"+mine+"/delete", map[string]any{"opId": opID()}, http.StatusNotFound, "not_found")
	edit(mine, map[string]any{"name": "Back"}, http.StatusNotFound, "not_found")
	// A view of an archived project is read, never written.
	e.expect(t, "POST", "/api/dispatch/projects/OTH/archive", map[string]any{"opId": opID()}, http.StatusOK, "")
	edit(theirs, map[string]any{"name": "Theirs, archived"}, http.StatusConflict, "archived")
	e.expect(t, "POST", "/api/dispatch/views/"+theirs+"/delete", map[string]any{"opId": opID()}, http.StatusConflict, "archived")
	create(map[string]any{"project": "OTH", "name": "Late", "kind": "list", "query": map[string]any{}}, http.StatusConflict, "archived")
	if got := names("?project=OTH"); !slices.Equal(got, []string{"Everything", "Spaced", "Theirs"}) {
		t.Fatalf("an archived project's views: %v", got)
	}

	// What the routes refuse: a project missing or not a key, an id that
	// is not one, a body or a parameter they do not take.
	create(map[string]any{"project": "NOPE", "name": "x", "kind": "list", "query": map[string]any{}}, http.StatusNotFound, "not_found")
	create(map[string]any{"project": "vw", "name": "x", "kind": "list", "query": map[string]any{}}, http.StatusBadRequest, "invalid_request")
	create(map[string]any{"name": "x", "kind": "list", "query": map[string]any{}, "bogus": 1}, http.StatusBadRequest, "invalid_request")
	edit("nope", map[string]any{"name": "x"}, http.StatusBadRequest, "invalid_request")
	edit("0123456789abcdef", map[string]any{"name": "x"}, http.StatusNotFound, "not_found")
	edit(mine, map[string]any{"project": "VW"}, http.StatusBadRequest, "invalid_request")
	e.expect(t, "GET", "/api/dispatch/views?project=NOPE", nil, http.StatusNotFound, "not_found")
	for _, q := range []string{"?project=vw", "?project=VW&project=OTH", "?bogus=1"} {
		e.expect(t, "GET", "/api/dispatch/views"+q, nil, http.StatusBadRequest, "invalid_request")
	}
}

// The list filters and sorts on the server, pages by its cursor and says
// how many it holds; a value outside its vocabulary or bound is 400.
func TestDispatchTicketListFiltersAndRefusesBadValues(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	e.newDispatchProject(t, "LST", map[string]any{"closeRequires": []string{}})
	label := done(e.srv.dispatch.CreateLabel(ctx, adminOp(""), "LST", dispatch.NewLabel{Name: "api", Color: "blue"})).ID
	milestone := done(e.srv.dispatch.CreateMilestone(ctx, adminOp(""), "LST", dispatch.NewMilestone{Name: "1.0"})).ID
	e.newDispatchTicket(t, "LST", "Alpha search words", map[string]any{"priority": 1, "owner": "Alice", "horizon": "now"})
	e.newDispatchTicket(t, "LST", "Beta", map[string]any{"status": "in_progress", "priority": 3, "assignees": []string{"person:Bob"}, "labels": []string{label}, "horizon": "next", "milestone": milestone})
	e.newDispatchTicket(t, "LST", "Gamma", map[string]any{"status": "done", "parent": "LST-1"})
	done(e.srv.dispatch.AddRelation(ctx, adminOp(""), "LST-1", "blocks", "LST-2"))
	e.newDispatchProject(t, "OTH", nil)
	e.newDispatchTicket(t, "OTH", "Elsewhere", nil)
	keys := func(query string) []string {
		t.Helper()
		out := e.expect(t, "GET", "/api/dispatch/tickets"+query, nil, http.StatusOK, "")
		var keys []string
		for _, tk := range out["tickets"].([]any) {
			keys = append(keys, tk.(map[string]any)["key"].(string))
		}
		if int(out["total"].(float64)) < len(keys) {
			t.Fatalf("%s: a total of %v for %d", query, out["total"], len(keys))
		}
		return keys
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"?project=LST&sort=key", []string{"LST-1", "LST-2", "LST-3"}},
		{"?sort=key", []string{"LST-1", "LST-2", "LST-3", "OTH-1"}},
		{"?project=LST&status=backlog&status=done&sort=key", []string{"LST-1", "LST-3"}},
		{"?project=LST&priority=3", []string{"LST-2"}},
		{"?project=LST&priority=0&priority=1&sort=key", []string{"LST-1", "LST-3"}},
		{"?project=LST&owner=Alice", []string{"LST-1"}},
		{"?project=LST&assignee=person:Bob", []string{"LST-2"}},
		{"?project=LST&label=" + label, []string{"LST-2"}},
		{"?project=LST&milestone=" + milestone, []string{"LST-2"}},
		{"?project=LST&horizon=now&horizon=next&sort=key", []string{"LST-1", "LST-2"}},
		{"?project=LST&horizon=", []string{"LST-3"}},
		{"?parent=LST-1", []string{"LST-3"}},
		{"?project=LST&blocked=1", []string{"LST-2"}},
		{"?project=LST&q=sea", []string{"LST-1"}},
		{"?project=LST&q=alpha+words", []string{"LST-1"}},
		{"?project=LST&sort=priority&desc=1", []string{"LST-2", "LST-1", "LST-3"}},
		{"?project=LST&status=cancelled", nil},
	} {
		if got := keys(tc.query); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.query, got, tc.want)
		}
	}
	// A page goes on from its cursor, and the total is the whole list's.
	out := e.expect(t, "GET", "/api/dispatch/tickets?project=LST&sort=key&limit=2", nil, http.StatusOK, "")
	if len(out["tickets"].([]any)) != 2 || out["total"] != 3.0 {
		t.Fatalf("a page of two: %v", out)
	}
	if got := keys("?project=LST&sort=key&limit=2&cursor=" + out["next"].(string)); !slices.Equal(got, []string{"LST-3"}) {
		t.Fatalf("the next page: %v", got)
	}
	// A summary carries no description.
	if _, has := out["tickets"].([]any)[0].(map[string]any)["description"]; has {
		t.Fatalf("a summary with its description: %v", out)
	}
	for _, q := range []string{
		"?status=shipped", "?priority=x", "?priority=9", "?label=api", "?milestone=1.0", "?horizon=soon", "?parent=LST", "?project=lst",
		"?assignee=Bob", "?sort=random", "?blocked=maybe", "?desc=2", "?limit=201", "?limit=x", "?cursor=nope",
		"?project=LST&project=OTH", "?q=" + strings.Repeat("w", dispatch.MaxQueryText+1), "?bogus=1", "?q=%zz",
	} {
		e.expect(t, "GET", "/api/dispatch/tickets"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	// A cursor of another sort is not this list's.
	e.expect(t, "GET", "/api/dispatch/tickets?project=LST&sort=title&cursor="+out["next"].(string), nil, http.StatusBadRequest, "invalid_request")
}

// A query's parameters are separated by '&' alone, as a browser reads a
// form's: a ';' is part of a value, sent as it is (the web app's encoder
// leaves it so) or escaped, in a search and in a name alike, where
// url.ParseQuery would refuse the query whole.
func TestDispatchAQueryValueMayHoldASemicolon(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "SEM", nil)
	e.newDispatchTicket(t, "SEM", "a;b in the title", map[string]any{"owner": "Ann;Bo"})
	e.newDispatchTicket(t, "SEM", "Another", map[string]any{"owner": "Ann"})
	for _, q := range []string{"q=a;b", "q=a%3Bb", "owner=Ann;Bo", "owner=Ann%3BBo", "q=a;b&owner=Ann;Bo"} {
		out := e.expect(t, "GET", "/api/dispatch/tickets?project=SEM&"+q, nil, http.StatusOK, "")
		if tks := out["tickets"].([]any); len(tks) != 1 || tks[0].(map[string]any)["key"] != "SEM-1" {
			t.Errorf("%s: %v", q, out)
		}
	}
	// A ';' in a parameter's name makes no parameter the route takes.
	e.expect(t, "GET", "/api/dispatch/tickets?project=SEM&a;b=1", nil, http.StatusBadRequest, "invalid_request")
}

// A read takes the parameters its route names and no other, as a write's
// body takes no field it does not name: one it does not take, and a query
// string holding a %-escape that is not one, are 400 invalid_request, on
// the reads that take none (the people, a project, a ticket, a receipt)
// as on those that take some, while the same read without them answers.
func TestDispatchReadsRefuseParametersTheyDoNotTake(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	// What the requests of dispatchRequests read: the project its receipt
	// a1 made, and its ticket.
	e.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "a1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	e.newDispatchTicket(t, "CND", "Read me", nil)
	// The download reads an attachment of the ticket's, whose id its
	// request names in place of the one dispatchRequests holds.
	attachment := "/api/dispatch/attachments/" + entityOf(t, e.upload(t, "CND-1", "read.txt", "text/plain", []byte("read me"), http.StatusCreated, ""), "attachment")["id"].(string)
	// The revision's read reads a plan revision of the ticket's, made
	// through the store, in place of the id dispatchRequests holds.
	rc := doneOf(t)(e.srv.dispatch.ProposePlan(context.Background(), adminOp(""), "CND-1", dispatch.PlanRequest{Crew: "team"},
		dispatch.PlanBody{Base: strings.Repeat("a", 40), Crew: json.RawMessage(`{"id":"team","name":"Team","tasks":[{"id":"build"}]}`)}))
	plan := "/api/dispatch/plans/" + rc.ID
	takeNone := []string{"/api/dispatch/people", "/api/dispatch/projects/CND", "/api/dispatch/tickets/CND-1", "/api/dispatch/ops/a1", attachment,
		"/api/dispatch/tickets/CND-1/plans", plan}
	reads := 0
	eachDispatchRoute(t, e.srv, func(method, path string, _ any) {
		if method != http.MethodGet {
			return
		}
		reads++
		if strings.HasPrefix(path, "/api/dispatch/attachments/") {
			// The file itself, not JSON.
			path = attachment
			if status, _, data, _ := e.send(t, method, path, nil, ""); status != http.StatusOK || string(data) != "read me" {
				t.Fatalf("%s: %d %q", path, status, data)
			}
		} else {
			if strings.HasPrefix(path, "/api/dispatch/plans/") {
				path = plan
			}
			e.expect(t, method, path, nil, http.StatusOK, "")
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		refused := []string{"bogus=1", "q=%zz", "%zz=1"}
		if slices.Contains(takeNone, path) {
			refused = append(refused, "limit=1", "after=1", "q=x", "x")
		}
		for _, q := range refused {
			e.expect(t, method, path+sep+q, nil, http.StatusBadRequest, "invalid_request")
		}
	})
	if reads != 13 {
		t.Fatalf("%d reads", reads)
	}
}

// A receipt is read back by the admin's op id without acting: a done
// one's and a refused one's, 404 for an id unknown or another actor's,
// 400 for an id that is not one.
func TestDispatchReceiptIsReadBackWithoutActing(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	created := e.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "r1", "key": "CND", "name": "Conductor"}, http.StatusCreated, "")
	project := entityOf(t, created, "project")
	out := e.expect(t, "GET", "/api/dispatch/ops/r1", nil, http.StatusOK, "")
	rc := receiptOf(t, out)
	if rc["opId"] != "r1" || rc["intent"] != "project.create" || rc["status"] != "done" || rc["key"] != "CND" || rc["id"] != project["id"] || rc["rev"] != project["rev"] {
		t.Fatalf("receipt: %v", rc)
	}
	e.expect(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "r2", "key": "CND", "name": "Again"}, http.StatusConflict, "key_taken")
	if rc := receiptOf(t, e.expect(t, "GET", "/api/dispatch/ops/r2", nil, http.StatusOK, "")); rc["status"] != "failed" || rc["refusal"] != "key_taken" {
		t.Fatalf("a refusal's receipt: %v", rc)
	}
	// Another actor's receipt is not the admin's.
	done(e.srv.dispatch.AddPerson(context.Background(), dispatch.Op{Actor: "agent:builder", ID: "r3"}, "Ada"))
	for _, id := range []string{"r3", "r9"} {
		e.expect(t, "GET", "/api/dispatch/ops/"+id, nil, http.StatusNotFound, "not_found")
	}
	e.expect(t, "GET", "/api/dispatch/ops/r%201", nil, http.StatusBadRequest, "invalid_request")
	e.expect(t, "GET", "/api/dispatch/ops/"+strings.Repeat("r", 33), nil, http.StatusBadRequest, "invalid_request")
	// The reads acted on nothing.
	if p := entityOf(t, e.expect(t, "GET", "/api/dispatch/projects/CND", nil, http.StatusOK, ""), "project"); p["rev"] != project["rev"] || p["name"] != "Conductor" {
		t.Fatalf("a read acted: %v", p)
	}
}

// A ticket's body holds every field at its bound with each character
// doubled by escaping: a description at its bound of escaped characters
// is accepted and read back whole, a field past its own bound is the
// store's bound, and a body past maxTicketBody is 413 bound, on the
// create and on the edit. The other writes take 64 KiB.
func TestDispatchTicketBodiesAreBounded(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.newDispatchProject(t, "BND", nil)
	description := strings.Repeat(`"`, dispatch.MaxTicketDescription/2) + strings.Repeat(`\`, dispatch.MaxTicketDescription/2)
	acceptance := strings.Repeat("\n", dispatch.MaxAcceptance)
	title := strings.Repeat(string(rune(0x2028)), dispatch.MaxTicketTitle)
	body := encodeNoHTML(t, map[string]any{"opId": opID(), "project": "BND", "title": title, "description": description, "acceptance": acceptance})
	if len(body) <= 2*(dispatch.MaxTicketDescription+dispatch.MaxAcceptance) || len(body) > maxTicketBody {
		t.Fatalf("a body of %d bytes", len(body))
	}
	e.expect(t, "POST", "/api/dispatch/tickets", body, http.StatusCreated, "")
	tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/BND-1", nil, http.StatusOK, ""), "ticket")
	if tk["description"] != description || tk["acceptance"] != acceptance || tk["title"] != title {
		t.Fatal("the ticket did not read back whole")
	}
	// A field past its own bound, the body within its: the store's bound.
	e.expect(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": opID(), "project": "BND", "title": "x", "description": strings.Repeat("d", dispatch.MaxTicketDescription+1)}, http.StatusConflict, "bound")
	// A body past its bound by one byte, on the create and the edit, and on
	// a write of 64 KiB.
	pad := func(prefix, suffix string, bound int) []byte {
		return []byte(prefix + strings.Repeat("<", bound+1-len(prefix)-len(suffix)) + suffix)
	}
	for _, tc := range []struct {
		path, prefix, suffix string
		bound                int
	}{
		{"/api/dispatch/tickets", `{"opId":"big1","project":"BND","title":"x","description":"`, `"}`, maxTicketBody},
		{"/api/dispatch/tickets/BND-1/edit", `{"opId":"big2","set":{"description":"`, `"}}`, maxTicketBody},
		{"/api/dispatch/projects", `{"opId":"big3","key":"BIG","name":"x","description":"`, `"}`, maxBody},
		{"/api/dispatch/tickets/BND-1/accept", `{"opId":"big4","note":"`, `"}`, maxBody},
	} {
		raw := pad(tc.prefix, tc.suffix, tc.bound)
		if len(raw) != tc.bound+1 {
			t.Fatalf("a body of %d bytes", len(raw))
		}
		e.expect(t, "POST", tc.path, raw, http.StatusRequestEntityTooLarge, "bound")
	}
	// An edit within the body's bound is taken, a description at its own.
	edit := encodeNoHTML(t, map[string]any{"opId": opID(), "set": map[string]any{"description": strings.Repeat(`\`, dispatch.MaxTicketDescription)}})
	if len(edit) <= 2*dispatch.MaxTicketDescription || len(edit) > maxTicketBody {
		t.Fatalf("an edit of %d bytes", len(edit))
	}
	e.expect(t, "POST", "/api/dispatch/tickets/BND-1/edit", edit, http.StatusOK, "")
}

// Every write's body is bounded before the store sees it, on every POST
// route of the table, the sixteen that opAt starts among them: a body one
// byte past its route's bound (maxTicketBody on a ticket's create and
// edit, maxAttachmentBody on an attachment's upload, maxBody on every
// other write) is 413 bound, and one at its bound is read whole, the store
// then refusing its display name (400 invalid_request), so no route's
// bound moves, up or down, without this failing. A write added later is
// held here to maxBody unless bounds names its own. The upload's body is
// multipart, its display name a field before the file and its file the
// rest (shapes); the route reads the whole of it before it answers.
func TestDispatchEveryWriteBodyIsBounded(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	const upload = "POST /api/dispatch/tickets/{key}/attachments"
	bounds := map[string]int{
		"POST /api/dispatch/tickets":             maxTicketBody,
		"POST /api/dispatch/tickets/{key}/edit":  maxTicketBody,
		"POST /api/dispatch/tickets/{key}/plans": maxTicketBody,
		upload:                                   maxAttachmentBody,
	}
	// A ticket's create checks its project's key before the store sees
	// the body.
	fields := map[string]string{"POST /api/dispatch/tickets": `"project":"CND",`}
	refusal := strings.TrimPrefix(dispatch.ErrBadBy.Error(), "dispatch: ")
	// shapes are the bodies of n bytes that are not JSON, with their
	// Content-Type: the upload's, its display name one character past its
	// bound and its file the rest.
	shapes := map[string]func(n int) ([]byte, string){
		upload: func(n int) ([]byte, string) {
			fields := [][2]string{{"opId", opID()}, {"byName", strings.Repeat("n", dispatch.MaxByName+1)}}
			empty, _ := uploadOf(t, 0, fields, "pad.bin", "application/octet-stream", nil)
			return uploadOf(t, 0, fields, "pad.bin", "application/octet-stream", bytes.Repeat([]byte{'n'}, n-len(empty)))
		},
	}
	writes := 0
	for _, r := range e.srv.dispatchRoutes() {
		if !strings.HasPrefix(r.pattern, "POST ") {
			continue
		}
		req, ok := dispatchRequests[r.pattern]
		if !ok {
			t.Fatalf("no request of %s", r.pattern)
		}
		bound, ok := bounds[r.pattern]
		if !ok {
			bound = maxBody
		}
		writes++
		// body is n bytes of one JSON object of the route's fields, its
		// display name the rest, or of the route's shape.
		body := func(n int) ([]byte, string) {
			if shape, ok := shapes[r.pattern]; ok {
				return shape(n)
			}
			prefix, suffix := `{"opId":"`+opID()+`",`+fields[r.pattern]+`"byName":"`, `"}`
			return []byte(prefix + strings.Repeat("n", n-len(prefix)-len(suffix)) + suffix), "application/json"
		}
		for _, n := range []int{bound + 1, bound} {
			b, ct := body(n)
			if len(b) != n {
				t.Fatalf("%s: a body of %d bytes, not %d", r.pattern, len(b), n)
			}
			status, _, data, out := e.send(t, "POST", req.path, b, ct)
			switch {
			case n > bound && (status != http.StatusRequestEntityTooLarge || errorCode(out) != "bound"):
				t.Fatalf("%s, a body past its bound: %d %.300s", r.pattern, status, data)
			case n == bound && (status != http.StatusBadRequest || errorCode(out) != "invalid_request" || out["error"].(map[string]any)["message"] != refusal):
				t.Fatalf("%s, a body at its bound: %d %.300s, want the display name's refusal", r.pattern, status, data)
			}
		}
	}
	if writes != 32 {
		t.Fatalf("%d writes", writes)
	}
}

// filler makes the texts of a test's largest entities from two
// characters, the bits of i choosing between them, so the i-th text of a
// length differs from the others where a rule keeps names unique.
type filler struct {
	name string
	// bytes are one byte each, for a field bounded in bytes; chars one
	// character each, for one bounded in characters.
	bytes, chars [2]string
}

var (
	// worstFiller makes every text at its escaped worst with HTML escaping
	// off: a field bounded in bytes of '"' and '\', two bytes each as
	// JSON; one bounded in characters of U+2028 and U+2029, three bytes
	// each and six as JSON whatever SetEscapeHTML says.
	worstFiller = filler{name: "escaped worst", bytes: [2]string{`"`, `\`}, chars: [2]string{string(rune(0x2028)), string(rune(0x2029))}}
	// angleFiller makes every text of '<' and '>', six bytes each as the
	// default encoder writes them and one as the replies do.
	angleFiller = filler{name: "angle brackets", bytes: [2]string{"<", ">"}, chars: [2]string{"<", ">"}}
)

// pick is n of the two, the j-th the second where bit j of i is set.
func pick(two [2]string, n, i int) string {
	var b strings.Builder
	for j := range n {
		k := 0
		if j < 62 && i>>j&1 == 1 {
			k = 1
		}
		b.WriteString(two[k])
	}
	return b.String()
}

// text is the i-th text of n bytes.
func (f filler) text(n, i int) string { return pick(f.bytes, n, i) }

// runes is the i-th text of n characters.
func (f filler) runes(n, i int) string { return pick(f.chars, n, i) }

// url is the i-th http URL of n bytes.
func (f filler) url(n, i int) string {
	const base = "https://h/"
	return base + f.text(n-len(base), i)
}

// file is the i-th attachment name at its bound of 255 bytes, which no
// '\' may be in: of the characters when they are longer than a byte.
func (f filler) file(i int) string {
	if n := len(f.chars[0]); n > 1 {
		return f.runes(dispatch.MaxAttachmentName/n, i)
	}
	return f.text(dispatch.MaxAttachmentName, i)
}

// largestTicket is what buildLargestTicket made, as the ticket's read
// must give it back.
type largestTicket struct {
	key, by, title, description, acceptance, owner, note, parentTitle string
	assignees                                                         []string
	comments, relationTitles, childTitles                             []string
	links                                                             []dispatch.NewLink
	attachments                                                       []string
	// approvedNotes are the approved revisions' notes the read carries,
	// newest first.
	approvedNotes []string
}

// buildLargestTicket makes, through the store, the largest ticket the
// server can make, every field and set at its bound and every text of
// f's: its title, description, acceptance criteria, owner, eight
// assignees, twenty labels, a milestone, a parent and the acceptance with
// its note; fifty comments, fifty links, a hundred relations and fifty
// children, each other ticket's title at its bound; and fifty attachments,
// planted in the database as their upload writes them, each of 16 MiB with
// a name and a type at their bounds (fifty uploads at that size would
// write 800 MiB, and a reply carries a row, never its file), every
// operation by a display name at its bound. And one more approved plan
// revision than the read carries (dispatch.MaxTicketApproved), as merges
// may leave them, each with its crew's name and its approval's note at
// their bounds and stale for both reasons, its handed-over comment edited
// and the title, the description and the criteria changed since, so its
// words are their longest; the edits come before the acceptance, which
// they would otherwise unmeet.
func buildLargestTicket(t *testing.T, e *testEnv, f filler, key string) largestTicket {
	t.Helper()
	done := doneOf(t)
	ctx := context.Background()
	ds := e.srv.dispatch
	by := f.runes(dispatch.MaxByName, 0)
	op := func() dispatch.Op { return adminOp(by) }
	done(ds.CreateProject(ctx, op(), dispatch.NewProject{Key: key, Name: key}))
	var labels []string
	for i := range dispatch.MaxTicketLabels {
		labels = append(labels, done(ds.CreateLabel(ctx, op(), key, dispatch.NewLabel{Name: f.runes(dispatch.MaxLabelName, i), Color: "yellow"})).ID)
	}
	milestone := done(ds.CreateMilestone(ctx, op(), key, dispatch.NewMilestone{Name: f.runes(dispatch.MaxMilestoneName, 0)})).ID
	want := largestTicket{
		by: by, title: f.runes(dispatch.MaxTicketTitle, 1), description: f.text(dispatch.MaxTicketDescription, 0),
		acceptance: f.text(dispatch.MaxAcceptance, 1), owner: f.runes(dispatch.MaxPersonName, 1), note: f.runes(dispatch.MaxAcceptedNote, 0),
		parentTitle: f.runes(dispatch.MaxTicketTitle, 2),
	}
	parent := done(ds.CreateTicket(ctx, op(), dispatch.NewTicket{Project: key, Title: want.parentTitle})).Key
	for i := range dispatch.MaxAssignees {
		want.assignees = append(want.assignees, "person:"+f.runes(dispatch.MaxPersonName, 2+i))
	}
	want.key = done(ds.CreateTicket(ctx, op(), dispatch.NewTicket{
		Project: key, Title: want.title, Description: want.description, Acceptance: want.acceptance,
		Status: dispatch.TicketInProgress, Priority: dispatch.MaxPriority, Horizon: dispatch.HorizonLater, Owner: want.owner,
		Assignees: want.assignees, Labels: labels, Milestone: milestone, Parent: parent,
	})).Key
	for i := range dispatch.MaxRelations {
		title := f.runes(dispatch.MaxTicketTitle, 3+i)
		n := dispatch.NewTicket{Project: key, Title: title, Status: dispatch.TicketInProgress}
		if i < dispatch.MaxTicketChildren {
			n.Parent = want.key
			want.childTitles = append(want.childTitles, title)
		}
		other := done(ds.CreateTicket(ctx, op(), n)).Key
		done(ds.AddRelation(ctx, op(), want.key, dispatch.RelationRelates, other))
		want.relationTitles = append(want.relationTitles, title)
	}
	for i := range dispatch.MaxLinks {
		l := dispatch.NewLink{Kind: dispatch.LinkExternal, Ref: f.text(dispatch.MaxLinkRef, i), URL: f.url(dispatch.MaxLinkURL, i), Note: f.runes(dispatch.MaxLinkNote, i)}
		done(ds.AddLink(ctx, op(), want.key, l))
		want.links = append(want.links, l)
	}
	var handed string
	for i := range dispatch.MaxTicketComments {
		body := f.text(dispatch.MaxCommentBody, i)
		id := done(ds.AddComment(ctx, op(), want.key, body)).ID
		if i == 0 {
			handed = id
		}
		want.comments = append(want.comments, body)
	}
	for i := range dispatch.MaxTicketApproved + 1 {
		crew, err := json.Marshal(map[string]any{"id": "crew", "name": f.runes(dispatch.MaxCrewName, i), "tasks": []map[string]any{{"id": "build"}}})
		if err != nil {
			t.Fatal(err)
		}
		body := dispatch.PlanBody{Base: strings.Repeat("ab", 20), Crew: crew, Comments: []dispatch.PlanComment{{ID: handed}}}
		id := done(ds.ProposePlan(ctx, op(), want.key, dispatch.PlanRequest{Crew: "crew", Comments: []string{handed}}, body)).ID
		note := f.runes(dispatch.MaxApprovedNote, i)
		done(ds.ApprovePlan(ctx, op(), id, 0, note))
		if i > 0 {
			want.approvedNotes = append([]string{note}, want.approvedNotes...)
		}
	}
	want.comments[0] = f.text(dispatch.MaxCommentBody, dispatch.MaxTicketComments)
	done(ds.EditComment(ctx, op(), want.key, handed, want.comments[0]))
	want.title, want.description, want.acceptance = f.runes(dispatch.MaxTicketTitle, 5000), f.text(dispatch.MaxTicketDescription, 1), f.text(dispatch.MaxAcceptance, 2)
	done(ds.EditTicket(ctx, op(), want.key, dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &want.title, Description: &want.description, Acceptance: &want.acceptance}}))
	done(ds.AcceptTicket(ctx, op(), want.key, want.note))
	tk, err := ds.Ticket(ctx, want.key)
	if err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, filepath.Join(ds.Dir(), dispatch.File))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i := range dispatch.MaxAttachments {
		name := f.file(i)
		if _, err := tx.Exec(`INSERT INTO attachments (id, ticket_id, name, size, sha256, content_type, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("%016x", i+1), tk.ID, name, dispatch.MaxAttachmentSize, strings.Repeat("0123456789abcdef", 4), f.text(dispatch.MaxContentType, i),
			at.Add(time.Duration(i)).Format("2006-01-02T15:04:05.000000000Z"), by); err != nil {
			t.Fatal(err)
		}
		want.attachments = append(want.attachments, name)
	}
	// Every revision approved, as merges of as many replicas leave them:
	// a local approval supersedes the others.
	if _, err := tx.Exec("UPDATE plans SET state = ? WHERE ticket_id = ?", dispatch.PlanApproved, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return want
}

// The largest ticket the server can make, every field and set at its
// bound, is read back whole and under MaxTicketReply: with every text at
// its escaped worst, which is what the bound is, and with every text of
// '<' and '>', which the reply holds as they are and never as the six
// bytes of the default encoder; and so it is through its session's read,
// linked to a run (sessionReadOfTheLargest).
func TestDispatchTheLargestTicketIsUnderItsBound(t *testing.T) {
	t.Parallel()
	for i, f := range []filler{worstFiller, angleFiller} {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			e := dispatchEnv(t)
			want := buildLargestTicket(t, e, f, fmt.Sprintf("BIG%d", i))
			status, raw, _ := e.call(t, "GET", "/api/dispatch/tickets/"+want.key, nil)
			if status != http.StatusOK {
				t.Fatalf("read: %d %.300s", status, raw)
			}
			t.Logf("the largest ticket's reply, %s: %d bytes of %d", f.name, len(raw), MaxTicketReply)
			if len(raw) >= MaxTicketReply {
				t.Fatalf("the largest ticket's reply is %d bytes, not under %d", len(raw), MaxTicketReply)
			}
			if f == angleFiller {
				for _, esc := range []string{"u003c", "u003e"} {
					if bytes.Contains(raw, []byte(`\`+esc)) {
						t.Fatalf("the reply holds the escape of %s", esc)
					}
				}
			}
			var out struct {
				Ticket dispatch.TicketView `json:"ticket"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			tk := out.Ticket
			if tk.Title != want.title || tk.Description != want.description || tk.Acceptance != want.acceptance || tk.Owner != want.owner ||
				tk.AcceptedNote != want.note || tk.AcceptedBy != want.by || tk.AcceptedFor != want.owner || tk.CreatedBy != want.by {
				t.Fatal("the ticket's own fields did not read back whole")
			}
			if !tk.Close.Met.Acceptance || len(tk.Labels) != dispatch.MaxTicketLabels || tk.MilestoneID == "" || tk.Parent == nil || tk.Parent.Title != want.parentTitle {
				t.Fatalf("the ticket's references: %+v", tk.Close)
			}
			if !slices.Equal(tk.Assignees, slices.Sorted(slices.Values(want.assignees))) {
				t.Fatal("the assignees did not read back whole")
			}
			if len(tk.Approved) != dispatch.MaxTicketApproved {
				t.Fatalf("%d approved revisions", len(tk.Approved))
			}
			for i, p := range tk.Approved {
				if p.ApprovedNote != want.approvedNotes[i] || p.ApprovedBy != want.by || p.Stale == nil || p.Stale.Remedy != dispatch.RemedyRevise ||
					!strings.Contains(p.Stale.Words, "the title, the description and the acceptance criteria changed") {
					t.Fatalf("approved revision %d did not read back whole: %+v", i, p.Stale)
				}
			}
			if len(tk.Comments) != len(want.comments) {
				t.Fatalf("%d comments", len(tk.Comments))
			}
			for i, c := range tk.Comments {
				if c.Body != want.comments[i] || c.By != want.by || c.N != i+1 {
					t.Fatalf("comment %d did not read back whole", i+1)
				}
			}
			if len(tk.Links) != len(want.links) {
				t.Fatalf("%d links", len(tk.Links))
			}
			for i, l := range tk.Links {
				if w := want.links[i]; l.Ref != w.Ref || l.URL != w.URL || l.Note != w.Note || l.Kind != w.Kind || l.CreatedBy != want.by {
					t.Fatalf("link %d did not read back whole", i)
				}
			}
			if len(tk.Relations) != dispatch.MaxRelations || tk.RelationsTotal != dispatch.MaxRelations {
				t.Fatalf("%d relations of %d", len(tk.Relations), tk.RelationsTotal)
			}
			var titles []string
			for _, r := range tk.Relations {
				titles = append(titles, r.Other.Title)
			}
			if !sameSet(titles, want.relationTitles) {
				t.Fatal("the relations' titles did not read back whole")
			}
			titles = titles[:0]
			for _, c := range tk.Children.Tickets {
				titles = append(titles, c.Title)
			}
			if tk.Children.Total != dispatch.MaxTicketChildren || !slices.Equal(titles, want.childTitles) {
				t.Fatalf("the children did not read back whole: %d of %d", len(titles), tk.Children.Total)
			}
			if len(tk.Attachments) != len(want.attachments) {
				t.Fatalf("%d attachments", len(tk.Attachments))
			}
			for i, a := range tk.Attachments {
				if a.Name != want.attachments[i] || a.CreatedBy != want.by {
					t.Fatalf("attachment %d did not read back whole", i)
				}
			}
			sessionReadOfTheLargest(t, e, f, want)
		})
	}
}

// The largest page of a ticket's comments, maxCommentPage of them, each at
// its escaped worst (a body of 16 KiB of '"' and '\', the author and the
// display name at their bounds, edited and then deleted, so it carries
// both times), is read back whole and under MaxTicketReply, as the
// ticket's own read is; the page after it holds the rest, and a page of
// one more is refused. The store's own bound, 200, would be more than
// twice MaxTicketReply.
func TestDispatchTheLargestPageOfCommentsIsUnderItsBound(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	ds, f := e.srv.dispatch, worstFiller
	actor, by := f.text(dispatch.MaxActor, 0), f.runes(dispatch.MaxByName, 0)
	op := func() dispatch.Op { return dispatch.Op{Actor: actor, ID: opID(), By: by} }
	done(ds.CreateProject(ctx, adminOp(""), dispatch.NewProject{Key: "CMT", Name: "Comments"}))
	key := done(ds.CreateTicket(ctx, adminOp(""), dispatch.NewTicket{Project: "CMT", Title: "Many comments"})).Key
	var bodies []string
	for i := range maxCommentPage + 1 {
		id := done(ds.AddComment(ctx, op(), key, f.text(dispatch.MaxCommentBody, i))).ID
		body := f.text(dispatch.MaxCommentBody, maxCommentPage+1+i)
		done(ds.EditComment(ctx, op(), key, id, body))
		done(ds.DeleteComment(ctx, op(), key, id))
		bodies = append(bodies, body)
	}
	page := func(query string) (raw []byte, comments []dispatch.Comment) {
		t.Helper()
		status, raw, _ := e.call(t, "GET", "/api/dispatch/tickets/"+key+"/comments"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %.300s", query, status, raw)
		}
		var out struct {
			Comments []dispatch.Comment `json:"comments"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return raw, out.Comments
	}
	for _, query := range []string{fmt.Sprintf("?limit=%d", maxCommentPage), ""} {
		raw, comments := page(query)
		t.Logf("the largest page of comments (%q): %d bytes of %d", query, len(raw), MaxTicketReply)
		if len(raw) >= MaxTicketReply {
			t.Fatalf("the largest page of comments is %d bytes, not under %d", len(raw), MaxTicketReply)
		}
		if len(comments) != maxCommentPage {
			t.Fatalf("%q: %d comments", query, len(comments))
		}
		for i, c := range comments {
			if c.N != i+1 || c.Body != bodies[i] || c.Author != actor || c.By != by || c.EditedAt.IsZero() || c.DeletedAt.IsZero() {
				t.Fatalf("comment %d did not read back whole", i+1)
			}
		}
	}
	if _, rest := page(fmt.Sprintf("?after=%d", maxCommentPage)); len(rest) != 1 || rest[0].N != maxCommentPage+1 || rest[0].Body != bodies[maxCommentPage] {
		t.Fatalf("the page after it: %d comments", len(rest))
	}
	e.expect(t, "GET", fmt.Sprintf("/api/dispatch/tickets/%s/comments?limit=%d", key, maxCommentPage+1), nil, http.StatusBadRequest, "invalid_request")
}

// sameSet reports whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// worstViewQuery is a saved view's query of as near MaxViewQuery bytes as
// it can be, as stored and as a reply carries it (a view's query is the
// JSON it is, compacted, which leaves it as it is): assignees whose names
// are written as the six-byte escape of U+2028.
func worstViewQuery(t *testing.T) string {
	t.Helper()
	esc := `\` + "u2028"
	entry := func(chars int) string { return `"person:` + strings.Repeat(esc, chars) + `"` }
	var entries []string
	size := func(extra string) int {
		return len(`{"assignees":[`) + len(strings.Join(append(entries, extra), ",")) + len(`]}`)
	}
	for size(entry(dispatch.MaxPersonName)) <= dispatch.MaxViewQuery {
		entries = append(entries, entry(dispatch.MaxPersonName))
	}
	for chars := dispatch.MaxPersonName - 1; chars > 0; chars-- {
		if size(entry(chars)) <= dispatch.MaxViewQuery {
			entries = append(entries, entry(chars))
			break
		}
	}
	q := `{"assignees":[` + strings.Join(entries, ",") + `]}`
	if len(q) > dispatch.MaxViewQuery || len(q) < dispatch.MaxViewQuery-len(esc) {
		t.Fatalf("a query of %d bytes", len(q))
	}
	if _, err := dispatch.ParseViewQuery(q); err != nil {
		t.Fatal(err)
	}
	return q
}

// The largest project read, its fields at their bounds, 100 labels, 100
// milestones with 4 KiB descriptions and 200 views with 8 KiB queries,
// every text at its escaped worst, is under MaxProjectReply and read back
// whole; so is the views' read, which carries the same 200 views.
func TestDispatchTheLargestProjectIsUnderItsBound(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	ds, f := e.srv.dispatch, worstFiller
	by := f.runes(dispatch.MaxByName, 0)
	op := func() dispatch.Op { return adminOp(by) }
	project := dispatch.NewProject{
		Key: "LARGE", Name: f.runes(dispatch.MaxProjectName, 0), Description: f.text(dispatch.MaxProjectDescription, 0),
		Cwd: f.text(dispatch.MaxCwd, 1), Tracker: dispatch.TrackerExternal, TrackerURL: f.url(dispatch.MaxTrackerURL, 0),
		CloseRequires: &[]string{dispatch.RequireAcceptance, dispatch.RequireDelivery},
	}
	done(ds.CreateProject(ctx, op(), project))
	for i := range dispatch.MaxLabels {
		done(ds.CreateLabel(ctx, op(), "LARGE", dispatch.NewLabel{Name: f.runes(dispatch.MaxLabelName, i), Color: "yellow"}))
	}
	for i := range dispatch.MaxMilestones {
		done(ds.CreateMilestone(ctx, op(), "LARGE", dispatch.NewMilestone{
			Name: f.runes(dispatch.MaxMilestoneName, i), Description: f.text(dispatch.MaxMilestoneDescription, i), Target: "2026-12-31", State: dispatch.MilestoneClosed,
		}))
	}
	query := worstViewQuery(t)
	for i := range dispatch.MaxViews {
		done(ds.CreateView(ctx, op(), dispatch.NewView{Project: "LARGE", Name: f.runes(dispatch.MaxViewName, i), Kind: dispatch.ViewRoadmap, Query: query}))
	}
	status, raw, _ := e.call(t, "GET", "/api/dispatch/projects/LARGE", nil)
	if status != http.StatusOK {
		t.Fatalf("read: %d %.300s", status, raw)
	}
	t.Logf("the largest project's reply: %d bytes of %d", len(raw), MaxProjectReply)
	if len(raw) >= MaxProjectReply {
		t.Fatalf("the largest project's reply is %d bytes, not under %d", len(raw), MaxProjectReply)
	}
	var out dispatch.ProjectView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	p := out.Project
	if p.Name != project.Name || p.Description != project.Description || p.Cwd != project.Cwd || p.TrackerURL != project.TrackerURL {
		t.Fatal("the project's fields did not read back whole")
	}
	if len(out.Labels) != dispatch.MaxLabels || len(out.Milestones) != dispatch.MaxMilestones || len(out.Views) != dispatch.MaxViews {
		t.Fatalf("%d labels, %d milestones, %d views", len(out.Labels), len(out.Milestones), len(out.Views))
	}
	for _, v := range out.Views {
		if string(v.Query) != query {
			t.Fatal("a view's query did not read back whole")
		}
	}
	for _, m := range out.Milestones {
		if len(m.Description) != dispatch.MaxMilestoneDescription {
			t.Fatal("a milestone's description did not read back whole")
		}
	}
	for _, path := range []string{"/api/dispatch/views?project=LARGE", "/api/dispatch/views"} {
		status, raw, _ := e.call(t, "GET", path, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %.300s", path, status, raw)
		}
		t.Logf("the largest views' reply, %s: %d bytes of %d", path, len(raw), MaxProjectReply)
		if len(raw) >= MaxProjectReply {
			t.Fatalf("the views' reply, %s, is %d bytes, not under %d", path, len(raw), MaxProjectReply)
		}
		var read struct {
			Views []dispatch.View `json:"views"`
		}
		if err := json.Unmarshal(raw, &read); err != nil {
			t.Fatal(err)
		}
		if len(read.Views) != dispatch.MaxViews {
			t.Fatalf("%s: %d views", path, len(read.Views))
		}
		for _, v := range read.Views {
			if string(v.Query) != query || v.Name == "" {
				t.Fatalf("%s: a view did not read back whole", path)
			}
		}
	}
}

// 201 projects, each with a description of 16 KiB of backslashes, a name
// at its escaped worst, an external tracker's URL and archived, are listed
// whole over two pages, each under maxProjectsPage: a summary carries
// none of what grows.
func TestDispatchProjectsArePagedUnderTheirBound(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	ds, f := e.srv.dispatch, worstFiller
	const n = dispatch.MaxProjectPage + 1
	for i := range n {
		key := fmt.Sprintf("P%05d", i)
		done(ds.CreateProject(ctx, adminOp(""), dispatch.NewProject{
			Key: key, Name: f.runes(dispatch.MaxProjectName, i), Description: strings.Repeat(`\`, dispatch.MaxProjectDescription),
			Tracker: dispatch.TrackerExternal, TrackerURL: f.url(dispatch.MaxTrackerURL, i),
		}))
		done(ds.ArchiveProject(ctx, adminOp(""), key))
	}
	var keys []string
	cursor, pages := "", 0
	for {
		path := "/api/dispatch/projects?archived=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, raw, out := e.call(t, "GET", path, nil)
		if status != http.StatusOK {
			t.Fatalf("page %d: %d %.300s", pages+1, status, raw)
		}
		pages++
		t.Logf("page %d: %d bytes of %d", pages, len(raw), maxProjectsPage)
		if len(raw) >= maxProjectsPage {
			t.Fatalf("page %d is %d bytes, not under %d", pages, len(raw), maxProjectsPage)
		}
		for _, p := range out["projects"].([]any) {
			keys = append(keys, p.(map[string]any)["key"].(string))
		}
		cursor, _ = out["next"].(string)
		if cursor == "" {
			break
		}
	}
	if pages != 2 || len(keys) != n || !slices.IsSorted(keys) || keys[0] != "P00000" || keys[n-1] != fmt.Sprintf("P%05d", n-1) {
		t.Fatalf("%d pages of %d projects", pages, len(keys))
	}
	if out := e.expect(t, "GET", "/api/dispatch/projects", nil, http.StatusOK, ""); len(out["projects"].([]any)) != 0 {
		t.Fatalf("archived projects listed without archived=1: %d", len(out["projects"].([]any)))
	}
}

// A run's ticket, for the run page's chip: none before a link; the one
// ticket a live run of the engine and a run's record are linked to; after
// that ticket's delete, still it with deletedAt; once the run is linked to
// a second ticket, the second, and still after the first's undelete,
// which removes the first's link. A run link names a run the engine holds
// or has a record of, and nothing else.
func TestDispatchRunTicketLookup(t *testing.T) {
	t.Parallel()
	done := doneOf(t)
	e := dispatchEnv(t)
	ctx := context.Background()
	ds := e.srv.dispatch
	live := e.launchCrew(t, "Linked", catMember("a", "immediately"))
	t.Cleanup(func() { e.do("POST", "/api/runs/"+live+"/stop", adminToken, nil) })
	recorded := crew.Run{ID: "recorded-0000beef", CrewID: "recorded", Name: "Recorded", StartedAt: time.Now().UTC().Add(-time.Hour), State: crew.RunFinished}
	if err := e.srv.records.Save(recorded); err != nil {
		t.Fatal(err)
	}
	if _, held := e.srv.runs.Get(recorded.ID); held {
		t.Fatal("the engine holds the recorded run")
	}
	e.newDispatchProject(t, "RUN", nil)
	lookup := func(run string) map[string]any {
		t.Helper()
		out := e.expect(t, "GET", "/api/dispatch/links?run="+run, nil, http.StatusOK, "")
		tk, _ := out["ticket"].(map[string]any)
		return tk
	}
	for _, run := range []string{live, recorded.ID} {
		first := e.newDispatchTicket(t, "RUN", "First for "+run, nil)["key"].(string)
		second := e.newDispatchTicket(t, "RUN", "Second for "+run, nil)["key"].(string)
		if tk := lookup(run); tk != nil {
			t.Fatalf("%s before any link: %v", run, tk)
		}
		done(ds.AddLink(ctx, adminOp(""), first, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}))
		if tk := lookup(run); tk == nil || tk["key"] != first || tk["deletedAt"] != nil || tk["title"] != "First for "+run || tk["status"] != "backlog" {
			t.Fatalf("%s linked: %v", run, tk)
		}
		e.expect(t, "POST", "/api/dispatch/tickets/"+first+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
		if tk := lookup(run); tk == nil || tk["key"] != first || tk["deletedAt"] == nil {
			t.Fatalf("%s after its ticket's delete: %v", run, tk)
		}
		done(ds.AddLink(ctx, adminOp(""), second, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}))
		if tk := lookup(run); tk == nil || tk["key"] != second || tk["deletedAt"] != nil {
			t.Fatalf("%s linked again: %v", run, tk)
		}
		out := e.expect(t, "POST", "/api/dispatch/tickets/"+first+"/undelete", map[string]any{"opId": opID()}, http.StatusOK, "")
		if links := entityOf(t, out, "ticket")["links"].([]any); len(links) != 0 {
			t.Fatalf("the undeleted ticket keeps a link to a run another holds: %v", links)
		}
		if tk := lookup(run); tk == nil || tk["key"] != second || tk["deletedAt"] != nil {
			t.Fatalf("%s after the first's undelete: %v", run, tk)
		}
	}
	// A run neither held nor recorded is no run to link.
	tk := e.newDispatchTicket(t, "RUN", "Nothing", nil)["key"].(string)
	if rc, err := ds.AddLink(ctx, adminOp(""), tk, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: "nobody-0000dead"}); err != nil || rc.Refusal != dispatch.RefusalNotFound {
		t.Fatalf("a run nobody has: %+v %v", rc, err)
	}
	for _, q := range []string{"", "?run=", "?run=not-a-run", "?run=" + live + "&run=" + live, "?run=" + live + "&bogus=1"} {
		e.expect(t, "GET", "/api/dispatch/links"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	if got := lookup("nobody-0000dead"); got != nil {
		t.Fatalf("a run never linked: %v", got)
	}
}

// eventStream opens GET /api/events as the admin and returns what it
// streams as "<event> <data>" strings on a channel closed when the stream
// ends, so a wait on it ends with the stream. The subscription is in place
// when it returns.
func (e *testEnv) eventStream(t *testing.T) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", e.http.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events stream: %d", resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	events := make(chan string, 4096)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				select {
				case events <- name + " " + strings.TrimPrefix(line, "data: "):
				case <-t.Context().Done():
					return
				}
			}
		}
	}()
	return events
}

// Every write reaches the pages as dispatch events, one per entity it
// changed, a commit's queued together as one entry for every client
// before the write's reply is sent, and written on the stream: a
// project's create, a ticket's (with its new owner, a person whose event
// names no id, in the ticket's entry) and an edit; nothing for a
// refusal.
func TestDispatchEventsReachTheStream(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	queued := e.srv.events.subscribe()
	defer e.srv.events.unsubscribe(queued)
	stream := e.eventStream(t)
	var want []string
	// commit is the events of the next entry queued, one commit's.
	commit := func(what string) []dispatchEvent {
		t.Helper()
		select {
		case msg := <-queued:
			var evs []dispatchEvent
			for _, data := range dispatchData(t, msg) {
				var ev dispatchEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatal(err)
				}
				want = append(want, "dispatch "+data)
				evs = append(evs, ev)
			}
			return evs
		default:
			t.Fatalf("%s: no event queued by the time the reply came", what)
		}
		return nil
	}
	project := e.newDispatchProject(t, "EVT", nil)
	if evs := commit("the project's create"); len(evs) != 1 || evs[0].Entity != "project" || evs[0].ID != project["id"] || evs[0].Ticket != "" ||
		evs[0].Project != "EVT" || evs[0].Seq < 1 {
		t.Fatalf("the project's events: %+v", evs)
	}
	tk := e.newDispatchTicket(t, "EVT", "Evented", map[string]any{"owner": "Alice"})
	evs := commit("the ticket's create")
	if len(evs) != 2 {
		t.Fatalf("the ticket's create: %+v, want its event and its owner's", evs)
	}
	ticketEv := evs[0]
	if ticketEv.Entity != "ticket" || ticketEv.ID != tk["id"] || ticketEv.Ticket != tk["id"] || ticketEv.Project != "EVT" {
		t.Fatalf("the ticket's event: %+v", ticketEv)
	}
	if ev := evs[1]; ev.Entity != "person" || ev.ID != "" || ev.Ticket != "" || ev.Project != "" || ev.Seq != ticketEv.Seq {
		t.Fatalf("the person's event: %+v", ev)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/EVT-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": "Edited"}}, http.StatusOK, "")
	if evs := commit("the edit"); len(evs) != 1 || evs[0].Entity != "ticket" || evs[0].ID != tk["id"] || evs[0].Seq <= ticketEv.Seq {
		t.Fatalf("the edit's events: %+v", evs)
	}
	e.expect(t, "POST", "/api/dispatch/tickets/EVT-1/edit", map[string]any{"opId": opID(), "expectedRev": 1, "set": map[string]any{"title": "Lost"}}, http.StatusConflict, "stale_revision")
	if len(queued) != 0 {
		t.Fatalf("a refusal sent %d events", len(queued))
	}
	// The stream carries each, in order.
	for _, w := range want {
		found := false
		for ev := range stream {
			if ev == w {
				found = true
				break
			}
			if strings.HasPrefix(ev, "dispatch ") {
				t.Fatalf("the stream sent %s before %s", ev, w)
			}
		}
		if !found {
			t.Fatalf("the stream ended before %s", w)
		}
	}
}

// Every write of this change reaches the pages as dispatch events naming
// what it changed, a commit's queued as one entry before its reply and
// written on a live stream in order: a comment's names it and its
// ticket; a relation's both its tickets, the one the route names first;
// a link's its ticket; a label's, a milestone's and a view's their
// project, and a label's or a milestone's delete every ticket it left
// beside it; a view of every project names no project. A refusal sends
// nothing.
func TestDispatchEventsNameWhatEveryWriteChanged(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	queued := e.srv.events.subscribe()
	defer e.srv.events.unsubscribe(queued)
	stream := e.eventStream(t)
	var streamed []string
	var seq int64
	// commit fails the test unless the next entry queued is one commit's
	// events, those wanted in their order, each with the commit's seq,
	// later than the commit before it.
	commit := func(what string, want ...dispatchEvent) {
		t.Helper()
		var msg []byte
		select {
		case msg = <-queued:
		default:
			t.Fatalf("%s: no event queued by the time the reply came", what)
		}
		var got []dispatchEvent
		for _, data := range dispatchData(t, msg) {
			var ev dispatchEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			got = append(got, ev)
			streamed = append(streamed, "dispatch "+data)
		}
		if len(got) == 0 || got[0].Seq <= seq {
			t.Fatalf("%s: %+v after the seq %d", what, got, seq)
		}
		seq = got[0].Seq
		for i := range want {
			want[i].Seq = seq
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %+v, want %+v", what, got, want)
		}
	}
	project := e.newDispatchProject(t, "EVW", nil)
	commit("the project's create", dispatchEvent{Entity: "project", ID: project["id"].(string), Project: "EVW"})
	ticket := func(tk map[string]any) dispatchEvent {
		id := tk["id"].(string)
		return dispatchEvent{Entity: "ticket", ID: id, Ticket: id, Project: "EVW"}
	}
	a := e.newDispatchTicket(t, "EVW", "A", nil)
	commit("A's create", ticket(a))
	b := e.newDispatchTicket(t, "EVW", "B", nil)
	commit("B's create", ticket(b))
	write := func(path string, body map[string]any, status int) map[string]any {
		t.Helper()
		body["opId"] = opID()
		return e.expect(t, "POST", path, body, status, "")
	}

	// A comment's add, edit and delete name it and its ticket.
	comment := entityOf(t, write("/api/dispatch/tickets/EVW-1/comments", map[string]any{"body": "Said"}, http.StatusCreated), "comment")["id"].(string)
	commented := dispatchEvent{Entity: "comment", ID: comment, Ticket: a["id"].(string), Project: "EVW"}
	commit("a comment's add", commented)
	write("/api/dispatch/tickets/EVW-1/comments/"+comment+"/edit", map[string]any{"body": "Said again"}, http.StatusOK)
	commit("a comment's edit", commented)
	write("/api/dispatch/tickets/EVW-1/comments/"+comment+"/delete", map[string]any{}, http.StatusOK)
	commit("a comment's delete", commented)

	// A relation's add and removal name both its tickets, the route's
	// first; a link's its ticket.
	relation := receiptOf(t, write("/api/dispatch/tickets/EVW-1/relations", map[string]any{"kind": "blocks", "to": "EVW-2"}, http.StatusCreated))["id"].(string)
	commit("a relation's add", ticket(a), ticket(b))
	write("/api/dispatch/tickets/EVW-2/relations/"+relation+"/delete", map[string]any{}, http.StatusOK)
	commit("a relation's removal", ticket(b), ticket(a))
	link := receiptOf(t, write("/api/dispatch/tickets/EVW-1/links", map[string]any{"kind": "external", "url": "https://example.com/1"}, http.StatusCreated))["id"].(string)
	commit("a link's add", ticket(a))
	write("/api/dispatch/tickets/EVW-1/links/"+link+"/delete", map[string]any{}, http.StatusOK)
	commit("a link's removal", ticket(a))

	// A label's and a milestone's name their project, and their delete
	// every ticket it left.
	label := entityOf(t, write("/api/dispatch/projects/EVW/labels", map[string]any{"name": "api", "color": "blue"}, http.StatusCreated), "label")["id"].(string)
	labelled := dispatchEvent{Entity: "label", ID: label, Project: "EVW"}
	commit("a label's create", labelled)
	write("/api/dispatch/projects/EVW/labels/"+label+"/edit", map[string]any{"color": "red"}, http.StatusOK)
	commit("a label's edit", labelled)
	write("/api/dispatch/tickets/EVW-1/edit", map[string]any{"add": map[string]any{"labels": []string{label}}}, http.StatusOK)
	commit("the label set on A", ticket(a))
	write("/api/dispatch/projects/EVW/labels/"+label+"/delete", map[string]any{}, http.StatusOK)
	commit("a label's delete", labelled, ticket(a))
	milestone := entityOf(t, write("/api/dispatch/projects/EVW/milestones", map[string]any{"name": "1.0"}, http.StatusCreated), "milestone")["id"].(string)
	planned := dispatchEvent{Entity: "milestone", ID: milestone, Project: "EVW"}
	commit("a milestone's create", planned)
	write("/api/dispatch/projects/EVW/milestones/"+milestone+"/edit", map[string]any{"state": "closed"}, http.StatusOK)
	commit("a milestone's edit", planned)
	write("/api/dispatch/tickets/EVW-2/edit", map[string]any{"set": map[string]any{"milestone": milestone}}, http.StatusOK)
	commit("the milestone set on B", ticket(b))
	write("/api/dispatch/projects/EVW/milestones/"+milestone+"/delete", map[string]any{}, http.StatusOK)
	commit("a milestone's delete", planned, ticket(b))

	// A view's name its project, none for a view of every project.
	view := entityOf(t, write("/api/dispatch/views", map[string]any{"project": "EVW", "name": "Mine", "kind": "list", "query": map[string]any{}}, http.StatusCreated), "view")["id"].(string)
	viewed := dispatchEvent{Entity: "view", ID: view, Project: "EVW"}
	commit("a view's create", viewed)
	write("/api/dispatch/views/"+view+"/edit", map[string]any{"kind": "board"}, http.StatusOK)
	commit("a view's edit", viewed)
	write("/api/dispatch/views/"+view+"/delete", map[string]any{}, http.StatusOK)
	commit("a view's delete", viewed)
	every := entityOf(t, write("/api/dispatch/views", map[string]any{"name": "Every", "kind": "list", "query": map[string]any{}}, http.StatusCreated), "view")["id"].(string)
	commit("a view of every project", dispatchEvent{Entity: "view", ID: every})

	// A refusal, kept or not, sends nothing.
	e.expect(t, "POST", "/api/dispatch/tickets/EVW-9/comments", map[string]any{"opId": opID(), "body": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/projects/EVW/labels", map[string]any{"opId": opID(), "name": "x", "color": "plaid"}, http.StatusConflict, "invalid")
	e.expect(t, "POST", "/api/dispatch/views/"+view+"/edit", map[string]any{"opId": opID(), "name": "x"}, http.StatusNotFound, "not_found")
	e.expect(t, "POST", "/api/dispatch/tickets/EVW-1/links/nope/delete", map[string]any{"opId": opID()}, http.StatusBadRequest, "invalid_request")
	if len(queued) != 0 {
		t.Fatalf("a refusal sent %d entries", len(queued))
	}
	// The live stream carries each event in order, no other dispatch event
	// between them.
	for _, w := range streamed {
		found := false
		for ev := range stream {
			if ev == w {
				found = true
				break
			}
			if strings.HasPrefix(ev, "dispatch ") {
				t.Fatalf("the stream sent %s before %s", ev, w)
			}
		}
		if !found {
			t.Fatalf("the stream ended before %s", w)
		}
	}
}

// A commit that changed more entities than a client's queue holds
// entries reaches every client as one entry and leaves its stream open:
// a label and then a milestone deleted through their routes, each from
// more tickets than eventQueue. A client that reads nothing while a
// delete commits holds it as one entry, the label's (or the milestone's)
// event and one for the tickets it left, more than maxDispatchEach of
// them, naming no ticket; a live GET /api/events stream carries those,
// stays open through both, and carries the next commit's. An entry per
// change dropped the client that read nothing at the 257th, and the live
// stream too unless its handler wrote out enough of them while the
// commit queued them.
func TestDispatchALargeCommitKeepsTheStreamOpen(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	ds, done, ctx := e.srv.dispatch, doneOf(t), context.Background()
	done(ds.CreateProject(ctx, adminOp("Tester"), dispatch.NewProject{Key: "BIG", Name: "Big"}))
	label := done(ds.CreateLabel(ctx, adminOp("Tester"), "BIG", dispatch.NewLabel{Name: "everywhere", Color: "teal"})).ID
	milestone := done(ds.CreateMilestone(ctx, adminOp("Tester"), "BIG", dispatch.NewMilestone{Name: "Everything"})).ID
	var tickets []string
	for i := range eventQueue + 1 {
		tickets = append(tickets, done(ds.CreateTicket(ctx, adminOp("Tester"), dispatch.NewTicket{
			Project: "BIG", Title: fmt.Sprintf("Ticket %d", i+1), Labels: []string{label}, Milestone: milestone,
		})).ID)
	}
	stream := e.eventStream(t)
	queued := e.srv.events.subscribe()
	defer e.srv.events.unsubscribe(queued)
	// next is the next dispatch event on the live stream.
	next := func(what string) dispatchEvent {
		t.Helper()
		for ev := range stream {
			data, ok := strings.CutPrefix(ev, "dispatch ")
			if !ok {
				continue
			}
			var got dispatchEvent
			if err := json.Unmarshal([]byte(data), &got); err != nil {
				t.Fatalf("%s: %s: %v", what, data, err)
			}
			return got
		}
		t.Fatalf("the stream ended before %s", what)
		return dispatchEvent{}
	}
	var seq int64
	for _, del := range []struct{ entity, id string }{{"label", label}, {"milestone", milestone}} {
		e.expect(t, "POST", "/api/dispatch/projects/BIG/"+del.entity+"s/"+del.id+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
		// Its events, queued before the reply: its own, then one for the
		// tickets it left, naming no ticket, with the commit's seq.
		if len(queued) != 1 {
			t.Fatalf("the %s's delete queued %d entries, want 1", del.entity, len(queued))
		}
		var want []dispatchEvent
		for _, data := range dispatchData(t, <-queued) {
			var ev dispatchEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			want = append(want, ev)
		}
		if len(want) != 2 || want[0].Entity != del.entity || want[0].ID != del.id || want[0].Ticket != "" || want[0].Project != "BIG" ||
			want[0].Seq <= seq || want[1] != (dispatchEvent{Seq: want[0].Seq, Entity: "ticket", Project: "BIG"}) {
			t.Fatalf("the %s's delete: %+v, want its event and one for its tickets", del.entity, want)
		}
		seq = want[0].Seq
		for i, w := range want {
			if ev := next(fmt.Sprintf("the %s's delete's event %d", del.entity, i)); ev != w {
				t.Fatalf("the stream's event %d of the %s's delete: %+v, want %+v", i, del.entity, ev, w)
			}
		}
	}
	select {
	case _, open := <-queued:
		if !open {
			t.Fatal("the client that read nothing was dropped")
		}
		t.Fatal("an entry more than the deletes'")
	default:
	}
	e.expect(t, "POST", "/api/dispatch/tickets/BIG-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": "After"}}, http.StatusOK, "")
	if ev := next("the edit after the deletes"); ev.Entity != "ticket" || ev.ID != tickets[0] || ev.Seq <= seq {
		t.Fatalf("the edit's event: %+v", ev)
	}
	// Every ticket left both.
	for _, key := range []string{"BIG-1", fmt.Sprintf("BIG-%d", len(tickets))} {
		if tk := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+key, nil, http.StatusOK, ""), "ticket"); len(tk["labels"].([]any)) != 0 || tk["milestoneId"] != "" {
			t.Fatalf("%s after the deletes: %v", key, tk)
		}
	}
}

// An attempt's ticket (PR-12): the session of a run's member reads the
// ticket its run is linked to and comments on it, with its own token or
// the admin's, as agent:<member> (dispatch_session.go).

// callAs sends body as call does (JSON with HTML escaping off, or the
// bytes given as they are) with the token given, none for "", and
// returns the reply's status, its bytes and the reply decoded.
func (e *testEnv) callAs(t *testing.T, token, method, path string, body any) (int, []byte, map[string]any) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		rd = bytes.NewReader(encodeNoHTML(t, body))
	}
	req, err := http.NewRequest(method, e.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%s %s: the reply is not JSON: %v: %.200s", method, path, err, data)
		}
	}
	return resp.StatusCode, data, out
}

// expectAs is expect with the token given.
func (e *testEnv) expectAs(t *testing.T, token, method, path string, body any, status int, code string) map[string]any {
	t.Helper()
	got, data, out := e.callAs(t, token, method, path, body)
	if got != status || (code != "" && errorCode(out) != code) {
		t.Fatalf("%s %s: %d %.300s, want %d %s", method, path, got, data, status, code)
	}
	return out
}

// memberSession launches a crew of one member, a, running cat, and gives
// its session a known agent token: the run, the member's session and its
// token. The member's session is the run's once the engine records it,
// which the launch's reply waits for; it is looked for until it is, for
// as long as the test may take. The run is stopped when the test ends.
func (e *testEnv) memberSession(t *testing.T, name string) (run, sid, token string) {
	t.Helper()
	run = e.launchCrew(t, name, catMember("a", "immediately"))
	t.Cleanup(func() { e.do("POST", "/api/runs/"+run+"/stop", adminToken, nil) })
	for sid == "" {
		if r, ok := e.srv.runs.Get(run); ok && len(r.Members) == 1 {
			sid = r.Members[0].SessionID
		}
		if sid == "" {
			if t.Context().Err() != nil {
				t.Fatal("the member's session never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return run, sid, e.agentToken(sid)
}

// The session of a run's member reads the ticket its run is linked to,
// with its own token or the admin's, and no other: 404 no_ticket before
// any link, for a session in no run and after the ticket's delete, 401
// for a token that is not the session's or the admin's. The read is the
// ticket as the admin's read gives it, with its labels' and its
// milestone's rows. The comment is the member's, agent:a, whichever token
// sent it, kept under that actor and not the admin's: sent again it
// answers its receipt and changes nothing, with other content
// op_conflict. Once the run is linked to a second ticket both routes name
// the second, and still do after the first's undelete.
func TestDispatchSessionTicketRoutes(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	// Every refusal is the token's: the per-client limiter would answer
	// some with 429.
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	run, sid, tok := e.memberSession(t, "Ticketed")
	read, comment := "/api/sessions/"+sid+"/ticket", "/api/sessions/"+sid+"/ticket/comments"
	for _, token := range []string{tok, adminToken} {
		e.expectAs(t, token, "GET", read, nil, http.StatusNotFound, "no_ticket")
		e.expectAs(t, token, "POST", comment, map[string]any{"opId": opID(), "body": "early"}, http.StatusNotFound, "no_ticket")
	}
	// A session in no run has no ticket either; its token is no other
	// session's, and neither is a share token of this one.
	other := e.createSession("cat")
	otherTok := e.agentToken(other)
	e.expectAs(t, otherTok, "GET", "/api/sessions/"+other+"/ticket", nil, http.StatusNotFound, "no_ticket")
	e.expectAs(t, otherTok, "POST", "/api/sessions/"+other+"/ticket/comments", map[string]any{"opId": opID(), "body": "x"}, http.StatusNotFound, "no_ticket")
	_, share, err := e.srv.links.Create(sid, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "wrong", "test-host-token", otherTok, share} {
		e.expectAs(t, token, "GET", read, nil, http.StatusUnauthorized, "unauthorized")
		e.expectAs(t, token, "POST", comment, map[string]any{"opId": opID(), "body": "x"}, http.StatusUnauthorized, "unauthorized")
	}
	e.expectAs(t, adminToken, "GET", "/api/sessions/nobody/ticket", nil, http.StatusNotFound, "not_found")

	e.newDispatchProject(t, "TKT", nil)
	label := entityOf(t, e.expect(t, "POST", "/api/dispatch/projects/TKT/labels", map[string]any{"opId": opID(), "name": "api", "color": "blue"}, http.StatusCreated, ""), "label")["id"].(string)
	e.expect(t, "POST", "/api/dispatch/projects/TKT/labels", map[string]any{"opId": opID(), "name": "unused", "color": "red"}, http.StatusCreated, "")
	milestone := entityOf(t, e.expect(t, "POST", "/api/dispatch/projects/TKT/milestones", map[string]any{"opId": opID(), "name": "1.0"}, http.StatusCreated, ""), "milestone")["id"].(string)
	first := e.newDispatchTicket(t, "TKT", "First", map[string]any{"description": "the <b>work</b>", "labels": []string{label}, "milestone": milestone})["key"].(string)
	second := e.newDispatchTicket(t, "TKT", "Second", nil)["key"].(string)
	e.expect(t, "POST", "/api/dispatch/tickets/"+first+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
	for _, token := range []string{tok, adminToken} {
		out := e.expectAs(t, token, "GET", read, nil, http.StatusOK, "")
		tk := entityOf(t, out, "ticket")
		labels, _ := out["labels"].([]any)
		ms, _ := out["milestone"].(map[string]any)
		if tk["key"] != first || tk["description"] != "the <b>work</b>" || len(labels) != 1 || labels[0].(map[string]any)["name"] != "api" || ms["name"] != "1.0" {
			t.Fatalf("the session's ticket: %v", out)
		}
	}
	e.expectAs(t, tok, "GET", read+"?page=1", nil, http.StatusBadRequest, "invalid_request")

	op := opID()
	out := e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "seen <b>"}, http.StatusCreated, "")
	if rc, c := entityOf(t, out, "receipt"), entityOf(t, out, "comment"); rc["status"] != "done" || rc["intent"] != "comment.add" ||
		c["author"] != "agent:a" || c["by"] != "" || c["body"] != "seen <b>" || c["n"] != 1.0 {
		t.Fatalf("the member's comment: %v", out)
	}
	if rc := entityOf(t, e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "seen <b>"}, http.StatusOK, ""), "receipt"); rc["repeat"] != true {
		t.Fatalf("the comment sent again: %v", rc)
	}
	e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "other"}, http.StatusConflict, "op_conflict")
	// The admin's token comments through the route as the member: the
	// route is the session's.
	if c := entityOf(t, e.expectAs(t, adminToken, "POST", comment, map[string]any{"opId": opID(), "body": "through the session"}, http.StatusCreated, ""), "comment"); c["author"] != "agent:a" || c["n"] != 2.0 {
		t.Fatalf("a comment through the session with the admin's token: %v", c)
	}
	// The member's receipts are its own: the admin's are not among them.
	e.expect(t, "GET", "/api/dispatch/ops/"+op, nil, http.StatusNotFound, "not_found")
	if rc, found, err := e.srv.dispatch.Receipt(context.Background(), "agent:a", op); err != nil || !found || rc.Status != dispatch.StatusDone {
		t.Fatalf("the member's receipt: %+v %v %v", rc, found, err)
	}
	if cs := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+first, nil, http.StatusOK, ""), "ticket")["comments"].([]any); len(cs) != 2 ||
		cs[0].(map[string]any)["author"] != "agent:a" || cs[1].(map[string]any)["author"] != "agent:a" {
		t.Fatalf("the ticket's comments: %v", cs)
	}
	// What the route will not take, and the store's refusal of an empty
	// comment, kept as a failed receipt.
	for _, b := range []any{
		map[string]any{"body": "no op id"},
		map[string]any{"opId": "not one!", "body": "x"},
		map[string]any{"opId": opID(), "body": "x", "byName": "Alice"},
		map[string]any{"opId": opID(), "body": "x", "expectedRev": 1},
		[]byte("not json"),
	} {
		e.expectAs(t, tok, "POST", comment, b, http.StatusBadRequest, "invalid_request")
	}
	e.expectAs(t, tok, "POST", comment, map[string]any{"opId": opID(), "body": ""}, http.StatusConflict, "invalid")
	e.expectAs(t, tok, "POST", comment, map[string]any{"opId": opID(), "body": strings.Repeat("x", maxBody)}, http.StatusRequestEntityTooLarge, "bound")

	// Deleted, the ticket holds the run no more.
	e.expect(t, "POST", "/api/dispatch/tickets/"+first+"/delete", map[string]any{"opId": opID()}, http.StatusOK, "")
	for _, token := range []string{tok, adminToken} {
		e.expectAs(t, token, "GET", read, nil, http.StatusNotFound, "no_ticket")
		e.expectAs(t, token, "POST", comment, map[string]any{"opId": opID(), "body": "after the delete"}, http.StatusNotFound, "no_ticket")
	}
	// The second holds it, and still does once the first is back.
	e.expect(t, "POST", "/api/dispatch/tickets/"+second+"/links", map[string]any{"opId": opID(), "kind": "run", "ref": run}, http.StatusCreated, "")
	for _, when := range []string{"linked to the second", "after the first's undelete"} {
		if when != "linked to the second" {
			e.expect(t, "POST", "/api/dispatch/tickets/"+first+"/undelete", map[string]any{"opId": opID()}, http.StatusOK, "")
		}
		if tk := entityOf(t, e.expectAs(t, tok, "GET", read, nil, http.StatusOK, ""), "ticket"); tk["key"] != second {
			t.Fatalf("%s: the session's ticket is %v", when, tk["key"])
		}
	}
	if c := entityOf(t, e.expectAs(t, tok, "POST", comment, map[string]any{"opId": opID(), "body": "on the second"}, http.StatusCreated, ""), "comment"); c["n"] != 1.0 {
		t.Fatalf("the comment on the second: %v", c)
	}
	if cs := entityOf(t, e.expect(t, "GET", "/api/dispatch/tickets/"+second, nil, http.StatusOK, ""), "ticket")["comments"].([]any); len(cs) != 1 {
		t.Fatalf("the second's comments: %v", cs)
	}
}

// The ticket a session reads is live and holds the run as its own read
// shows it. A link moved to another ticket between the lookup and the
// read sends the lookup round again and answers the other ticket; the
// ticket deleted in that gap answers no_ticket; a link that moves on
// every look answers no_ticket after ticketReads lookups. Not parallel:
// the seam is the package's.
func TestDispatchSessionTicketIsReadWhereItsLinkIs(t *testing.T) {
	e := dispatchEnv(t)
	ctx := context.Background()
	done := doneOf(t)
	ds := e.srv.dispatch
	run, sid, tok := e.memberSession(t, "Moving")
	e.newDispatchProject(t, "MOV", nil)
	a := e.newDispatchTicket(t, "MOV", "A", nil)["key"].(string)
	b := e.newDispatchTicket(t, "MOV", "B", nil)["key"].(string)
	held := done(ds.AddLink(ctx, adminOp(""), a, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run})).ID
	holder := a
	read := "/api/sessions/" + sid + "/ticket"
	t.Cleanup(func() { betweenTicketReads.Store(nil) })
	var calls atomic.Int32
	// move moves the run's link from its holder to the other ticket; it
	// runs in the handlers, so it reports with Errorf, and holds mu over
	// holder and held, which the handlers of several requests share.
	var mu sync.Mutex
	move := func() {
		mu.Lock()
		defer mu.Unlock()
		next := a
		if holder == a {
			next = b
		}
		if rc, err := ds.RemoveLink(ctx, adminOp(""), holder, held); err != nil || rc.Status != dispatch.StatusDone {
			t.Errorf("the link's remove: %+v %v", rc, err)
		}
		rc, err := ds.AddLink(ctx, adminOp(""), next, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run})
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Errorf("the link's add: %+v %v", rc, err)
		}
		holder, held = next, rc.ID
	}
	seam := func(f func()) {
		calls.Store(0)
		betweenTicketReads.Store(&f)
	}

	seam(func() {
		if calls.Add(1) == 1 {
			move()
		}
	})
	if tk := entityOf(t, e.expectAs(t, tok, "GET", read, nil, http.StatusOK, ""), "ticket"); tk["key"] != b || calls.Load() != 2 {
		t.Fatalf("the link moved between the reads: %v after %d lookups", tk["key"], calls.Load())
	}

	seam(func() {
		if calls.Add(1) == 1 {
			if rc, err := ds.DeleteTicket(ctx, adminOp(""), b); err != nil || rc.Status != dispatch.StatusDone {
				t.Errorf("the delete: %+v %v", rc, err)
			}
		}
	})
	e.expectAs(t, tok, "GET", read, nil, http.StatusNotFound, "no_ticket")
	if n := calls.Load(); n != 1 {
		t.Fatalf("the ticket deleted between the reads: %d lookups", n)
	}

	betweenTicketReads.Store(nil)
	done(ds.UndeleteTicket(ctx, adminOp(""), b))
	if tk := entityOf(t, e.expectAs(t, tok, "GET", read, nil, http.StatusOK, ""), "ticket"); tk["key"] != b {
		t.Fatalf("after the undelete: %v", tk["key"])
	}
	seam(func() {
		calls.Add(1)
		move()
	})
	e.expectAs(t, tok, "GET", read, nil, http.StatusNotFound, "no_ticket")
	if n := calls.Load(); n != ticketReads {
		t.Fatalf("a link that moves on every look: %d lookups, want %d", n, ticketReads)
	}
}

// A comment's resend goes where the comment went, not where the run's
// link is now: after the link moved to another ticket, after it was
// removed, and after the first ticket's delete, the same opId and body
// answer the receipt, a repeat, with nothing written to either ticket,
// and other content under it op_conflict. A refused comment's resend
// answers its refusal again, though the run's ticket changed since, and
// its opId with another body op_conflict, before the link moved and
// after.
func TestDispatchSessionCommentResendKeepsItsTicket(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	ctx := context.Background()
	done := doneOf(t)
	ds := e.srv.dispatch
	run, sid, tok := e.memberSession(t, "Resent")
	comment := "/api/sessions/" + sid + "/ticket/comments"
	e.newDispatchProject(t, "RSN", nil)
	a := e.newDispatchTicket(t, "RSN", "A", nil)["key"].(string)
	b := e.newDispatchTicket(t, "RSN", "B", nil)["key"].(string)
	link := done(ds.AddLink(ctx, adminOp(""), a, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run})).ID
	op, refused := opID(), opID()
	if c := entityOf(t, e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "on A"}, http.StatusCreated, ""), "comment"); c["n"] != 1.0 {
		t.Fatalf("the first comment: %v", c)
	}
	e.expectAs(t, tok, "POST", comment, map[string]any{"opId": refused, "body": ""}, http.StatusConflict, "invalid")
	e.expectAs(t, tok, "POST", comment, map[string]any{"opId": refused, "body": "now with words"}, http.StatusConflict, "op_conflict")
	comments := func(key string) int {
		t.Helper()
		cs, err := ds.Comments(ctx, key, 0, dispatch.MaxCommentPage)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	resend := func(when string) {
		t.Helper()
		out := e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "on A"}, http.StatusOK, "")
		if rc := entityOf(t, out, "receipt"); rc["status"] != "done" || rc["repeat"] != true {
			t.Fatalf("%s: the resend: %v", when, out)
		}
		if c, _ := out["comment"].(map[string]any); c["body"] != "on A" || c["n"] != 1.0 {
			t.Fatalf("%s: the resend's comment: %v", when, out)
		}
		e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "other"}, http.StatusConflict, "op_conflict")
		if out := e.expectAs(t, tok, "POST", comment, map[string]any{"opId": refused, "body": ""}, http.StatusConflict, "invalid"); entityOf(t, out, "receipt")["repeat"] != true {
			t.Fatalf("%s: the refusal's resend: %v", when, out)
		}
		e.expectAs(t, tok, "POST", comment, map[string]any{"opId": refused, "body": "now with words"}, http.StatusConflict, "op_conflict")
		if na, nb := comments(a), comments(b); na != 1 || nb != 0 {
			t.Fatalf("%s: %d comments on A and %d on B", when, na, nb)
		}
	}
	done(ds.RemoveLink(ctx, adminOp(""), a, link))
	link = done(ds.AddLink(ctx, adminOp(""), b, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run})).ID
	resend("the link moved to B")
	done(ds.RemoveLink(ctx, adminOp(""), b, link))
	resend("the link removed")
	done(ds.DeleteTicket(ctx, adminOp(""), a))
	resend("A deleted")
}

// A session's comment that commits is answered by its receipt though
// nothing can be read after the commit: the store closed from OnChange,
// after the comment's commit and before the reply looks up the ticket it
// went to, the reply is 201 with the done receipt and a null comment,
// never an error a client would send again under a new opId.
func TestDispatchSessionCommentKeepsItsReceiptWhenTheReadAfterFails(t *testing.T) {
	t.Parallel()
	e := dispatchEnv(t)
	ctx := context.Background()
	done := doneOf(t)
	ds := e.srv.dispatch
	run, sid, tok := e.memberSession(t, "Unread")
	e.newDispatchProject(t, "URD", nil)
	key := e.newDispatchTicket(t, "URD", "A", nil)["key"].(string)
	done(ds.AddLink(ctx, adminOp(""), key, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}))
	var closed atomic.Bool
	ds.OnChange(func([]dispatch.Change) {
		if closed.CompareAndSwap(false, true) {
			if err := ds.Close(); err != nil {
				t.Errorf("closing the store: %v", err)
			}
		}
	})
	out := e.expectAs(t, tok, "POST", "/api/sessions/"+sid+"/ticket/comments", map[string]any{"opId": opID(), "body": "seen"}, http.StatusCreated, "")
	if rc := entityOf(t, out, "receipt"); rc["status"] != "done" || rc["id"] == "" || rc["id"] == nil {
		t.Fatalf("the receipt: %v", out)
	}
	if c, ok := out["comment"]; !ok || c != nil {
		t.Fatalf("the comment, unreadable: %v", out)
	}
	if !closed.Load() {
		t.Fatal("no commit closed the store")
	}
}

// A comment and its resend that overlap, the resend sent before the
// comment commits, answer as one operation whatever the run's link does
// in between: the resend held after its run is found, the comment
// committed on A, the link moved to B (or removed), the resend answers
// the comment's receipt, a repeat with the comment on A, and nothing is
// written to B; another body under the id meanwhile answers op_conflict,
// never no_ticket or a comment on B. Not parallel:
// the seam is the package's.
func TestDispatchSessionCommentOverlappingItsResend(t *testing.T) {
	e := dispatchEnv(t)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	ctx := context.Background()
	done := doneOf(t)
	ds := e.srv.dispatch
	run, sid, tok := e.memberSession(t, "Overlapping")
	comment := "/api/sessions/" + sid + "/ticket/comments"
	e.newDispatchProject(t, "OVL", nil)
	a := e.newDispatchTicket(t, "OVL", "A", nil)["key"].(string)
	b := e.newDispatchTicket(t, "OVL", "B", nil)["key"].(string)
	t.Cleanup(func() { beforeSessionComment.Store(nil) })
	comments := func(key string) int {
		t.Helper()
		cs, err := ds.Comments(ctx, key, 0, dispatch.MaxCommentPage)
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	// post sends a comment from a goroutine of its own, the test's
	// helpers being the test goroutine's: its status and reply, or the
	// error that kept it from one.
	type reply struct {
		status int
		out    map[string]any
		err    error
	}
	post := func(body map[string]any) <-chan reply {
		ch := make(chan reply, 1)
		go func() {
			var r reply
			defer func() { ch <- r }()
			raw, err := json.Marshal(body)
			if err != nil {
				r.err = err
				return
			}
			req, err := http.NewRequest("POST", e.http.URL+comment, bytes.NewReader(raw))
			if err != nil {
				r.err = err
				return
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			resp, err := e.client.Do(req)
			if err != nil {
				r.err = err
				return
			}
			defer resp.Body.Close()
			r.status = resp.StatusCode
			r.err = json.NewDecoder(resp.Body).Decode(&r.out)
		}()
		return ch
	}
	for i, then := range []string{"moved to B", "removed"} {
		link := done(ds.AddLink(ctx, adminOp(""), a, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run})).ID
		op, body := opID(), fmt.Sprintf("overlapping %d", i)
		// The seam holds the first request to reach it, the resend, until
		// released, and lets the rest by.
		var calls atomic.Int32
		arrived, release := make(chan struct{}), make(chan struct{})
		f := func() {
			if calls.Add(1) == 1 {
				close(arrived)
				<-release
			}
		}
		beforeSessionComment.Store(&f)
		resend := post(map[string]any{"opId": op, "body": body})
		<-arrived
		if c := entityOf(t, e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": body}, http.StatusCreated, ""), "comment"); c["body"] != body {
			t.Fatalf("%s: the comment: %v", then, c)
		}
		done(ds.RemoveLink(ctx, adminOp(""), a, link))
		if then == "moved to B" {
			done(ds.AddLink(ctx, adminOp(""), b, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run}))
		}
		e.expectAs(t, tok, "POST", comment, map[string]any{"opId": op, "body": "other"}, http.StatusConflict, "op_conflict")
		close(release)
		r := <-resend
		if r.err != nil || r.status != http.StatusOK {
			t.Fatalf("%s: the resend: %d %v %v", then, r.status, r.out, r.err)
		}
		if rc, _ := r.out["receipt"].(map[string]any); rc["status"] != "done" || rc["repeat"] != true {
			t.Fatalf("%s: the resend's receipt: %v", then, r.out)
		}
		if c, _ := r.out["comment"].(map[string]any); c["body"] != body {
			t.Fatalf("%s: the resend's comment: %v", then, r.out)
		}
		beforeSessionComment.Store(nil)
		if na, nb := comments(a), comments(b); na != i+1 || nb != 0 {
			t.Fatalf("%s: %d comments on A and %d on B", then, na, nb)
		}
		if then == "moved to B" {
			// Back to A for the next round: B's link removed.
			tk, err := ds.Ticket(ctx, b)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range tk.Links {
				if l.Kind == dispatch.LinkRun {
					done(ds.RemoveLink(ctx, adminOp(""), b, l.ID))
				}
			}
		}
	}
}

// sessionReadOfTheLargest reads the largest ticket through its session's
// route: the ticket's run link in place of its last external link (a
// ticket holds fifty), its milestone's description at its bound, the
// reply read with the member's token whole and under MaxTicketReply,
// beside the ticket's twenty labels and its milestone.
func sessionReadOfTheLargest(t *testing.T, e *testEnv, f filler, want largestTicket) {
	t.Helper()
	done := doneOf(t)
	ctx := context.Background()
	ds := e.srv.dispatch
	run, sid, tok := e.memberSession(t, "Largest")
	tk, err := ds.Ticket(ctx, want.key)
	if err != nil {
		t.Fatal(err)
	}
	last := tk.Links[len(tk.Links)-1]
	done(ds.RemoveLink(ctx, adminOp(want.by), want.key, last.ID))
	done(ds.AddLink(ctx, adminOp(want.by), want.key, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: run, URL: last.URL, Note: last.Note}))
	project, _, _ := dispatch.ParseKey(want.key)
	description := f.text(dispatch.MaxMilestoneDescription, 0)
	done(ds.EditMilestone(ctx, adminOp(want.by), project, tk.MilestoneID, dispatch.MilestoneEdit{Description: &description}))
	status, raw, _ := e.callAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil)
	if status != http.StatusOK {
		t.Fatalf("the session's read: %d %.300s", status, raw)
	}
	t.Logf("the largest ticket through its session, %s: %d bytes of %d", f.name, len(raw), MaxTicketReply)
	if len(raw) >= MaxTicketReply {
		t.Fatalf("the largest ticket's reply through its session is %d bytes, not under %d", len(raw), MaxTicketReply)
	}
	for _, esc := range []string{"u003c", "u003e"} {
		if f == angleFiller && bytes.Contains(raw, []byte(`\`+esc)) {
			t.Fatalf("the session's reply holds the escape of %s", esc)
		}
	}
	var out sessionTicketReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	v := out.Ticket
	if v.Key != want.key || v.Description != want.description || v.Acceptance != want.acceptance || len(v.Links) != dispatch.MaxLinks ||
		len(v.Comments) != len(want.comments) || len(v.Relations) != dispatch.MaxRelations || len(v.Attachments) != len(want.attachments) {
		t.Fatalf("the ticket through its session did not read back whole: %d links, %d comments, %d relations, %d attachments",
			len(v.Links), len(v.Comments), len(v.Relations), len(v.Attachments))
	}
	for i, c := range v.Comments {
		if c.Body != want.comments[i] {
			t.Fatalf("comment %d through the session did not read back whole", i+1)
		}
	}
	if len(out.Labels) != dispatch.MaxTicketLabels || out.Milestone == nil || out.Milestone.Description != description {
		t.Fatalf("the ticket's labels and milestone through its session: %d labels, %+v", len(out.Labels), out.Milestone)
	}
	for _, l := range out.Labels {
		if utf8.RuneCountInString(l.Name) != dispatch.MaxLabelName || !slices.Contains(v.Labels, l.ID) {
			t.Fatalf("a label through the session: %+v", l)
		}
	}
}

// A switchyard keeps no tickets: the session's two routes answer 403
// switchyard, as every Dispatch route does.
func TestDispatchSessionTicketOnASwitchyard(t *testing.T) {
	t.Parallel()
	e := switchyardEnv(t, true)
	for _, r := range []struct{ method, path string }{{"GET", "/api/sessions/s1/ticket"}, {"POST", "/api/sessions/s1/ticket/comments"}} {
		if resp, out := e.do(r.method, r.path, adminToken, map[string]any{"opId": "a1", "body": "x"}); resp.StatusCode != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Errorf("%s %s: %d %v", r.method, r.path, resp.StatusCode, out)
		}
	}
}
