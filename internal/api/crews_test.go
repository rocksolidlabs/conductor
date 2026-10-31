package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// crewBody is a crew as the web client sends it: two members of the test
// catalog, the second starting once the first is done.
func (e *testEnv) crewBody(name string) map[string]any {
	return map[string]any{
		"name": name, "goal": "ship /v1/users", "cwd": e.root, "where": "server", "isolation": "worktree",
		"openAfterLaunch": true, "viewLinkTtlSeconds": 28800,
		"members": []any{
			map[string]any{"name": "lead", "agentId": "sh", "prompt": "Own the plan for $GOAL.", "args": []any{"-i"}, "start": map[string]any{"when": "immediately"}},
			map[string]any{"name": "tests", "agentId": "cat", "prompt": "Write the tests.", "start": map[string]any{"when": "after", "member": "lead"}},
		},
	}
}

// crewMember returns member i of a crew, as sent or as received.
func crewMember(c map[string]any, i int) map[string]any {
	return c["members"].([]any)[i].(map[string]any)
}

// crews returns the summaries GET /api/crews lists on one page of 500, in
// order; total must count them.
func (e *testEnv) crews() []map[string]any {
	e.t.Helper()
	resp, out := e.do("GET", "/api/crews?limit=500", adminToken, nil)
	raw, ok := out["crews"].([]any)
	if resp.StatusCode != http.StatusOK || !ok || out["total"] != float64(len(raw)) {
		e.t.Fatalf("crews: %d %v", resp.StatusCode, out)
	}
	list := []map[string]any{}
	for _, c := range raw {
		list = append(list, c.(map[string]any))
	}
	return list
}

// crew returns the crew GET /api/crews/{id} answers in full.
func (e *testEnv) crew(id string) map[string]any {
	e.t.Helper()
	resp, out := e.do("GET", "/api/crews/"+id, adminToken, nil)
	c, _ := out["crew"].(map[string]any)
	if resp.StatusCode != http.StatusOK || c == nil {
		e.t.Fatalf("crew %s: %d %v", id, resp.StatusCode, out)
	}
	return c
}

// crewIDs returns the IDs GET /api/crews lists, in order.
func (e *testEnv) crewIDs() []string {
	e.t.Helper()
	ids := []string{}
	for _, c := range e.crews() {
		ids = append(ids, c["id"].(string))
	}
	return ids
}

// sendCrew sends body to a crew route as the admin, requires status, and
// returns the crew in the reply.
func (e *testEnv) sendCrew(method, path string, body any, status int) map[string]any {
	e.t.Helper()
	resp, out := e.do(method, path, adminToken, body)
	c, _ := out["crew"].(map[string]any)
	if resp.StatusCode != status || c == nil {
		e.t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, out)
	}
	return c
}

// storedCrew returns the crew with the given ID as its file in the data
// directory holds it, or nil when there is none.
func (e *testEnv) storedCrew(id string) map[string]any {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "crews", id+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(b, &c); err != nil {
		e.t.Fatalf("%s.json: %v %s", id, err, b)
	}
	return c
}

// wantAPIError requires an error reply with status and code whose message
// contains msg.
func wantAPIError(t *testing.T, what string, resp *http.Response, out map[string]any, status int, code, msg string) {
	t.Helper()
	apiErr, _ := out["error"].(map[string]any)
	message, _ := apiErr["message"].(string)
	if resp.StatusCode != status || apiErr["code"] != code || !strings.Contains(message, msg) {
		t.Errorf("%s: %d %v, want %d %s with %q", what, resp.StatusCode, out, status, code, msg)
	}
}

func TestCrewsCRUD(t *testing.T) {
	e := newTestEnv(t, nil)

	// Create: the server derives the ID from the name, trims the name and
	// stamps the times, in UTC.
	created := e.sendCrew("POST", "/api/crews", e.crewBody("  API sweep  "), http.StatusCreated)
	if created["id"] != "api-sweep" || created["name"] != "API sweep" || created["goal"] != "ship /v1/users" || created["cwd"] != e.root ||
		created["where"] != "server" || created["isolation"] != "worktree" || created["openAfterLaunch"] != true || created["viewLinkTtlSeconds"] != 28800.0 {
		t.Fatalf("created %v", created)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, fmt.Sprint(created["createdAt"]))
	if err != nil || time.Since(createdAt) > time.Minute || created["updatedAt"] != created["createdAt"] || !strings.HasSuffix(fmt.Sprint(created["createdAt"]), "Z") {
		t.Fatalf("times %v %v: %v", created["createdAt"], created["updatedAt"], err)
	}
	wantMembers := []any{
		map[string]any{"name": "lead", "agentId": "sh", "prompt": "Own the plan for $GOAL.", "args": []any{"-i"}, "start": map[string]any{"when": "immediately"}},
		map[string]any{"name": "tests", "agentId": "cat", "prompt": "Write the tests.", "start": map[string]any{"when": "after", "member": "lead"}},
	}
	if !reflect.DeepEqual(created["members"], wantMembers) {
		t.Fatalf("members %v", created["members"])
	}
	if again := e.sendCrew("POST", "/api/crews", e.crewBody("API sweep"), http.StatusCreated); again["id"] != "api-sweep-2" {
		t.Fatalf("same name again: %v", again["id"])
	}
	if list := e.crews(); len(list) != 2 || list[0]["id"] != "api-sweep" || list[1]["id"] != "api-sweep-2" || !reflect.DeepEqual(e.crew("api-sweep"), created) {
		t.Fatalf("list %v", list)
	}

	// Update the lead's prompt and rename the crew: the ID and the creation
	// time stay.
	body := e.crewBody("Users sweep")
	crewMember(body, 0)["prompt"] = "New plan for $GOAL."
	updated := e.sendCrew("PUT", "/api/crews/api-sweep", body, http.StatusOK)
	updatedAt, err := time.Parse(time.RFC3339Nano, fmt.Sprint(updated["updatedAt"]))
	if updated["id"] != "api-sweep" || updated["name"] != "Users sweep" || crewMember(updated, 0)["prompt"] != "New plan for $GOAL." ||
		updated["createdAt"] != created["createdAt"] || err != nil || updatedAt.Before(createdAt) || !strings.HasSuffix(fmt.Sprint(updated["updatedAt"]), "Z") {
		t.Fatalf("updated %v", updated)
	}
	if stored := e.storedCrew("api-sweep"); stored == nil || !reflect.DeepEqual(stored, updated) {
		t.Fatalf("the file holds %v", stored)
	}

	// Duplicate: a new crew under <id>-copy.
	dup := e.sendCrew("POST", "/api/crews/api-sweep/duplicate", nil, http.StatusCreated)
	if dup["id"] != "api-sweep-copy" || dup["name"] != "Users sweep copy" || !reflect.DeepEqual(dup["members"], updated["members"]) || dup["cwd"] != e.root {
		t.Fatalf("duplicate %v", dup)
	}
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"api-sweep-2", "api-sweep", "api-sweep-copy"}) { // by name
		t.Fatalf("ids %v", ids)
	}

	// Delete.
	if resp, _ := e.do("DELETE", "/api/crews/api-sweep-copy", adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if e.storedCrew("api-sweep-copy") != nil {
		t.Fatal("deleted crew still has a file")
	}
	resp, out := e.do("DELETE", "/api/crews/api-sweep-copy", adminToken, nil)
	wantAPIError(t, "delete again", resp, out, http.StatusNotFound, "not_found", "")
	resp, out = e.do("PUT", "/api/crews/nope", adminToken, e.crewBody("Nope"))
	wantAPIError(t, "update an unknown crew", resp, out, http.StatusNotFound, "not_found", "")
	resp, out = e.do("POST", "/api/crews/nope/duplicate", adminToken, nil)
	wantAPIError(t, "duplicate an unknown crew", resp, out, http.StatusNotFound, "not_found", "")

	// Invalid crews are refused with the reason.
	bad := e.crewBody("Bad")
	crewMember(bad, 0)["name"] = "Lead!"
	resp, out = e.do("POST", "/api/crews", adminToken, bad)
	wantAPIError(t, "member name Lead!", resp, out, http.StatusBadRequest, "invalid_crew", `"Lead!"`)
	unknown := e.crewBody("Unknown")
	crewMember(unknown, 1)["agentId"] = "nope"
	resp, out = e.do("PUT", "/api/crews/api-sweep", adminToken, unknown)
	wantAPIError(t, "unknown agent", resp, out, http.StatusBadRequest, "invalid_crew", `"nope"`)

	// Nobody but the admin reaches a crew route.
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/crews", nil},
		{"GET", "/api/crews/api-sweep", nil},
		{"POST", "/api/crews", e.crewBody("Intruder")},
		{"PUT", "/api/crews/api-sweep", e.crewBody("Intruder")},
		{"DELETE", "/api/crews/api-sweep", nil},
		{"POST", "/api/crews/api-sweep/duplicate", nil},
	} {
		for _, token := range []string{"", "wrong", "test-host-token"} {
			if resp, _ := e.do(r.method, r.path, token, r.body); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: %d", r.method, r.path, token, resp.StatusCode)
			}
		}
	}

	// A restarted server lists the same crews, unchanged by what was refused.
	live := e.crews()
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"api-sweep-2", "api-sweep"}) || !reflect.DeepEqual(e.crew("api-sweep"), updated) {
		t.Fatalf("after the refusals: %v", live)
	}
	if restarted := e.restart().crews(); !reflect.DeepEqual(live, restarted) {
		t.Fatalf("restart differs:\n live      %v\n restarted %v", live, restarted)
	}
}

func TestCrewSaveRejectsInvalidCrews(t *testing.T) {
	e := newTestEnv(t, nil)
	kept := e.sendCrew("POST", "/api/crews", e.crewBody("Kept"), http.StatusCreated)
	with := func(change func(b map[string]any)) map[string]any {
		b := e.crewBody("Crew")
		change(b)
		return b
	}
	cases := []struct {
		name string
		body any
		code string
		msg  string // part of the message
	}{
		{"member name Lead!", with(func(b map[string]any) { crewMember(b, 0)["name"] = "Lead!" }), "invalid_crew", "must match"},
		{"unknown agent", with(func(b map[string]any) { crewMember(b, 1)["agentId"] = "nope" }), "invalid_crew", `"nope"`},
		{"after a missing member", with(func(b map[string]any) {
			crewMember(b, 1)["start"] = map[string]any{"when": "after", "member": "ghost"}
		}), "invalid_crew", `"ghost"`},
		{"13 members", with(func(b map[string]any) {
			for i := 2; i < 13; i++ {
				b["members"] = append(b["members"].([]any), map[string]any{"name": fmt.Sprintf("m%d", i), "agentId": "cat", "prompt": "", "start": map[string]any{"when": "manual"}})
			}
		}), "invalid_crew", "at most 12"},
		{"where elsewhere", with(func(b map[string]any) { b["where"] = "cloud" }), "invalid_crew", `"cloud"`},
		{"args over 8 KiB in all", with(func(b map[string]any) {
			crewMember(b, 0)["args"] = []any{strings.Repeat("a", 4096), strings.Repeat("b", 4096), "c"}
		}), "invalid_crew", "8192 bytes"},
		{"members starting after each other", with(func(b map[string]any) {
			crewMember(b, 0)["start"] = map[string]any{"when": "after", "member": "tests"}
		}), "invalid_crew", "cycle"},
		{"member name git refuses", with(func(b map[string]any) {
			crewMember(b, 1)["name"] = "tests.lock"
		}), "invalid_crew", "git"},
		{"name with a control character", with(func(b map[string]any) { b["name"] = "API\x1b[2Jsweep" }), "invalid_crew", "control"},
		{"id sent by the client", with(func(b map[string]any) { b["id"] = "mine" }), "invalid_request", `"id"`},
		{"createdAt sent by the client", with(func(b map[string]any) { b["createdAt"] = "2026-09-29T10:00:00Z" }), "invalid_request", `"createdAt"`},
		{"unknown member field", with(func(b map[string]any) { crewMember(b, 0)["model"] = "x" }), "invalid_request", `"model"`},
		{"no body", nil, "invalid_request", ""},
		{"body over 2 MiB", with(func(b map[string]any) { b["goal"] = strings.Repeat("g", maxCrewBody) }), "invalid_request", "too large"},
	}
	for _, tc := range cases {
		for _, path := range []string{"POST /api/crews", "PUT /api/crews/kept"} {
			method, route, _ := strings.Cut(path, " ")
			resp, out := e.do(method, route, adminToken, tc.body)
			wantAPIError(t, tc.name+" ("+path+")", resp, out, http.StatusBadRequest, tc.code, tc.msg)
		}
	}
	if list := e.crews(); len(list) != 1 || list[0]["id"] != "kept" || !reflect.DeepEqual(e.crew("kept"), kept) {
		t.Fatalf("a refused save changed the crews: %v", list)
	}
}

