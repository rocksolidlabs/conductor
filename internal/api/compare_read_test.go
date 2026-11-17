package api

import (
	"bytes"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A comparison's read (GET /api/runs/{run}/comparison, PR-3): the admin's
// alone, from any run of the comparison's chain; door 10 closed to every
// lane; nothing of the touched index read.

// comparisonOf reads the comparison of the run as the admin: its status,
// its bytes and its JSON.
func (ce *compareEnv) comparisonOf(t *testing.T, run string) (int, []byte, map[string]any) {
	t.Helper()
	return ce.call(t, "GET", "/api/runs/"+run+"/comparison", nil)
}

// The comparison's shape (the engine's measure with the independence the
// server adds; no supporting or selection part yet), the same from the
// launched run and from its continuation, under its bound; 404 for a run
// of no comparison and an unknown one, 400 for an id that is none and for
// a parameter; the admin's alone: a share link (a controller's), a
// candidate's, a reviewer's and an ordinary member's session tokens and
// no token are refused: 401, or, for a session token naming its own
// session, the shared reads' answer to a token not a turn's (409 lane
// for a lane, 403 not_brakeman for another member).
func TestComparisonRoute(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	status, raw, out := ce.comparisonOf(t, ce.run)
	if status != http.StatusOK || len(raw) > crew.MaxComparison {
		t.Fatalf("the comparison: %d %.300s", status, raw)
	}
	keys := []string{"run", "base", "objective", "criteria", "candidates", "checks", "settled", "independence", "supporting", "state"}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			t.Fatalf("no %s in %s", k, raw)
		}
	}
	for k := range out {
		if !slices.Contains(keys, k) {
			t.Fatalf("a key %s the read does not give yet: %s", k, raw)
		}
	}
	cands, _ := out["candidates"].([]any)
	if out["run"] != ce.run || len(cands) != 2 || cands[0].(map[string]any)["task"] != "approach-a" || cands[1].(map[string]any)["task"] != "approach-b" ||
		out["objective"] != "Make the lookup fast." || out["independence"].(map[string]any)["notObserved"] != notObservedWords {
		t.Fatalf("the comparison: %s", raw)
	}

	// Any run of the chain: its continuation reads the same comparison.
	ce.expect(t, "POST", "/api/runs/"+ce.run+"/stop", nil, http.StatusOK, "")
	next := entityOf(t, ce.expect(t, "POST", "/api/runs/"+ce.run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
	status, raw, out = ce.comparisonOf(t, next)
	if status != http.StatusOK || out["run"] != ce.run {
		t.Fatalf("the continuation's comparison: %d %.300s", status, raw)
	}

	for _, tc := range []struct {
		path   string
		status int
		words  string
	}{
		{"/api/runs/" + ce.plain + "/comparison", http.StatusNotFound, "this run is not a comparison"},
		{"/api/runs/api-0000dead/comparison", http.StatusNotFound, "no such run"},
		{"/api/runs/NOT%20A%20RUN/comparison", http.StatusBadRequest, "a run id"},
		{"/api/runs/" + ce.run + "/comparison?task=approach-a", http.StatusBadRequest, ""},
	} {
		status, raw, out := ce.call(t, "GET", tc.path, nil)
		if status != tc.status || !strings.Contains(apiMessage(out), tc.words) {
			t.Errorf("%s: %d %.300s, want %d %q", tc.path, status, raw, tc.status, tc.words)
		}
	}

	_, share, err := ce.srv.links.Create(ce.a, session.RoleControl, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	nextSid := runningAttempt(t, ce.testEnv, next, "approach-a", 2)["sessionId"].(string)
	nextTok := ce.agentToken(nextSid)
	path := "/api/runs/" + ce.run + "/comparison"
	// A token naming its own session is asked as a Brakeman turn's, since
	// the comparison is a shared read (PR-5): a lane's answers 409 lane, an
	// ordinary member's 403 not_brakeman, as on the candidates route.
	for name, c := range map[string]struct {
		tok, sid string
		status   int
	}{
		"a share link": {share, "", 401}, "no token": {"", "", 401}, "the workbench's token misspelt": {adminToken + "x", "", 401},
		"approach-a's token": {ce.aTok, "", 401}, "approach-a's token naming its session": {ce.aTok, ce.a, 409},
		"approach-b's token naming approach-a's session": {ce.bTok, ce.a, 401}, "the continuation's candidate naming its session": {nextTok, nextSid, 409},
		"an ordinary member's token naming its session": {ce.plainTok, ce.member, 403},
	} {
		var status int
		var raw []byte
		if c.sid == "" {
			status, raw, _ = ce.callAs(t, c.tok, "GET", path, nil)
		} else {
			status, raw, _ = ce.callAsTurn(t, c.tok, c.sid, "GET", path, nil)
		}
		if status != c.status || bytes.Contains(raw, []byte("approach-")) {
			t.Errorf("%s: %d %.300s, want %d", name, status, raw, c.status)
		}
	}
}

// Door 10: a candidate's session reads no evidence route with its token:
// the comparison, the candidates route and a check's log, Brakeman's
// shared reads (the comparison one since PR-5), answer the lane's token
// naming its own session 409 lane, and one naming none 401. The admin
// reads each.
func TestALaneReadsNoEvidenceRoute(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	for _, pattern := range []string{"GET /api/runs/{run}/comparison", "GET /api/runs/{run}/candidates", "GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log"} {
		for name, lane := range map[string]struct{ tok, sid string }{"approach-a": {ce.aTok, ce.a}, "approach-b": {ce.bTok, ce.b}} {
			status, raw, out := ce.callDoor(t, lane.tok, pattern, lane.sid)
			laneRefused(t, name+" on "+pattern, status, raw, out)
			method, p, _ := ce.door(pattern, lane.sid, ce.run)
			if status, raw, _ := ce.callAs(t, lane.tok, method, p, nil); status != http.StatusUnauthorized {
				t.Errorf("%s on %s naming no session: %d %.200s", name, pattern, status, raw)
			}
		}
	}
	ce.expect(t, "GET", "/api/runs/"+ce.run+"/candidates", nil, http.StatusOK, "")
	ce.expect(t, "GET", "/api/runs/"+ce.run+"/comparison", nil, http.StatusOK, "")
}

// I4: the comparison does not read the touched index. approach-a's session
// reports a file of approach-b's worktree as touched (what an agent's hooks
// say, which Conductor does not observe); the touched index shows it, and
// the comparison is the same as before, byte for byte, naming no such
// file, its independence saying what is not observed.
func TestTheComparisonReadsNoTouches(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	status, before, _ := ce.comparisonOf(t, ce.run)
	if status != http.StatusOK {
		t.Fatalf("the comparison: %d %s", status, before)
	}
	b := runningAttempt(t, ce.testEnv, ce.run, "approach-b", 1)
	touched := filepath.Join(b["worktree"].(string), "cache.go")
	if status, raw, _ := ce.callAs(t, ce.aTok, "POST", "/api/sessions/"+ce.a+"/events", map[string]any{"type": "file", "op": "read", "path": touched, "tool": "Read"}); status >= 300 {
		t.Fatalf("the touch: %d %s", status, raw)
	}
	waitUntil(t, "the touch in the index", func() bool {
		_, raw, _ := ce.call(t, "GET", "/api/touched", nil)
		return bytes.Contains(raw, []byte("cache.go"))
	})
	status, after, out := ce.comparisonOf(t, ce.run)
	if status != http.StatusOK || !bytes.Equal(before, after) || bytes.Contains(after, []byte("cache.go")) {
		t.Fatalf("the comparison after the touch:\n%s\nbefore:\n%s", after, before)
	}
	if out["independence"].(map[string]any)["notObserved"] != notObservedWords {
		t.Fatalf("the independence: %v", out["independence"])
	}
}