// A value quoted in an error comes back cut short, however long it was sent.
// A crew that breaks a rule is refused for that before its agents are looked
// up and before its id is; an agent the catalog does not have is 400 whatever
// else holds. No crew limit comes after them: a valid crew is created.
func TestCrewErrorsQuoteShortAndComeInOrder(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sendCrew("POST", "/api/crews", e.crewBody("Kept"), http.StatusCreated)
	message := func(out map[string]any) string {
		apiErr, _ := out["error"].(map[string]any)
		msg, _ := apiErr["message"].(string)
		return msg
	}
	huge := e.crewBody("Huge")
	crewMember(huge, 1)["agentId"] = strings.Repeat("a", 900<<10)
	invalid := e.crewBody("Invalid")
	crewMember(invalid, 0)["name"] = "Lead!"
	unknown := e.crewBody("Unknown")
	crewMember(unknown, 1)["agentId"] = "nope"
	both := e.crewBody("Both")
	crewMember(both, 0)["name"] = "Lead!"
	crewMember(both, 1)["agentId"] = "nope"
	for _, path := range []string{"POST /api/crews", "PUT /api/crews/kept", "PUT /api/crews/missing"} {
		method, route, _ := strings.Cut(path, " ")
		resp, out := e.do(method, route, adminToken, huge)
		wantAPIError(t, "a 900 KiB agentId ("+path+")", resp, out, http.StatusBadRequest, "invalid_crew", "agentId")
		if msg := message(out); len(msg) > 300 {
			t.Errorf("%s: a message of %d bytes", path, len(msg))
		}
		resp, out = e.do(method, route, adminToken, both)
		wantAPIError(t, "a rule broken and an unknown agent ("+path+")", resp, out, http.StatusBadRequest, "invalid_crew", "must match")
		resp, out = e.do(method, route, adminToken, invalid)
		wantAPIError(t, "a rule broken ("+path+")", resp, out, http.StatusBadRequest, "invalid_crew", "must match")
		resp, out = e.do(method, route, adminToken, unknown)
		wantAPIError(t, "an unknown agent ("+path+")", resp, out, http.StatusBadRequest, "invalid_crew", `"nope"`)
	}
	e.sendCrew("POST", "/api/crews", e.crewBody("Valid"), http.StatusCreated)
}

// The largest crew the limits allow goes through the crew routes, whose
// bodies may reach 2 MiB (`maxCrewBody`) where others stop at 64 KiB: twelve
// members, each with a prompt of 4000 four-byte and control characters (JSON
// writes a control character as six bytes) and 8 KiB of args, and a goal of
// 2000 four-byte characters.
func TestCrewRoutesTakeTheLargestCrews(t *testing.T) {
	e := newTestEnv(t, nil)
	body := e.crewBody("Largest")
	prompt := strings.Repeat("\U0001F600\x01", 2000)
	var members []any
	for i := range 12 {
		members = append(members, map[string]any{
			"name": fmt.Sprintf("m%02d", i), "agentId": "sh", "prompt": prompt,
			"args":  []any{strings.Repeat("a", 4096), strings.Repeat("b", 4096)},
			"start": map[string]any{"when": "manual"},
		})
	}
	body["members"] = members
	body["goal"] = strings.Repeat("\U0001F600", 2000)
	if b, _ := json.Marshal(body); len(b) < 256<<10 {
		t.Fatalf("the body is only %d bytes", len(b))
	}
	created := e.sendCrew("POST", "/api/crews", body, http.StatusCreated)
	if created["goal"] != body["goal"] || crewMember(created, 11)["prompt"] != prompt {
		t.Fatalf("the crew came back changed: goal %d bytes", len(fmt.Sprint(created["goal"])))
	}
	e.sendCrew("PUT", "/api/crews/largest", body, http.StatusOK)
	// One byte more of args is one byte too many.
	crewMember(body, 0)["args"] = []any{strings.Repeat("a", 4096), strings.Repeat("b", 4096), "c"}
	resp, out := e.do("POST", "/api/crews", adminToken, body)
	wantAPIError(t, "8 KiB and a byte of args", resp, out, http.StatusBadRequest, "invalid_crew", "8192 bytes")
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"largest"}) {
		t.Fatalf("ids %v", ids)
	}
}

// The agents a crew names must be in the catalog when it is saved or copied.
func TestCrewRoutesCheckTheAgents(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sendCrew("POST", "/api/crews", e.crewBody("API sweep"), http.StatusCreated)
	// The tests member runs cat, which the catalog then hides.
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("hide cat: %d", c)
	}
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/crews", e.crewBody("Another")},
		{"PUT", "/api/crews/api-sweep", e.crewBody("API sweep")},
		{"POST", "/api/crews/api-sweep/duplicate", nil},
	} {
		resp, out := e.do(r.method, r.path, adminToken, r.body)
		wantAPIError(t, r.method+" "+r.path, resp, out, http.StatusBadRequest, "invalid_crew", `"cat"`)
	}
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"api-sweep"}) {
		t.Fatalf("ids %v", ids)
	}
}

func TestCrewRoutesNeedAStore(t *testing.T) {
	e := newTestEnv(t, nil)
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ro := e.serve(srv)
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/crews", e.crewBody("Crew")},
		{"PUT", "/api/crews/crew", e.crewBody("Crew")},
		{"DELETE", "/api/crews/crew", nil},
		{"POST", "/api/crews/crew/duplicate", nil},
		{"GET", "/api/crews/crew", nil},
		{"POST", "/api/crews/crew/launch", nil},
		{"POST", "/api/crews/examples", nil},
	} {
		resp, out := ro.do(r.method, r.path, adminToken, r.body)
		wantAPIError(t, r.method+" "+r.path, resp, out, http.StatusServiceUnavailable, "store_unavailable", "")
	}
	// ro.crews requires total to count the list: 0.
	if list := ro.crews(); len(list) != 0 {
		t.Fatalf("crews without a store: %v", list)
	}
}

// There is no limit on the number of crews: the list pages through them, by
// name, with the total, and limit and offset are checked.
func TestCrewRoutesPageThroughMoreThan50Crews(t *testing.T) {
	e := newTestEnv(t, nil)
	for i := range 55 {
		e.sendCrew("POST", "/api/crews", e.crewBody(fmt.Sprintf("Crew %02d", i)), http.StatusCreated)
	}
	e.sendCrew("POST", "/api/crews/crew-00/duplicate", nil, http.StatusCreated)
	resp, out := e.do("GET", "/api/crews?offset=50&limit=4", adminToken, nil)
	page, _ := out["crews"].([]any)
	if resp.StatusCode != http.StatusOK || out["total"] != 56.0 || len(page) != 4 || page[0].(map[string]any)["id"] != "crew-49" {
		t.Fatalf("page: %d %v", resp.StatusCode, out)
	}
	// A summary carries the goal (the Crews page shows it under the name) and the members without their prompts.
	if first := page[0].(map[string]any); first["members"] == nil || first["goal"] == nil || fmt.Sprint(first["members"]) != "[map[agentId:sh name:lead start:map[when:immediately]] map[agentId:cat name:tests start:map[member:lead when:after]]]" {
		t.Fatalf("a summary: %v", first)
	}
	if _, out := e.do("GET", "/api/crews", adminToken, nil); len(out["crews"].([]any)) != 56 || out["total"] != 56.0 {
		t.Fatalf("the default page of 100: %v", out["total"])
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=x", "offset=-1", "offset=1.5"} {
		resp, out := e.do("GET", "/api/crews?"+q, adminToken, nil)
		wantAPIError(t, q, resp, out, http.StatusBadRequest, "invalid_request", "")
	}
}

// A crew file that cannot be used is left out of the list; reading or
// updating it answers 409 crew_unreadable with why, without the data
// directory's path; deleting it works.
func TestCrewRoutesAnswerForACorruptFile(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sendCrew("POST", "/api/crews", e.crewBody("Good"), http.StatusCreated)
	if err := os.WriteFile(filepath.Join(e.srv.store.Dir(), "crews", "broken.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"good"}) {
		t.Fatalf("ids %v", ids)
	}
	for _, r := range []struct {
		method, path string
		body         any
	}{{"GET", "/api/crews/broken", nil}, {"PUT", "/api/crews/broken", e.crewBody("Fixed")}, {"POST", "/api/crews/broken/duplicate", nil}, {"POST", "/api/crews/broken/launch", nil}} {
		resp, out := e.do(r.method, r.path, adminToken, r.body)
		wantAPIError(t, r.method+" "+r.path, resp, out, http.StatusConflict, "crew_unreadable", "broken.json")
		if strings.Contains(fmt.Sprint(out), e.srv.store.Dir()) {
			t.Errorf("%s %s names the data directory: %v", r.method, r.path, out)
		}
	}
	if resp, _ := e.do("DELETE", "/api/crews/broken", adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, out := e.do("GET", "/api/crews/nope", adminToken, nil)
	wantAPIError(t, "unknown", resp, out, http.StatusNotFound, "not_found", "")
}

func TestCrewFailedSaveChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory of mode 0500")
	}
	e := newTestEnv(t, nil)
	e.sendCrew("POST", "/api/crews", e.crewBody("Kept"), http.StatusCreated)
	before, kept := e.crews(), e.crew("kept")
	dir := filepath.Join(e.srv.store.Dir(), "crews")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/crews", e.crewBody("Lost")},
		{"PUT", "/api/crews/kept", e.crewBody("Changed")},
		{"POST", "/api/crews/kept/duplicate", nil},
		{"DELETE", "/api/crews/kept", nil},
	} {
		resp, out := e.do(r.method, r.path, adminToken, r.body)
		wantAPIError(t, r.method+" "+r.path, resp, out, http.StatusInternalServerError, "store_failed", "could not save the crews")
		if strings.Contains(fmt.Sprint(out), e.srv.store.Dir()) {
			t.Errorf("%s %s names the data directory: %v", r.method, r.path, out)
		}
	}
	if after := e.crews(); !reflect.DeepEqual(after, before) || !reflect.DeepEqual(e.crew("kept"), kept) {
		t.Fatalf("a failed save changed the crews:\n before %v\n after  %v", before, after)
	}
}

// A crews.json that cannot be used stops startup, as a bad catalog.json does.
func TestNewRefusesAMalformedCrewsFile(t *testing.T) {
	e := newTestEnv(t, nil)
	path := filepath.Join(e.srv.store.Dir(), "crews.json")
	if err := os.WriteFile(path, []byte(`{"crews": [{"id": "x", "bogus": 1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, e.srv.store)
	if err == nil || srv != nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("New: %v", err)
	}
}

// A corrupt crew file names itself in the startup log, at error level, and
// the server starts with the other crews.
func TestNewLogsACorruptCrewFile(t *testing.T) {
	e := newTestEnv(t, nil)
	e.sendCrew("POST", "/api/crews", e.crewBody("Good"), http.StatusCreated)
	broken := filepath.Join(e.srv.store.Dir(), "crews", "broken.json")
	if err := os.WriteFile(broken, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	srv, err := New(e.srv.cfg, e.srv.base, slog.New(slog.NewTextHandler(&logs, nil)), nil, e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), broken) {
		t.Fatalf("log:\n%s", logs.String())
	}
	if ids := e.serve(srv).crewIDs(); !slices.Equal(ids, []string{"good"}) {
		t.Fatalf("ids %v", ids)
	}
}

// A crew near the bound on its file goes through when a client sends it
// indented: the body may be up to 2 MiB (maxCrewBody), twice the bound, and
// the crew itself is held to crew.MaxEncoded as its file.
func TestCrewRoutesTakeAnIndentedBodyNearTheBound(t *testing.T) {
	e := newTestEnv(t, nil)
	ctl := func(n int) string { return strings.Repeat("\x01", n) } // six bytes each as JSON
	body := e.crewBody("Indented")
	var members []any
	for i := range 12 {
		members = append(members, map[string]any{
			"name": fmt.Sprintf("m%02d", i), "agentId": "sh", "prompt": ctl(4000),
			"args":  []any{ctl(4096), ctl(4096)},
			"start": map[string]any{"when": "manual"},
		})
	}
	body["members"] = members
	body["goal"] = ctl(2000)
	compact, _ := json.Marshal(body)
	raw, err := json.MarshalIndent(body, "", strings.Repeat(" ", 2048))
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) < 800<<10 || len(raw) <= 1<<20 || len(raw) >= maxCrewBody {
		t.Fatalf("compact %d bytes, indented %d", len(compact), len(raw))
	}
	// e.do would marshal the body again, compact: send the bytes as they are.
	req, _ := http.NewRequest("POST", e.http.URL+"/api/crews", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("the indented crew: %d", resp.StatusCode)
	}
	fi, err := os.Stat(filepath.Join(e.srv.store.Dir(), "crews", "indented.json"))
	if err != nil || fi.Size() > crew.MaxEncoded {
		t.Fatalf("its file: %v %v", fi, err)
	}
}

// A crews directory the server cannot read is a read failure and is answered
// as one: the list, and a create, which reads the directory for a free id,
// say "could not read the crews", without the data directory's path.
func TestCrewRoutesSayWhenTheyCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 0300")
	}
	e := newTestEnv(t, nil)
	dir := filepath.Join(e.srv.store.Dir(), "crews")
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	resp, out := e.do("GET", "/api/crews", adminToken, nil)
	wantAPIError(t, "list", resp, out, http.StatusInternalServerError, "store_failed", "could not read the crews")
	resp, out = e.do("POST", "/api/crews", adminToken, e.crewBody("New"))
	wantAPIError(t, "create", resp, out, http.StatusInternalServerError, "store_failed", "could not read the crews")
	if strings.Contains(fmt.Sprint(out), e.srv.store.Dir()) {
		t.Errorf("the reply names the data directory: %v", out)
	}
}

// PUT is the crews' first route of its kind: a dev UI on another origin
// (NUXT_PUBLIC_API_BASE) must be allowed to send it.
func TestDevCORSAllowsPut(t *testing.T) {
	e := newTestEnv(t, nil) // Dev is on
	req, _ := http.NewRequest("OPTIONS", e.http.URL+"/api/crews/x", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	methods := strings.Split(resp.Header.Get("Access-Control-Allow-Methods"), ", ")
	if resp.StatusCode != http.StatusNoContent || !slices.Contains(methods, "PUT") {
		t.Fatalf("preflight: %d %q", resp.StatusCode, methods)
	}
}

// gitRepo makes e.root a git repository with one commit, and skips the test
// when git is not installed. HOME is a temporary directory, so that no
// configuration of the user running the tests applies.
func (e *testEnv) gitRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(e.root, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "README"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", e.root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// runCrewBody is a crew whose lead, an interactive sh, is typed a prompt that
// prints the crew's variables, and whose other member starts by hand.
func (e *testEnv) runCrewBody(isolation string) map[string]any {
	return map[string]any{
		"name": "API sweep", "goal": "ship /v1/users", "cwd": e.root, "where": "server", "isolation": isolation,
		"openAfterLaunch": false,
		"members": []any{
			map[string]any{"name": "lead", "agentId": "sh", "args": []any{"-i"},
				"prompt": `echo "C=$CONDUCTOR_CREW R=$CONDUCTOR_RUN M=$CONDUCTOR_MEMBER G=$GOAL T=${CONDUCTOR_NOTIFY_TOKEN:+set}"`,
				"start":  map[string]any{"when": "immediately"}},
			map[string]any{"name": "tests", "agentId": "sh", "args": []any{"-i"}, "prompt": "echo tests-here",
				"start": map[string]any{"when": "manual"}},
		},
	}
}

// stopEverything stops every session of e's server when the test ends.
func (e *testEnv) stopEverything(t *testing.T) {
	t.Cleanup(func() {
		e.srv.registry.Each(func(d session.Driver) { _ = d.Stop(context.Background()) })
	})
}

// runMember returns a member of a run as a reply carries it.
func runMember(t *testing.T, run map[string]any, name string) map[string]any {
	t.Helper()
	for _, m := range run["members"].([]any) {
		if m := m.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("run has no member %q: %v", name, run)
	return nil
}

// A crew launches as a run of ordinary server sessions: each member in a
// worktree of its own, tagged with the run, with the crew's variables in its
// environment, and its prompt typed once it is ready. The run routes list it,
// report the members' diffs, start a member by hand, add one and stop it all.
func TestCrewRunLifecycle(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	e.stopEverything(t)
	e.sendCrew("POST", "/api/crews", e.runCrewBody("worktree"), http.StatusCreated)

	resp, out := e.do("POST", "/api/crews/api-sweep/launch", adminToken, nil)
	run, _ := out["run"].(map[string]any)
	if resp.StatusCode != http.StatusCreated || run == nil {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	runID, _ := run["id"].(string)
	lead := runMember(t, run, "lead")
	wantPath := filepath.Join(e.root, ".conductor", "worktrees", runID, "lead")
	// The launch answers once the sessions exist; the prompts come as they are ready.
	if !strings.HasPrefix(runID, "api-sweep-") || run["crewId"] != "api-sweep" || lead["status"] != "starting" ||
		lead["worktree"] != wantPath || lead["branch"] != "crew/"+runID+"/lead" || runMember(t, run, "tests")["status"] != "pending" {
		t.Fatalf("run %v", run)
	}
	leadID, _ := lead["sessionId"].(string)
	_, got := e.do("GET", "/api/sessions/"+leadID, adminToken, nil)
	info := got["session"].(map[string]any)
	if !reflect.DeepEqual(info["crew"], map[string]any{"runId": runID, "crewId": "api-sweep", "member": "lead"}) ||
		info["cwd"] != wantPath || info["branch"] != "crew/"+runID+"/lead" || info["name"] != "lead" {
		t.Fatalf("lead's session %v", info)
	}
	c := dialViewer(t, e, leadID, adminToken)
	c.hello(80, 24)
	c.expectOutput("C=api-sweep R=" + runID + " M=lead G=ship /v1/users T=set")
	// The worktrees stay out of the repository's status.
	if out, err := exec.Command("git", "-C", e.root, "status", "--porcelain").CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if d, _ := e.srv.registry.Get(leadID); !slices.ContainsFunc(d.(*session.Local).Activity(), func(a session.ActivityEntry) bool {
		return a.Type == session.ActivityInput && a.ByName == "crew"
	}) {
		t.Fatal("the prompt was not recorded as typed by the crew")
	}

	// Listed, and with diffs.
	_, out = e.do("GET", "/api/runs", adminToken, nil)
	if runs, _ := out["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["id"] != runID {
		t.Fatalf("runs %v", out)
	}
	resp, out = e.do("GET", "/api/runs/"+runID, adminToken, nil)
	if d := runMember(t, out["run"].(map[string]any), "lead")["diff"]; resp.StatusCode != http.StatusOK ||
		!reflect.DeepEqual(d, map[string]any{"added": 0.0, "removed": 0.0}) {
		t.Fatalf("get: %d %v", resp.StatusCode, out)
	}

	// Started by hand, then again: 409.
	resp, out = e.do("POST", "/api/runs/"+runID+"/members/tests/start", adminToken, nil)
	if resp.StatusCode != http.StatusOK || runMember(t, out["run"].(map[string]any), "tests")["status"] != "starting" {
		t.Fatalf("start: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members/tests/start", adminToken, nil)
	wantAPIError(t, "start again", resp, out, http.StatusConflict, "member_started", "")
	resp, out = e.do("POST", "/api/runs/"+runID+"/members/ghost/start", adminToken, nil)
	wantAPIError(t, "start a stranger", resp, out, http.StatusNotFound, "not_found", "")

	// Added mid-run.
	docs := map[string]any{"name": "docs", "agentId": "sh", "prompt": "", "start": map[string]any{"when": "manual"}}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, docs)
	if resp.StatusCode != http.StatusCreated || runMember(t, out["run"].(map[string]any), "docs")["status"] != "pending" {
		t.Fatalf("add: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, docs)
	wantAPIError(t, "add the same name", resp, out, http.StatusBadRequest, "invalid_crew", `"docs"`)
	nope := map[string]any{"name": "nope", "agentId": "nope", "prompt": "", "start": map[string]any{"when": "manual"}}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, nope)
	wantAPIError(t, "add an unknown agent", resp, out, http.StatusBadRequest, "invalid_crew", `"nope"`)
	withArgs := map[string]any{"name": "cats", "agentId": "cat", "args": []any{"-n"}, "prompt": "", "start": map[string]any{"when": "manual"}}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, withArgs)
	wantAPIError(t, "add arguments to an agent that takes none", resp, out, http.StatusBadRequest, "invalid_crew", "arguments")

	// Stopped: every session, and nothing starts any more.
	resp, out = e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["run"].(map[string]any)["stoppedAt"] == nil {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	for _, name := range []string{"lead", "tests"} {
		id := runMember(t, out["run"].(map[string]any), name)["sessionId"].(string)
		if d, _ := e.srv.registry.Get(id); d.Info().Status != session.StatusStopped {
			t.Errorf("%s: %v", name, d.Info().Status)
		}
	}
	resp, out = e.do("POST", "/api/runs/"+runID+"/members/docs/start", adminToken, nil)
	wantAPIError(t, "start in a stopped run", resp, out, http.StatusConflict, "run_stopped", "")
	if _, err := os.Stat(filepath.Join(wantPath, "README")); err != nil {
		t.Fatalf("the worktree went: %v", err)
	}

	for _, path := range []string{"/api/runs/nope", "/api/runs/nope/stop", "/api/runs/nope/members/lead/start"} {
		method := "POST"
		if path == "/api/runs/nope" {
			method = "GET"
		}
		resp, out = e.do(method, path, adminToken, nil)
		wantAPIError(t, path, resp, out, http.StatusNotFound, "not_found", "")
	}
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/crews/api-sweep/launch"}, {"GET", "/api/runs"}, {"GET", "/api/runs/" + runID},
		{"POST", "/api/runs/" + runID + "/members"}, {"POST", "/api/runs/" + runID + "/members/docs/start"}, {"POST", "/api/runs/" + runID + "/stop"},
	} {
		for _, token := range []string{"", "wrong", "test-host-token"} {
			if resp, _ := e.do(r.method, r.path, token, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with %q: %d", r.method, r.path, token, resp.StatusCode)
			}
		}
	}
}

// A crew with a view link lifetime gets a view link at launch: a run link
// whose token is in the launch reply and nowhere else (the run, its links,
// the crews, the log). A crew without one gets none.
func TestCrewLaunchViewLink(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.stopEverything(t)

	plain := e.launchCrew(t, "Plain", catMember("core", "immediately"))
	if _, out := e.do("GET", "/api/runs/"+plain+"/links", adminToken, nil); len(out["links"].([]any)) != 0 {
		t.Fatalf("a crew without a view link got links: %v", out)
	}
	resp, out := e.do("POST", "/api/crews/plain/launch", adminToken, nil)
	if _, ok := out["viewLink"]; resp.StatusCode != http.StatusCreated || ok {
		t.Fatalf("launch without a view link: %d %v", resp.StatusCode, out)
	}

	e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Shared", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "viewLinkTtlSeconds": 3600,
		"members": []any{catMember("core", "immediately")},
	}, http.StatusCreated)
	before := time.Now()
	resp, out = e.do("POST", "/api/crews/shared/launch", adminToken, nil)
	run, _ := out["run"].(map[string]any)
	view, _ := out["viewLink"].(map[string]any)
	if resp.StatusCode != http.StatusCreated || run == nil || view == nil {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	runID := run["id"].(string)
	link, _ := view["link"].(map[string]any)
	token, _ := view["token"].(string)
	if token == "" || view["url"] != "http://example.test/join/"+token || link["runId"] != runID || link["sessionId"] != "" ||
		link["role"] != "view" || link["label"] != "launch" || link["token"] != nil {
		t.Fatalf("view link %v", view)
	}
	expires, err := time.Parse(time.RFC3339Nano, link["expiresAt"].(string))
	if err != nil || expires.Before(before.Add(3599*time.Second)) || expires.After(time.Now().Add(3601*time.Second)) {
		t.Fatalf("expires %v (%v)", link["expiresAt"], err)
	}
	// Noted in the run's log, which the reply already carries.
	noted := func(r map[string]any) bool {
		for _, entry := range r["log"].([]any) {
			if entry := entry.(map[string]any); entry["type"] == "link" && entry["message"] == "link created: launch (view)" {
				return true
			}
		}
		return false
	}
	if !noted(run) {
		t.Fatalf("the reply's run log has no link entry: %v", run["log"])
	}

	// The token opens the member's session as a viewer.
	coreID := e.waitRunning(t, runID, "core")
	c := dialViewer(t, e, coreID, token)
	c.hello(80, 24)
	if w := c.expectControl(proto.CtlWelcome); w["role"] != "view" {
		t.Fatalf("welcome %v", w)
	}

	// Nowhere else.
	for _, path := range []string{"/api/runs/" + runID, "/api/runs", "/api/runs/" + runID + "/links", "/api/crews", "/api/sessions"} {
		resp, out := e.do("GET", path, adminToken, nil)
		b, _ := json.Marshal(out)
		if resp.StatusCode != http.StatusOK || strings.Contains(string(b), token) {
			t.Errorf("GET %s: %d, carries the token: %v", path, resp.StatusCode, strings.Contains(string(b), token))
		}
		if path == "/api/runs/"+runID && !noted(out["run"].(map[string]any)) {
			t.Errorf("the run log has no link entry: %v", out)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Error("the log carries the token")
	}
}

// A launch that cannot go ahead is refused before any session starts.
func TestCrewLaunchRefusals(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	resp, out := e.do("POST", "/api/crews/nope/launch", adminToken, nil)
	wantAPIError(t, "unknown crew", resp, out, http.StatusNotFound, "not_found", "")

	save := func(name string, change func(b map[string]any)) string {
		b := e.runCrewBody("none")
		b["name"] = name
		change(b)
		return e.sendCrew("POST", "/api/crews", b, http.StatusCreated)["id"].(string)
	}
	cases := []struct {
		name, id  string
		status    int
		code, msg string
	}{
		{"no members", save("Empty", func(b map[string]any) { b["members"] = []any{} }), http.StatusBadRequest, "invalid_crew", "no members"},
		{"on a host", save("Hosted", func(b map[string]any) { b["where"] = "host" }), http.StatusBadRequest, "invalid_crew", "host"},
		{"arguments to cat", save("Cats", func(b map[string]any) { crewMember(b, 1)["agentId"] = "cat" }), http.StatusBadRequest, "invalid_crew", "arguments"},
		{"worktrees without a repository", save("Trees", func(b map[string]any) { b["isolation"] = "worktree" }), http.StatusConflict, "not_a_repo", "git"},
		{"a cwd outside the roots", save("Outside", func(b map[string]any) { b["cwd"] = t.TempDir() }), http.StatusBadRequest, "invalid_cwd", "allowed roots"},
	}
	hidden := save("Hidden", func(b map[string]any) {
		crewMember(b, 1)["agentId"] = "cat"
		delete(crewMember(b, 1), "args")
	})
	for _, tc := range cases {
		resp, out := e.do("POST", "/api/crews/"+tc.id+"/launch", adminToken, nil)
		wantAPIError(t, tc.name, resp, out, tc.status, tc.code, tc.msg)
	}
	// An agent the catalog no longer has.
	if c := e.del("cat"); c != http.StatusNoContent {
		t.Fatalf("hide cat: %d", c)
	}
	resp, out = e.do("POST", "/api/crews/"+hidden+"/launch", adminToken, nil)
	wantAPIError(t, "a hidden agent", resp, out, http.StatusBadRequest, "invalid_crew", `"cat"`)
	if n := e.srv.registry.Count(); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.root, ".conductor")); len(entries) != 0 {
		t.Fatalf("made %v", entries)
	}
	if _, out := e.do("GET", "/api/runs", adminToken, nil); len(out["runs"].([]any)) != 0 {
		t.Fatalf("runs %v", out)
	}
}

// A server without git on its PATH cannot make worktrees: a launch with
// isolation "worktree" says so, 500 launch_failed, not that the working
// directory is no repository. Nothing starts and no run is kept.
func TestCrewLaunchWithoutGit(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	b := e.runCrewBody("worktree")
	id := e.sendCrew("POST", "/api/crews", b, http.StatusCreated)["id"].(string)
	t.Setenv("PATH", t.TempDir())
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	wantAPIError(t, "no git", resp, out, http.StatusInternalServerError, "launch_failed", "git is not installed on the server")
	if n := e.srv.registry.Count(); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
	if _, out := e.do("GET", "/api/runs", adminToken, nil); len(out["runs"].([]any)) != 0 {
		t.Fatalf("runs %v", out)
	}
}

// A session launched on its own gets none of the crew's variables.
func TestPlainSessionHasNoCrewVariables(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("sh")
	c := dialViewer(t, e, id, adminToken)
	c.hello(80, 24)
	c.send(proto.Encode(proto.TypeInput, []byte("echo \"X=${CONDUCTOR_CREW-no}${CONDUCTOR_RUN-no}${CONDUCTOR_MEMBER-no}${GOAL-no}\"\n")))
	c.expectOutput("X=nononono")
	if _, got := e.do("GET", "/api/sessions/"+id, adminToken, nil); got["session"].(map[string]any)["crew"] != nil {
		t.Fatalf("session %v", got["session"])
	}
}

// The run engine takes the server's activity and changes: a handoff one
// member reports through the events route is typed into the member it names,
// once that member's prompt is cleared when it waits on one, and the done a
// member reports starts the members after it.
func TestCrewHandoffReachesTheOtherMember(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	member := func(name string, start map[string]any) map[string]any {
		return map[string]any{"name": name, "agentId": "cat", "prompt": "", "start": start}
	}
	e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Relay", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{
			member("lead", map[string]any{"when": "immediately"}),
			member("tests", map[string]any{"when": "immediately"}),
			member("docs", map[string]any{"when": "after", "member": "lead"}),
		},
	}, http.StatusCreated)
	resp, out := e.do("POST", "/api/crews/relay/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	// memberNow reads a member of the run as GET /api/runs/{run} reports it.
	memberNow := func(name string) map[string]any {
		_, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
		return runMember(t, out["run"].(map[string]any), name)
	}
	leadID, testsID := e.waitRunning(t, runID, "lead"), e.waitRunning(t, runID, "tests")
	agent := e.agentToken(leadID)

	resp, out = e.do("POST", "/api/sessions/"+leadID+"/events", agent, map[string]any{"type": "handoff", "to": "tests", "message": "run the suite"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("handoff: %d %v", resp.StatusCode, out)
	}
	d, _ := e.srv.registry.Get(testsID)
	tests := d.(*session.Local)
	waitTyped := func(text string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !slices.ContainsFunc(tests.Activity(), func(a session.ActivityEntry) bool {
			return a.Type == session.ActivityInput && a.ByName == "crew" && a.Message == text
		}) {
			if time.Now().After(deadline) {
				t.Fatalf("tests records %+v", tests.Activity())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitTyped("Handoff from lead: run the suite")
	c := dialViewer(t, e, testsID, adminToken)
	c.hello(80, 24)
	c.expectOutput("Handoff from lead: run the suite")
	waitLog := func(text string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
			log, _ := json.Marshal(out["run"].(map[string]any)["log"])
			if strings.Contains(string(log), text) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("run log %s", log)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitLog("handoff delivered from lead to tests")

	// tests waits on a prompt: the next handoff waits for it, and goes once
	// the agent clears the prompt, which records no entry.
	testsAgent := e.agentToken(testsID)
	if resp, out := e.do("POST", "/api/sessions/"+testsID+"/events", testsAgent, map[string]any{"type": "needs_input", "message": "Allow edit?"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("needs_input: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/sessions/"+leadID+"/events", agent, map[string]any{"type": "handoff", "to": "tests", "message": "and the docs"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("handoff: %d %v", resp.StatusCode, out)
	}
	waitLog("handoff queued from lead to tests: tests is waiting for input")
	if resp, out := e.do("POST", "/api/sessions/"+testsID+"/events", testsAgent, map[string]any{"type": "clear"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("clear: %d %v", resp.StatusCode, out)
	}
	waitTyped("Handoff from lead: and the docs")

	// lead is done: docs starts.
	if m := memberNow("docs"); m["status"] != "pending" {
		t.Fatalf("docs before lead is done: %v", m)
	}
	if resp, out := e.do("POST", "/api/sessions/"+leadID+"/events", agent, map[string]any{"type": "done"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("done: %d %v", resp.StatusCode, out)
	}
	e.waitRunning(t, runID, "docs")
}

// catMember is a crew member that runs cat and has no prompt: it runs as soon
// as its session starts.
func catMember(name, when string) map[string]any {
	return map[string]any{"name": name, "agentId": "cat", "prompt": "", "start": map[string]any{"when": when}}
}

// launchCrew saves a crew of members working in e's root, without worktrees,
// launches it and returns the run's ID.
func (e *testEnv) launchCrew(t *testing.T, name string, members ...any) string {
	t.Helper()
	id := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": name, "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "members": members,
	}, http.StatusCreated)["id"].(string)
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	return out["run"].(map[string]any)["id"].(string)
}

// waitRunning waits until a member of a run is running, as GET /api/runs/{run}
// reports it, and returns the ID of its session.
func (e *testEnv) waitRunning(t *testing.T, runID, name string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
		m := runMember(t, out["run"].(map[string]any), name)
		if m["status"] == "running" {
			return m["sessionId"].(string)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is %v", name, m)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// local returns the server session with the given ID.
func (e *testEnv) local(id string) *session.Local {
	e.t.Helper()
	d, _ := e.srv.registry.Get(id)
	l, ok := d.(*session.Local)
	if !ok {
		e.t.Fatalf("session %s is not a server session", id)
	}
	return l
}

// inputsBy returns the messages of the input entries l records by byName.
func inputsBy(l *session.Local, byName string) []string {
	var out []string
	for _, a := range l.Activity() {
		if a.Type == session.ActivityInput && a.ByName == byName {
			out = append(out, a.Message)
		}
	}
	return out
}

// outputUntil reads until the accumulated OUTPUT/SCROLLBACK contains needle
// and returns what it read.
func (w *wsClient) outputUntil(needle string) string {
	w.t.Helper()
	var acc []byte
	for !bytes.Contains(acc, []byte(needle)) {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for output %q: %v (have %q)", needle, err, acc)
		}
		if f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
			acc = append(acc, f.Payload...)
		}
	}
	return string(acc)
}

// A broadcast types its text and a carriage return into the members it names,
// recorded as input by the admin's name; a member waiting on a prompt is
// skipped and named, and nothing is typed into it.
func TestBroadcastSkipsWaitingMembers(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"), catMember("web", "immediately"))
	coreID, webID := e.waitRunning(t, runID, "core"), e.waitRunning(t, runID, "web")
	if resp, out := e.do("POST", "/api/sessions/"+webID+"/attention", adminToken, map[string]any{"state": "needs_input", "message": "Allow edit?"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("needs_input: %d %v", resp.StatusCode, out)
	}

	resp, out := e.do("POST", "/api/runs/"+runID+"/broadcast", adminToken, map[string]any{"text": "run the suite", "members": []any{"core", "web"}, "byName": "jd"})
	want := map[string]any{"sent": []any{"core"}, "skipped": []any{map[string]any{"member": "web", "reason": "needs_input"}}}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(out, want) {
		t.Fatalf("broadcast: %d %v", resp.StatusCode, out)
	}
	core := dialViewer(t, e, coreID, adminToken)
	core.hello(80, 24)
	core.expectOutput("run the suite\r\n")
	if got := inputsBy(e.local(coreID), "jd"); !reflect.DeepEqual(got, []string{"run the suite"}) {
		t.Fatalf("core records %q", got)
	}

	// web still waits, and got nothing: what an admin types after the
	// broadcast comes first.
	web := e.local(webID)
	if st := web.Info().Attention.State; st != session.AttentionNeedsInput || len(inputsBy(web, "jd")) != 0 {
		t.Fatalf("web is %q and records %q", st, inputsBy(web, "jd"))
	}
	wc := dialViewer(t, e, webID, adminToken)
	wc.hello(80, 24)
	wc.send(proto.Encode(proto.TypeInput, []byte("marker\r")))
	if got := wc.outputUntil("marker"); strings.Contains(got, "run the suite") {
		t.Fatalf("web was typed into: %q", got)
	}
}

// A broadcast to no member in particular goes to every member, in the run's
// order; a name no member has is skipped as unknown, and a member with no
// session, or whose session has ended, as not running. The text is made one
// line, bounded to 4096 bytes.
func TestBroadcastReasonsAndLimits(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"), catMember("web", "immediately"), catMember("docs", "manual"))
	coreID, webID := e.waitRunning(t, runID, "core"), e.waitRunning(t, runID, "web")
	if resp, _ := e.do("DELETE", "/api/sessions/"+webID, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop web: %d", resp.StatusCode)
	}
	e.waitEnded(webID)
	broadcast := func(body map[string]any) (*http.Response, map[string]any) {
		t.Helper()
		return e.do("POST", "/api/runs/"+runID+"/broadcast", adminToken, body)
	}

	resp, out := broadcast(map[string]any{"text": " line one\nline two\ttabbed\r\nbell\x07\x1b ", "byName": "  jd\x07 "})
	want := map[string]any{"sent": []any{"core"}, "skipped": []any{
		map[string]any{"member": "web", "reason": "not_running"},
		map[string]any{"member": "docs", "reason": "not_running"},
	}}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(out, want) {
		t.Fatalf("to everyone: %d %v", resp.StatusCode, out)
	}
	core := dialViewer(t, e, coreID, adminToken)
	core.hello(80, 24)
	core.expectOutput("line one line two tabbed  bell\r\n")
	if got := inputsBy(e.local(coreID), "jd"); !reflect.DeepEqual(got, []string{"line one line two tabbed  bell"}) {
		t.Fatalf("core records %q", got)
	}

	resp, out = broadcast(map[string]any{"text": "again", "members": []any{"ghost", "core", "core", "docs"}, "byName": "jd"})
	want = map[string]any{"sent": []any{"core"}, "skipped": []any{
		map[string]any{"member": "ghost", "reason": "unknown"},
		map[string]any{"member": "docs", "reason": "not_running"},
	}}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(out, want) {
		t.Fatalf("named: %d %v", resp.StatusCode, out)
	}
	if got := inputsBy(e.local(coreID), "jd"); !reflect.DeepEqual(got, []string{"line one line two tabbed  bell", "again"}) {
		t.Fatalf("core records %q", got)
	}

	// 4096 bytes once made one line, and not one more.
	resp, out = broadcast(map[string]any{"text": "\n" + strings.Repeat("x", 4096) + "\t", "members": []any{"docs"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("4096 bytes: %d %v", resp.StatusCode, out)
	}
	resp, out = broadcast(map[string]any{"text": strings.Repeat("x", 4097), "members": []any{"core"}})
	wantAPIError(t, "4097 bytes", resp, out, http.StatusBadRequest, "invalid_request", "4096")
	resp, out = broadcast(map[string]any{"text": " \r\n\t\x07 "})
	wantAPIError(t, "blank", resp, out, http.StatusBadRequest, "invalid_request", "")
	resp, out = broadcast(map[string]any{"text": "hi", "to": "core"})
	wantAPIError(t, "unknown field", resp, out, http.StatusBadRequest, "invalid_request", "")
	if got := inputsBy(e.local(coreID), "jd"); len(got) != 2 {
		t.Fatalf("a refused broadcast was typed: %q", got)
	}
	resp, out = e.do("POST", "/api/runs/nope/broadcast", adminToken, map[string]any{"text": "hi"})
	wantAPIError(t, "unknown run", resp, out, http.StatusNotFound, "not_found", "")

	_, lo := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control"})
	for _, token := range []string{"", "wrong", "test-host-token", lo["token"].(string)} {
		if resp, _ := e.do("POST", "/api/runs/"+runID+"/broadcast", token, map[string]any{"text": "hi"}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("broadcast with %q: %d", token, resp.StatusCode)
		}
	}
}

// A run link grants its role on the session of every member of its run, one
// added later included, and on no other session. Revoked, it closes the
// viewers attached through it and opens nothing more.
func TestRunLinkGrantsViewOnEveryMember(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"), catMember("web", "immediately"))
	coreID, webID := e.waitRunning(t, runID, "core"), e.waitRunning(t, runID, "web")
	outside := e.createSession("cat")

	resp, out := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "admin"})
	wantAPIError(t, "bad role", resp, out, http.StatusBadRequest, "invalid_role", "")
	resp, out = e.do("POST", "/api/runs/nope/links", adminToken, map[string]any{"role": "view"})
	wantAPIError(t, "unknown run", resp, out, http.StatusNotFound, "not_found", "")
	resp, out = e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "standup", "ttlSeconds": 3600})
	link, _ := out["link"].(map[string]any)
	token, _ := out["token"].(string)
	if resp.StatusCode != http.StatusCreated || link["runId"] != runID || link["sessionId"] != "" || link["role"] != "view" ||
		link["expiresAt"] == nil || out["url"] != "http://example.test/join/"+token {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	linkID := link["id"].(string)

	attach := func(id string) *wsClient {
		t.Helper()
		c := dialViewer(t, e, id, token)
		c.hello(80, 24)
		if w := c.expectControl(proto.CtlWelcome); w["role"] != "view" || w["sessionId"] != id {
			t.Fatalf("welcome on %s: %v", id, w)
		}
		c.expectControl(proto.CtlReady)
		return c
	}
	viewers := []*wsClient{attach(coreID), attach(webID)}
	dialViewer(t, e, outside, token).expectClose(proto.CloseUnauthorized)

	// A member added later is covered.
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, catMember("docs", "immediately"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %v", resp.StatusCode, out)
	}
	viewers = append(viewers, attach(runMember(t, out["run"].(map[string]any), "docs")["sessionId"].(string)))

	// Listed with the viewers attached through it, never with its token; not
	// listed for a session.
	resp, out = e.do("GET", "/api/runs/"+runID+"/links", adminToken, nil)
	links, _ := out["links"].([]any)
	if resp.StatusCode != http.StatusOK || len(links) != 1 {
		t.Fatalf("list: %d %v", resp.StatusCode, out)
	}
	if l := links[0].(map[string]any); l["id"] != linkID || l["runId"] != runID || l["label"] != "standup" || l["active"] != 3.0 || l["token"] != nil {
		t.Fatalf("listed %v", l)
	}
	if _, out := e.do("GET", "/api/sessions/"+coreID+"/links", adminToken, nil); len(out["links"].([]any)) != 0 {
		t.Fatalf("session links %v", out)
	}
	resp, out = e.do("GET", "/api/runs/nope/links", adminToken, nil)
	wantAPIError(t, "list an unknown run", resp, out, http.StatusNotFound, "not_found", "")

	// Revoked only through its run.
	for _, path := range []string{"/api/runs/other/links/" + linkID, "/api/sessions/" + coreID + "/links/" + linkID, "/api/runs/" + runID + "/links/nope"} {
		resp, out := e.do("DELETE", path, adminToken, nil)
		wantAPIError(t, "revoke "+path, resp, out, http.StatusNotFound, "not_found", "")
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/runs/" + runID + "/links"}, {"POST", "/api/runs/" + runID + "/links"}, {"DELETE", "/api/runs/" + runID + "/links/" + linkID},
	} {
		for _, tok := range []string{"", "wrong", "test-host-token", token} {
			if resp, _ := e.do(r.method, r.path, tok, map[string]any{"role": "view"}); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s with %q: %d", r.method, r.path, tok, resp.StatusCode)
			}
		}
	}
	// The viewers read as a browser does, so that each answers its close.
	closed := make(chan websocket.StatusCode, len(viewers))
	for _, c := range viewers {
		go func() {
			for {
				if _, err := c.read(); err != nil {
					closed <- websocket.CloseStatus(err)
					return
				}
			}
		}()
	}
	if resp, _ := e.do("DELETE", "/api/runs/"+runID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	for range viewers {
		if code := <-closed; code != proto.CloseForbidden {
			t.Errorf("a viewer closed with %d", code)
		}
	}
	dialViewer(t, e, coreID, token).expectClose(proto.CloseUnauthorized)
	if _, out := e.do("GET", "/api/runs/"+runID+"/links", adminToken, nil); out["links"].([]any)[0].(map[string]any)["revoked"] != true {
		t.Fatalf("after revoke %v", out)
	}
	// The run log has the link created and revoked, and never its token.
	wantRunLog(t, e, runID, token, "link created: standup (view)", "link revoked: standup")
}

// wantRunLog requires the log of a run to have a link entry with each of
// messages, and nowhere the text never.
func wantRunLog(t *testing.T, e *testEnv, runID, never string, messages ...string) {
	t.Helper()
	_, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
	log, _ := out["run"].(map[string]any)["log"].([]any)
	raw, _ := json.Marshal(log)
	if strings.Contains(string(raw), never) {
		t.Fatalf("the run log has %q: %s", never, raw)
	}
	for _, msg := range messages {
		if !slices.ContainsFunc(log, func(a any) bool {
			entry := a.(map[string]any)
			return entry["type"] == session.ActivityLink && entry["message"] == msg
		}) {
			t.Errorf("the run log has no link entry %q: %s", msg, raw)
		}
	}
}

// A run the engine forgets, past its 100 runs, takes its links with it: they
// open nothing any more, the join page no longer knows them, and the viewers
// attached through them are closed, with the reason that holds for each (a
// member that ended with the run's stop closes its viewer as the session's
// end, not as a revoke). A session's own link to a member's session stays.
func TestRunLinksGoWithTheirForgottenRun(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"))
	coreID := e.waitRunning(t, runID, "core")
	_, lo := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	token := lo["token"].(string)
	_, so := e.do("POST", "/api/sessions/"+coreID+"/links", adminToken, map[string]any{"role": "view"})
	sessionToken := so["token"].(string)
	wantRunLog(t, e, runID, token, "link created: unlabelled (view)")

	viewer := dialViewer(t, e, coreID, token)
	viewer.hello(80, 24)
	viewer.expectControl(proto.CtlReady)
	// Stopped, the run has nothing running: the first to go.
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	viewer.expectControl(proto.CtlStatus)

	idle := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Idle", "goal": "wait", "cwd": e.root, "where": "server", "isolation": "none",
		"members": []any{catMember("lead", "manual")},
	}, http.StatusCreated)["id"].(string)
	for i := range 100 {
		if resp, out := e.do("POST", "/api/crews/"+idle+"/launch", adminToken, nil); resp.StatusCode != http.StatusCreated {
			t.Fatalf("launch %d: %d %v", i, resp.StatusCode, out)
		}
	}
	// Forgotten by the engine: answered from its record alone (#127).
	resp, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["record"] != true {
		t.Fatalf("the forgotten run: %d %v", resp.StatusCode, out)
	}

	// Closed as on a revoke, its member's session's end long past: the
	// session's end closes the viewers of the session's own links alone.
	viewer.expectClose(proto.CloseForbidden)
	resp, out = e.do("GET", "/api/join/"+token, "", nil)
	wantAPIError(t, "join", resp, out, http.StatusNotFound, "invalid_link", "")
	dialViewer(t, e, coreID, token).expectClose(proto.CloseUnauthorized)
	if resp, _ := e.do("GET", "/api/sessions/"+coreID, token, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the member's session: %d", resp.StatusCode)
	}
	// The member's own link went when its session ended, with the stop.
	resp, out = e.do("GET", "/api/join/"+sessionToken, "", nil)
	wantAPIError(t, "the ended session's own link", resp, out, http.StatusNotFound, "invalid_link", "")
}

// GET /api/join/{token} answers a run link with its run: every member, with
// its session only while that session runs. The link reads the session and
// the files of a member, and of no other session.
func TestRunLinkJoinSessionAndFiles(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	if err := os.WriteFile(filepath.Join(e.root, "notes.md"), []byte("# hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runID := e.launchCrew(t, "Squad", catMember("core", "immediately"), catMember("web", "immediately"), catMember("docs", "manual"))
	coreID, webID := e.waitRunning(t, runID, "core"), e.waitRunning(t, runID, "web")
	if resp, _ := e.do("DELETE", "/api/sessions/"+webID, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop web: %d", resp.StatusCode)
	}
	e.waitEnded(webID)
	outside := e.createSession("cat")
	_, lo := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control", "label": "pairing"})
	token := lo["token"].(string)

	resp, out := e.do("GET", "/api/join/"+token, "", nil)
	want := map[string]any{
		"run": map[string]any{"id": runID, "name": "Squad", "members": []any{
			map[string]any{"name": "core", "sessionId": coreID, "agentId": "cat", "status": "running"},
			map[string]any{"name": "web", "agentId": "cat", "status": "ended"},
			map[string]any{"name": "docs", "agentId": "cat", "status": "pending"},
		}},
		"role": "control", "label": "pairing", "switchyard": false,
	}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(out, want) {
		t.Fatalf("join: %d %v", resp.StatusCode, out)
	}

	resp, out = e.do("GET", "/api/sessions/"+coreID, token, nil)
	if resp.StatusCode != http.StatusOK || out["role"] != "control" || out["links"] != nil {
		t.Fatalf("member session: %d %v", resp.StatusCode, out)
	}
	for _, path := range []string{"/api/sessions/" + outside, "/api/sessions"} {
		if resp, _ := e.do("GET", path, token, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s: %d", path, resp.StatusCode)
		}
	}
	if status, body, _ := e.getFile(coreID, token, "notes.md", "raw"); status != http.StatusOK || body != "# hi\n" {
		t.Fatalf("member file: %d %q", status, body)
	}
	if status, _, _ := e.getFile(outside, token, "notes.md", "raw"); status != http.StatusUnauthorized {
		t.Fatalf("outside file: %d", status)
	}
	c := dialViewer(t, e, coreID, token)
	c.hello(80, 24)
	if w := c.expectControl(proto.CtlWelcome); w["role"] != "control" {
		t.Fatalf("welcome %v", w)
	}
	c.c.Close(websocket.StatusNormalClosure, "")

	linkID := lo["link"].(map[string]any)["id"].(string)
	e.do("DELETE", "/api/runs/"+runID+"/links/"+linkID, adminToken, nil)
	resp, out = e.do("GET", "/api/join/"+token, "", nil)
	wantAPIError(t, "join after revoke", resp, out, http.StatusNotFound, "revoked", "")
}

// Revoking a link twice notes it once, in the run's log and in the session's.
func TestRevokingARevokedLinkRecordsNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "Crew", catMember("lead", "manual"))
	_, out := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view", "label": "pair"})
	linkID := out["link"].(map[string]any)["id"].(string)
	for range 2 {
		if resp, _ := e.do("DELETE", "/api/runs/"+runID+"/links/"+linkID, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("revoke: %d", resp.StatusCode)
		}
	}
	_, out = e.do("GET", "/api/runs/"+runID, adminToken, nil)
	n := 0
	for _, raw := range out["run"].(map[string]any)["log"].([]any) {
		if raw.(map[string]any)["message"] == "link revoked: pair" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the run log notes the revoke %d times", n)
	}
	id := e.createSession("cat")
	_, out = e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view", "label": "solo"})
	sl := out["link"].(map[string]any)["id"].(string)
	for range 2 {
		if resp, _ := e.do("DELETE", "/api/sessions/"+id+"/links/"+sl, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("session revoke: %d", resp.StatusCode)
		}
	}
	n = 0
	for _, a := range e.local(id).Activity() {
		if a.Type == session.ActivityLink && a.Message == "link revoked: solo" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the session records the revoke %d times", n)
	}
}

// A crew with a member whose agent is not installed on the server is refused
// at launch, before any session starts, naming the member and the agent; so
// is such a member added to a run.
func TestLaunchRefusesAnAgentNotInstalled(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	ghost := agentBody("ghost")
	ghost["command"] = []string{"definitely-not-a-real-binary-xyz"}
	e.save(ghost)
	body := e.crewBody("Ghost crew")
	body["isolation"] = "none"
	crewMember(body, 1)["agentId"] = "ghost"
	c := e.sendCrew("POST", "/api/crews", body, http.StatusCreated)
	resp, out := e.do("POST", "/api/crews/"+c["id"].(string)+"/launch", adminToken, nil)
	wantAPIError(t, "launch", resp, out, http.StatusBadRequest, "invalid_crew", `member "tests": agent "ghost" is not installed on the server`)
	if _, out := e.do("GET", "/api/sessions", adminToken, nil); len(out["sessions"].([]any)) != 0 {
		t.Fatalf("a session was started: %v", out)
	}
	crewMember(body, 1)["agentId"] = "cat"
	c = e.sendCrew("POST", "/api/crews", body, http.StatusCreated)
	resp, out = e.do("POST", "/api/crews/"+c["id"].(string)+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("launch: %d %v", resp.StatusCode, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	resp, out = e.do("POST", "/api/runs/"+runID+"/members", adminToken, map[string]any{"name": "late", "agentId": "ghost", "prompt": "", "start": map[string]any{"when": "manual"}})
	wantAPIError(t, "add member", resp, out, http.StatusBadRequest, "invalid_crew", `member "late": agent "ghost" is not installed on the server`)
}

// POST /api/crews/examples seeds the examples once, one file each, with the
// server's default working directory: a second call adds nothing, an edit
// survives it, a deleted example comes back, and the list pages them with the
// rest.
func TestSeedExamplesOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	if resp, _ := e.do("POST", "/api/crews/examples", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	resp, out := e.do("POST", "/api/crews/examples", adminToken, nil)
	if resp.StatusCode != http.StatusOK || len(out["added"].([]any)) != 5 || len(out["skipped"].([]any)) != 0 {
		t.Fatalf("first seed: %d %v", resp.StatusCode, out)
	}
	if ids := e.crewIDs(); !slices.Equal(ids, []string{"example-dependency-upgrade", "example-docs-writer", "example-test-fixer", "example-todo-app", "example-todo-app-tasks"}) {
		t.Fatalf("listed by name: %v", ids)
	}
	// The todo app with tasks reads back with its tasks, every member started by them.
	tc := e.crew("example-todo-app-tasks")
	if tasks, _ := tc["tasks"].([]any); len(tasks) != 5 || tasks[3].(map[string]any)["id"] != "tests" || len(tasks[3].(map[string]any)["needs"].([]any)) != 2 {
		t.Fatalf("the task example's tasks: %v", tc["tasks"])
	}
	for i := range tc["members"].([]any) {
		if m := crewMember(tc, i); m["start"].(map[string]any)["when"] != "task" {
			t.Fatalf("member %v", m)
		}
	}
	c := e.crew("example-todo-app")
	if c["cwd"] != e.root || c["isolation"] != "worktree" || len(c["members"].([]any)) != 4 || e.storedCrew("example-todo-app") == nil {
		t.Fatalf("todo app: %v", c)
	}
	if m := crewMember(c, 3); m["name"] != "tester" || m["agentId"] != "codex" || m["start"].(map[string]any)["member"] != "cli" {
		t.Fatalf("tester: %v", m)
	}
	// Edit it as the editor would, then seed again: the edit stays.
	for _, k := range []string{"id", "createdAt", "updatedAt"} {
		delete(c, k)
	}
	c["goal"] = "edited"
	// The test catalog has neither claude nor codex: save it with its own agents.
	for i := range c["members"].([]any) {
		crewMember(c, i)["agentId"] = "cat"
	}
	e.sendCrew("PUT", "/api/crews/example-todo-app", c, http.StatusOK)
	if resp, _ := e.do("DELETE", "/api/crews/example-docs-writer", adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	_, out = e.do("POST", "/api/crews/examples", adminToken, nil)
	if added := out["added"].([]any); len(added) != 1 || added[0] != "example-docs-writer" || len(out["skipped"].([]any)) != 4 {
		t.Fatalf("second seed: %v", out)
	}
	if got := e.crew("example-todo-app"); got["goal"] != "edited" {
		t.Fatalf("the edit was lost: %v", got)
	}
}

// GET /api/crews/{id}/runs lists the crew's live runs first, then the
// records of runs that ended; a run stopped through the API is recorded
// under runs/ in the data directory and listed after the engine forgets it.
func TestCrewRunsListsRecordsAndLiveRuns(t *testing.T) {
	e := newTestEnv(t, nil)
	members := []any{map[string]any{"name": "solo", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "manual"}}}
	first := e.launchCrew(t, "records", members...)
	resp, out := e.do("POST", "/api/runs/"+first+"/stop", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	crewID := out["run"].(map[string]any)["crewId"].(string)
	file := filepath.Join(e.srv.cfg.DataDir, "runs", first+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(file); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no record at %s", file)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, out = e.do("POST", "/api/crews/"+crewID+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second launch: %d %v", resp.StatusCode, out)
	}
	second := out["run"].(map[string]any)["id"].(string)
	resp, out = e.do("GET", "/api/crews/"+crewID+"/runs", adminToken, nil)
	runs, _ := out["runs"].([]any)
	if resp.StatusCode != http.StatusOK || len(runs) != 2 || out["live"] != 2.0 {
		t.Fatalf("runs: %d %v", resp.StatusCode, out)
	}
	ids := []string{runs[0].(map[string]any)["id"].(string), runs[1].(map[string]any)["id"].(string)}
	if ids[0] != second || ids[1] != first {
		t.Fatalf("order %v, want %s then %s", ids, second, first)
	}
	// The engine forgets the stopped run: its record still lists it.
	e.srv.runs.ForgetForTest(first)
	resp, out = e.do("GET", "/api/crews/"+crewID+"/runs", adminToken, nil)
	runs, _ = out["runs"].([]any)
	if resp.StatusCode != http.StatusOK || len(runs) != 2 || out["live"] != 1.0 || runs[1].(map[string]any)["id"] != first || runs[1].(map[string]any)["state"] != "stopped" {
		t.Fatalf("after forget: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/crews/nope/runs", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown crew: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("GET", "/api/crews/"+crewID+"/runs", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
}

// A launch may name the run: the name is on the run, read back and kept by a
// resume; no body launches it unnamed; a bad name or an unknown field is refused.
func TestCrewLaunchTakesALabel(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.sendCrew("POST", "/api/crews", map[string]any{
		"name": "Named", "goal": "ship", "cwd": e.root, "where": "server", "isolation": "none", "members": []any{catMember("core", "manual")},
	}, http.StatusCreated)["id"].(string)
	resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated || out["run"].(map[string]any)["label"] != nil {
		t.Fatalf("unnamed: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do("POST", "/api/crews/"+id+"/launch", adminToken, map[string]any{"label": "  fix login  "})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("named: %d %v", resp.StatusCode, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	if _, got := e.do("GET", "/api/runs/"+runID, adminToken, nil); got["run"].(map[string]any)["label"] != "fix login" {
		t.Fatalf("read: %v", got["run"])
	}
	e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil)
	if resp, got := e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil); resp.StatusCode >= 300 || got["run"].(map[string]any)["label"] != "fix login" {
		t.Fatalf("resume: %d %v", resp.StatusCode, got)
	}
	for _, body := range []map[string]any{{"label": strings.Repeat("x", 61)}, {"name": "x"}} {
		resp, out := e.do("POST", "/api/crews/"+id+"/launch", adminToken, body)
		wantAPIError(t, fmt.Sprint(body), resp, out, http.StatusBadRequest, "invalid_request", "")
	}
}

// A run's chat over the members' own connections: a post on one member
// reaches every member's viewers (a run link's among them), the roster
// names who is on the run, a controller's send types a kept message into
// the member it names and marks it, a member not there is answered
// not_sent, the run read alone carries the chat and its record keeps it
// once it ends, while lists carry none.
func TestRunChatReachesEveryMembersViewers(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "Chatty", catMember("core", "immediately"), catMember("tests", "immediately"))
	coreID := e.waitRunning(t, runID, "core")
	testsID := e.waitRunning(t, runID, "tests")
	resp, lo := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "control", "ttlSeconds": 3600})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("run link: %d %v", resp.StatusCode, lo)
	}
	nate := dialViewer(t, e, coreID, adminToken)
	nate.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	if w := nate.expectControl(proto.CtlWelcome); w["runChat"] != true || w["chat"] != true {
		t.Fatalf("welcome %v", w)
	}
	nate.expectControl(proto.CtlReady)
	priya := dialViewer(t, e, testsID, lo["token"].(string))
	priya.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Priya"}))
	priya.expectControl(proto.CtlReady)
	// Priya's arrival sends everyone on the run the roster: who is on which member.
	roster := priya.expectControl(proto.CtlChatRoster)
	names := map[string]string{}
	for _, raw := range roster["list"].([]any) {
		p := raw.(map[string]any)
		names[p["name"].(string)], _ = p["on"].(string)
	}
	if roster["scope"] != "run" || names["Nate"] != "core" || names["Priya"] != "tests" {
		t.Fatalf("roster %v", roster)
	}

	nate.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "core is green", On: "core", Nonce: "r1"}))
	m := priya.expectChat(proto.ChatKindMessage)
	if m["scope"] != "run" || m["on"] != "core" || m["text"] != "core is green" || m["by"].(map[string]any)["name"] != "Nate" || m["nonce"] != "r1" {
		t.Fatalf("priya got %v", m)
	}
	if own := nate.expectChat(proto.ChatKindMessage); own["id"] != m["id"] {
		t.Fatalf("nate's own copy %v", own)
	}

	// Typed into tests (cat echoes it), marked for everyone with the member.
	nate.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Scope: proto.ChatScopeRun, Ref: m["id"].(string), To: "tests"}))
	priya.expectOutput("core is green")
	if marker := priya.expectChat(proto.ChatKindSentToAgent); marker["ref"] != m["id"] || marker["to"] != "tests" || marker["scope"] != "run" {
		t.Fatalf("marker %v", marker)
	}
	nate.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Scope: proto.ChatScopeRun, Ref: m["id"].(string), To: "docs"}))
	if er := nate.expectControl(proto.CtlError); er["code"] != proto.ErrCodeNotSent || er["message"] != session.NotSentUnknown || er["requestId"] != m["id"] {
		t.Fatalf("a member not there: %v", er)
	}

	// The run read alone has the chat; the lists do not.
	_, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
	chat, _ := out["run"].(map[string]any)["chat"].([]any)
	var texts []string
	for _, raw := range chat {
		if mm := raw.(map[string]any); mm["kind"] == proto.ChatKindMessage {
			texts = append(texts, mm["text"].(string))
		}
	}
	if strings.Join(texts, "|") != "core is green" {
		t.Fatalf("the run's chat %v", chat)
	}
	_, list := e.do("GET", "/api/runs", adminToken, nil)
	for _, raw := range list["runs"].([]any) {
		if r := raw.(map[string]any); r["id"] == runID && r["chat"] != nil {
			t.Fatalf("the list carries the chat: %v", r["chat"])
		}
	}

	// Stopped, the record keeps it.
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, ok, err := e.srv.records.Get(runID)
		if err == nil && ok && len(rec.Chat) > 0 {
			var kept []string
			for _, mm := range rec.Chat {
				if mm.Kind == proto.ChatKindMessage {
					kept = append(kept, mm.Text)
				}
			}
			if strings.Join(kept, "|") != "core is green" {
				t.Fatalf("the record's chat %v", rec.Chat)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record never kept the chat: %v %v", ok, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, runs := e.do("GET", "/api/crews/chatty/runs", adminToken, nil)
	for _, raw := range runs["runs"].([]any) {
		if r := raw.(map[string]any); r["chat"] != nil {
			t.Fatalf("the crew's runs carry the chat: %v", r["chat"])
		}
	}
}

// A quiet connection (a run page's own) on one member hears the run's chat
// posted and sent from another member's viewer, before and after a line is
// typed into its own member.
func TestRunChatReachesAQuietConnectionOnAnotherMember(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "Quietly", catMember("lead", "immediately"), catMember("review", "immediately"))
	leadID := e.waitRunning(t, runID, "lead")
	reviewID := e.waitRunning(t, runID, "review")
	priya := dialViewer(t, e, leadID, adminToken)
	priya.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Priya", ChatOnly: true}))
	priya.expectControl(proto.CtlReady)
	nate := dialViewer(t, e, reviewID, adminToken)
	nate.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	nate.expectControl(proto.CtlReady)
	// The ready marker comes ahead of the chat's replay: a post made before
	// the replay is taken comes in it, not live. Nate hears the run live once
	// the roster his attach sends has come, behind the replay.
	nate.expectControl(proto.CtlViewers)
	// Priya posts from her quiet connection; Nate posts; both see both.
	priya.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "hi back"}))
	if m := nate.expectChat(proto.ChatKindMessage); m["text"] != "hi back" {
		t.Fatalf("nate got %v", m)
	}
	if m := priya.expectChat(proto.ChatKindMessage); m["text"] != "hi back" {
		t.Fatalf("priya's own copy %v", m)
	}
	nate.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "echo run-chat-to-lead", To: "lead"}))
	if m := priya.expectChat(proto.ChatKindMessage); m["text"] != "echo run-chat-to-lead" {
		t.Fatalf("priya got %v", m)
	}
	if marker := priya.expectChat(proto.ChatKindSentToAgent); marker["to"] != "lead" {
		t.Fatalf("priya's marker %v", marker)
	}
	if marker := nate.expectChat(proto.ChatKindSentToAgent); marker["to"] != "lead" {
		t.Fatalf("nate's marker %v", marker)
	}
	// Still there after the line was typed into her member.
	nate.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "and after"}))
	if m := priya.expectChat(proto.ChatKindMessage); m["text"] != "and after" {
		t.Fatalf("priya after the send got %v", m)
	}
}

// A run the engine no longer holds is answered from its record, marked
// `record`, as it was when it ended: its page opens read-only. Its actions
// but a resume (TestARunIsResumedFromItsRecord) answer 404 as for any run
// the engine does not hold (#127).
func TestGetRunAnswersItsRecordReadOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	members := []any{map[string]any{"name": "solo", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "manual"}}}
	runID := e.launchCrew(t, "records", members...)
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(file); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no record at %s", file)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Held by the engine: the run itself, no record mark.
	if resp, out := e.do("GET", "/api/runs/"+runID, adminToken, nil); resp.StatusCode != http.StatusOK || out["record"] != nil {
		t.Fatalf("while held: %d %v", resp.StatusCode, out)
	}
	e.srv.runs.ForgetForTest(runID)
	resp, out := e.do("GET", "/api/runs/"+runID, adminToken, nil)
	run, _ := out["run"].(map[string]any)
	if resp.StatusCode != http.StatusOK || out["record"] != true || run["id"] != runID || run["state"] != "stopped" || run["stoppedAt"] == nil {
		t.Fatalf("from the record: %d %v", resp.StatusCode, out)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/runs/" + runID + "/stop", nil},
		{"POST", "/api/runs/" + runID + "/members/solo/start", nil},
		{"POST", "/api/runs/" + runID + "/broadcast", map[string]any{"text": "anyone?"}},
	} {
		if resp, out := e.do(c.method, c.path, adminToken, c.body); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s on a record: %d %v", c.method, c.path, resp.StatusCode, out)
		}
	}
	if resp, _ := e.do("GET", "/api/runs/no-such-run-00000000", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run: %d", resp.StatusCode)
	}
}

// A run's record is written from its launch on (the journal, a couple of
// seconds after a change), not only as it ends: a run cut short by the
// server's end has one (#153).
func TestARunIsJournaledFromItsLaunch(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "journal", catMember("solo", "immediately"))
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(file); err == nil {
			var rec crew.Run
			if json.Unmarshal(b, &rec) != nil || rec.ID != runID || rec.StoppedAt != nil || (rec.State != crew.RunRunning && rec.State != crew.RunNeedsInput) {
				t.Fatalf("the journal: %s", b)
			}
			if len(rec.Members) != 1 || rec.Members[0].Prompt != "" || rec.Members[0].Start.When != "immediately" {
				t.Fatalf("the journal's member: %+v", rec.Members)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no record of the running run at %s", file)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A run the server no longer holds is resumed from its record, once: the
// member whose conversation is resumable continues it (its agent resumed
// with the id the record kept), the new run names the record, the record
// names the new run, and a second resume is refused naming it (#153).
func TestARunIsResumedFromItsRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	e.saveResumable("talker")
	runID := e.launchCrew(t, "from record", map[string]any{"name": "solo", "agentId": "talker", "prompt": "Carry on.", "start": map[string]any{"when": "immediately"}})
	sid := e.waitRunning(t, runID, "solo")
	tok := e.agentToken(sid)
	_, info := e.do("GET", "/api/sessions/"+sid, adminToken, nil)
	pid := info["session"].(map[string]any)["agentSession"].(map[string]any)["id"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/attention", tok, map[string]any{"state": "done", "agentSession": pid, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(file); err == nil {
			var rec crew.Run
			if json.Unmarshal(b, &rec) == nil && rec.StoppedAt != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stopped record at %s", file)
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.srv.runs.ForgetForTest(runID)
	resp, out := e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume from the record: %d %v", resp.StatusCode, out)
	}
	next := out["run"].(map[string]any)
	nextID := next["id"].(string)
	if next["resumedFrom"] != runID || next["crewId"] == nil {
		t.Fatalf("the new run: %v", next)
	}
	member := next["members"].([]any)[0].(map[string]any)
	nsid, _ := member["sessionId"].(string)
	if nsid == "" {
		t.Fatalf("the member has no session: %v", member)
	}
	_, ninfo := e.do("GET", "/api/sessions/"+nsid, adminToken, nil)
	if cmd := commandOf(ninfo["session"].(map[string]any)); !slices.Contains(cmd, "--resume") || !slices.Contains(cmd, pid) {
		t.Fatalf("the member's command: %q", cmd)
	}
	// The record names the continuation, and is resumed no more.
	_, rec := e.do("GET", "/api/runs/"+runID, adminToken, nil)
	if rec["record"] != true || rec["run"].(map[string]any)["resumedBy"] != nextID {
		t.Fatalf("the record after: %v", rec)
	}
	resp, out = e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil)
	if apiErr, _ := out["error"].(map[string]any); resp.StatusCode != http.StatusConflict || apiErr["code"] != "already_resumed" || apiErr["run"] != nextID {
		t.Fatalf("a second resume of the record: %d %v", resp.StatusCode, out)
	}
	e.do("POST", "/api/runs/"+nextID+"/stop", adminToken, nil)
}

// The journal follows the conversation a resume from the record would
// continue: a member's agent session reported after the launch's record was
// written reaches the record on its own, with no other change of the run
// (Codex's review of #153's change).
func TestAConversationReportedLaterReachesTheJournal(t *testing.T) {
	e := newTestEnv(t, nil)
	e.saveResumable("talker")
	runID := e.launchCrew(t, "late report", map[string]any{"name": "solo", "agentId": "talker", "prompt": "", "start": map[string]any{"when": "immediately"}})
	sid := e.waitRunning(t, runID, "solo")
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	record := func() crew.Run {
		b, err := os.ReadFile(file)
		if err != nil {
			return crew.Run{}
		}
		var rec crew.Run
		_ = json.Unmarshal(b, &rec)
		return rec
	}
	deadline := time.Now().Add(10 * time.Second)
	for record().ID != runID {
		if time.Now().After(deadline) {
			t.Fatal("no record of the running run")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if m := record().Members[0]; m.AgentSession != nil && m.AgentSession.Resumable {
		t.Fatalf("resumable before any turn: %+v", m)
	}
	tok := e.agentToken(sid)
	_, info := e.do("GET", "/api/sessions/"+sid, adminToken, nil)
	pid := info["session"].(map[string]any)["agentSession"].(map[string]any)["id"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/attention", tok, map[string]any{"state": "done", "agentSession": pid, "turn": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("report: %d %v", resp.StatusCode, out)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		if m := record().Members[0]; m.AgentSession != nil && m.AgentSession.Resumable && m.AgentSession.ID == pid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the record never learnt of the conversation: %+v", record().Members)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil)
}

// Resumes of one record fired together make one continuation: the record is
// read and checked under a lease, the others refused. And the continuation
// is recorded before the source names it (Codex's review of #153's change).
func TestResumesOfARecordFiredTogetherMakeOneContinuation(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "record race", map[string]any{"name": "solo", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "manual"}})
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(file); err == nil && strings.Contains(string(b), `"stoppedAt"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no stopped record at %s", file)
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.srv.runs.ForgetForTest(runID)
	const n = 6
	codes := make(chan int, n)
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			resp, out := e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil)
			codes <- resp.StatusCode
			if resp.StatusCode == http.StatusCreated {
				ids <- out["run"].(map[string]any)["id"].(string)
			}
		})
	}
	wg.Wait()
	close(codes)
	close(ids)
	created, conflicts := 0, 0
	for c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("a resume answered %d", c)
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("%d continuations made and %d refused of %d resumes of the record at once", created, conflicts, n)
	}
	next := <-ids
	if _, ok, _ := e.srv.records.Get(next); !ok {
		t.Fatal("the continuation has no record as its resume answers")
	}
	if rec, _, _ := e.srv.records.Get(runID); rec.ResumedBy != next {
		t.Fatalf("the source names %q, want %s", rec.ResumedBy, next)
	}
	e.do("POST", "/api/runs/"+next+"/stop", adminToken, nil)
}

// A run forgotten right after it was resumed, before the journal wrote it
// again, has its record name the continuation all the same: a forgotten
// run's last state is its record (Codex's review of #153's change).
func TestAForgottenRunsRecordNamesItsContinuation(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "forget after resume", map[string]any{"name": "solo", "agentId": "cat", "prompt": "", "start": map[string]any{"when": "manual"}})
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	resp, out := e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: %d %v", resp.StatusCode, out)
	}
	next := out["run"].(map[string]any)["id"].(string)
	e.srv.runs.ForgetForTest(runID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, ok, _ := e.srv.records.Get(runID); ok && rec.ResumedBy == next {
			break
		}
		if time.Now().After(deadline) {
			rec, ok, _ := e.srv.records.Get(runID)
			t.Fatalf("the forgotten run's record: %v %q", ok, rec.ResumedBy)
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.do("POST", "/api/runs/"+next+"/stop", adminToken, nil)
}

// A run forgotten keeps its chat in its record: the record written as it
// goes is the whole of it (Codex's review of #153's change). And a held
// run's continuation has its record as its resume answers.
func TestAForgottenRunsRecordKeepsItsChat(t *testing.T) {
	e := newTestEnv(t, nil)
	runID := e.launchCrew(t, "chat kept", catMember("solo", "immediately"))
	sid := e.waitRunning(t, runID, "solo")
	v := dialViewer(t, e, sid, adminToken)
	v.hello(80, 24)
	v.expectControl(proto.CtlReady)
	v.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "kept in the record?", Nonce: "k1"}))
	v.expectChat(proto.ChatKindMessage)
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, ok, _ := e.srv.records.Get(runID); ok && rec.StoppedAt != nil && len(rec.Chat) > 0 {
			break
		}
		if time.Now().After(deadline) {
			rec, ok, _ := e.srv.records.Get(runID)
			t.Fatalf("no stopped record with the chat: %v %d messages", ok, len(rec.Chat))
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, out := e.do("POST", "/api/runs/"+runID+"/resume", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("resume: %d %v", resp.StatusCode, out)
	}
	next := out["run"].(map[string]any)["id"].(string)
	if _, ok, _ := e.srv.records.Get(next); !ok {
		t.Fatal("the continuation has no record as its resume answers")
	}
	e.srv.runs.ForgetForTest(runID)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	e.srv.runs.WaitRecorded(ctx)
	deadline = time.Now().Add(5 * time.Second)
	for {
		rec, ok, _ := e.srv.records.Get(runID)
		if ok && rec.ResumedBy == next {
			if !slices.ContainsFunc(rec.Chat, func(m proto.ChatMessage) bool { return m.Text == "kept in the record?" }) {
				t.Fatalf("the forgotten run's record lost its chat: %+v", rec.Chat)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the forgotten run's record: %v %q", ok, rec.ResumedBy)
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.do("POST", "/api/runs/"+next+"/stop", adminToken, nil)
}

// manifestCrewBody is a crew with tasks as the web client sends it, its
// member on agent, with the project manifest given.
func (e *testEnv) manifestCrewBody(name, agent string, manifest any) map[string]any {
	return map[string]any{
		"name": name, "goal": "ship it", "cwd": e.root, "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": agent, "prompt": "", "start": map[string]any{"when": "task"}}},
		"tasks": []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it.",
			"checks": []any{map[string]any{"id": "unit", "argv": []any{"true"}}}}},
		"manifest": manifest,
	}
}

// asJSON is v as a client reads it back: through JSON.
func asJSON(t *testing.T, v any) any {
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

// A crew's project manifest goes in by the crew body and reads back as sent,
// with the server's count of its changes beside it: 1 when created, 2 once
// changed, kept by a save that leaves it as it was. The count is the
// server's: a body that carries it is refused as one carrying id is.
func TestACrewsProjectManifestRoundTripsWithItsRevision(t *testing.T) {
	e := newTestEnv(t, nil)
	manifest := map[string]any{
		"prerequisites": []any{map[string]any{"id": "node", "argv": []any{"node", "--version"}, "timeoutSeconds": 30}},
		"env":           []any{"NPM_TOKEN"},
		"ports":         []any{"web"},
		"setup":         []any{map[string]any{"id": "deps", "argv": []any{"npm", "ci"}}},
		"services":      []any{map[string]any{"id": "web", "argv": []any{"npm", "start", "--", "--port", "${CONDUCTOR_PORT_WEB}"}}},
		"ready":         []any{map[string]any{"id": "up", "url": "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/", "intervalSeconds": 1}},
		"preview":       "http://localhost:${CONDUCTOR_PORT_WEB}/",
		"verify":        []any{map[string]any{"id": "e2e", "argv": []any{"npm", "run", "e2e"}}},
		"cleanup":       []any{map[string]any{"id": "clean", "argv": []any{"rm", "-rf", "${CONDUCTOR_STATE_DIR}/db"}}},
	}
	created := e.sendCrew("POST", "/api/crews", e.manifestCrewBody("Manifests", "cat", manifest), http.StatusCreated)
	id, _ := created["id"].(string)
	if !reflect.DeepEqual(created["manifest"], asJSON(t, manifest)) || created["manifestRevision"] != 1.0 {
		t.Fatalf("created: %v %v", created["manifest"], created["manifestRevision"])
	}
	if got := e.crew(id); !reflect.DeepEqual(got, created) {
		t.Fatalf("read back: %v", got)
	}
	changed := e.manifestCrewBody("Manifests", "cat", map[string]any{"setup": []any{map[string]any{"id": "deps", "argv": []any{"npm", "install"}}}})
	updated := e.sendCrew("PUT", "/api/crews/"+id, changed, http.StatusOK)
	if updated["manifestRevision"] != 2.0 || !reflect.DeepEqual(updated["manifest"], asJSON(t, changed["manifest"])) {
		t.Fatalf("updated: %v %v", updated["manifest"], updated["manifestRevision"])
	}
	if got := e.crew(id); got["manifestRevision"] != 2.0 || !reflect.DeepEqual(got["manifest"], updated["manifest"]) {
		t.Fatalf("read back after the update: %v", got)
	}
	changed["goal"] = "ship it again"
	if again := e.sendCrew("PUT", "/api/crews/"+id, changed, http.StatusOK); again["manifestRevision"] != 2.0 {
		t.Fatalf("an unchanged manifest: %v", again["manifestRevision"])
	}
	if stored := e.storedCrew(id); stored["manifestRevision"] != 2.0 || stored["manifest"] == nil {
		t.Fatalf("the file: %v", stored)
	}

	// The count is the server's.
	carrying := e.manifestCrewBody("Manifests", "cat", manifest)
	carrying["manifestRevision"] = 7
	resp, out := e.do("PUT", "/api/crews/"+id, adminToken, carrying)
	wantAPIError(t, "a body carrying manifestRevision", resp, out, http.StatusBadRequest, "invalid_request", `unknown field "manifestRevision"`)
	resp, out = e.do("POST", "/api/crews", adminToken, carrying)
	wantAPIError(t, "a create carrying manifestRevision", resp, out, http.StatusBadRequest, "invalid_request", `unknown field "manifestRevision"`)
	// A manifest is decoded as strictly as the rest of the body.
	resp, out = e.do("POST", "/api/crews", adminToken, e.manifestCrewBody("Manifests", "cat", map[string]any{"setups": []any{}}))
	wantAPIError(t, "an unknown field of the manifest", resp, out, http.StatusBadRequest, "invalid_request", `unknown field "setups"`)
	// setup is the manifest's setup phase: a crew has one or the other.
	both := e.manifestCrewBody("Manifests", "cat", manifest)
	both["setup"] = []any{map[string]any{"id": "old", "argv": []any{"true"}}}
	resp, out = e.do("PUT", "/api/crews/"+id, adminToken, both)
	wantAPIError(t, "setup beside a manifest", resp, out, http.StatusBadRequest, "invalid_crew", "setup is the project manifest's setup phase: put it there")
	// A rule of the manifest is a rule of the crew.
	resp, out = e.do("POST", "/api/crews", adminToken, e.manifestCrewBody("Manifests", "cat", map[string]any{"preview": "http://example.com/"}))
	wantAPIError(t, "a preview off the loopback", resp, out, http.StatusBadRequest, "invalid_crew", "must be http or https on 127.0.0.1, localhost or [::1]")
	if got := e.crew(id); got["manifestRevision"] != 2.0 || got["goal"] != "ship it again" {
		t.Fatalf("after the refusals: %v", got)
	}
}

// A crew read back goes back by PUT once the fields the server sets are
// taken out, as docs/protocol.md's crew rows name them: id, createdAt,
// updatedAt and manifestRevision, which a crew with a legacy setup carries
// too (1 when created). A body that keeps the count is refused.
func TestACrewReadBackGoesBackWithoutWhatTheServerSets(t *testing.T) {
	e := newTestEnv(t, nil)
	body := e.manifestCrewBody("Legacy", "cat", nil)
	delete(body, "manifest")
	body["setup"] = []any{map[string]any{"id": "deps", "argv": []any{"npm", "ci"}}}
	id, _ := e.sendCrew("POST", "/api/crews", body, http.StatusCreated)["id"].(string)
	read := e.crew(id)
	if read["manifestRevision"] != 1.0 {
		t.Fatalf("a new crew with a setup: %v", read["manifestRevision"])
	}
	for _, k := range []string{"id", "createdAt", "updatedAt"} {
		delete(read, k)
	}
	resp, out := e.do("PUT", "/api/crews/"+id, adminToken, read)
	wantAPIError(t, "a crew read back with its count", resp, out, http.StatusBadRequest, "invalid_request", `unknown field "manifestRevision"`)
	delete(read, "manifestRevision")
	read["goal"] = "edited"
	if saved := e.sendCrew("PUT", "/api/crews/"+id, read, http.StatusOK); saved["manifestRevision"] != 1.0 || saved["goal"] != "edited" || saved["setup"] == nil {
		t.Fatalf("a crew read back without what the server sets: %v", saved)
	}
}

// A project manifest of every phase launches (EN1 PR-5 lifted the last
// refusal): one with verify and cleanup steps is taken, and one with its
// setup phase alone launches, its setup run as a legacy setup is.
func TestALaunchOfAProjectManifestOfEveryPhaseIsTaken(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	every := e.sendCrew("POST", "/api/crews", e.manifestCrewBody("Every", "ready-cat",
		map[string]any{"verify": []any{map[string]any{"id": "e2e", "argv": []any{"true"}}}, "cleanup": []any{map[string]any{"id": "rm", "argv": []any{"true"}}}}), http.StatusCreated)
	resp, out := e.do("POST", "/api/crews/"+every["id"].(string)+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a manifest with verify and cleanup steps: %d %v", resp.StatusCode, out)
	}
	everyRun := out["run"].(map[string]any)["id"].(string)
	runningAttempt(t, e, everyRun, "build", 1)
	if resp, out := e.do("POST", "/api/runs/"+everyRun+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	setupOnly := e.sendCrew("POST", "/api/crews", e.manifestCrewBody("Setup", "ready-cat",
		map[string]any{"setup": []any{map[string]any{"id": "deps", "argv": []any{"sh", "-c", "echo dep > dep.txt"}}}}), http.StatusCreated)
	resp, out = e.do("POST", "/api/crews/"+setupOnly["id"].(string)+"/launch", adminToken, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a manifest with its setup alone: %d %v", resp.StatusCode, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	a := runningAttempt(t, e, runID, "build", 1)
	if _, err := os.Stat(filepath.Join(a["worktree"].(string), "dep.txt")); err != nil {
		t.Fatalf("the manifest's setup did not run in the worktree: %v", err)
	}
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
}
