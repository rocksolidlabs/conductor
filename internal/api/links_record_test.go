package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run stopped and then kept only as its record, after a restart of the
// server: its page opens from the record, and its links' routes refuse with
// the words why no link is made (409 run_record), never "no such run" for a
// run whose page opens. Nothing is made, listed or handed out: the reply
// carries no link, token or address, the store holds no link of the run,
// and a link made before the restart opens nothing. A run with no record
// is still 404 not_found.
func TestARunKeptOnlyAsItsRecordRefusesALinkWithWhy(t *testing.T) {
	e := newTestEnv(t, nil)
	e.stopEverything(t)
	runID := e.launchCrew(t, "kept", catMember("solo", "immediately"))
	e.waitRunning(t, runID, "solo")
	resp, out := e.do("POST", "/api/runs/"+runID+"/links", adminToken, map[string]any{"role": "view"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a link while the run is held: %d %v", resp.StatusCode, out)
	}
	before, _ := out["token"].(string)
	if resp, out := e.do("POST", "/api/runs/"+runID+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	file := filepath.Join(e.srv.cfg.DataDir, "runs", runID+".json")
	until(t, "the stopped run's record", func() bool {
		b, err := os.ReadFile(file)
		return err == nil && strings.Contains(string(b), `"stoppedAt"`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	again := e.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })

	// The test's premise: the engine holds no run of that id, the record is read.
	if _, held := again.srv.runs.Get(runID); held {
		t.Fatal("the restarted engine holds the stopped run")
	}
	resp, out = again.do("GET", "/api/runs/"+runID, adminToken, nil)
	if resp.StatusCode != http.StatusOK || out["record"] != true {
		t.Fatalf("the run's page after the restart: %d %v", resp.StatusCode, out)
	}

	for _, c := range []struct {
		what, method string
		body         any
	}{
		{"a view link", "POST", map[string]any{"role": "view"}},
		{"a control link", "POST", map[string]any{"role": "control", "label": "phone"}},
		{"a decision link", "POST", map[string]any{"role": "control", "decide": true}},
		{"the list", "GET", nil},
	} {
		resp, out := again.do(c.method, "/api/runs/"+runID+"/links", adminToken, c.body)
		wantAPIError(t, c.what, resp, out, http.StatusConflict, "run_record", runRecordNoLink)
		for _, k := range []string{"link", "links", "token", "url", "invite", "remote", "rendezvous"} {
			if _, ok := out[k]; ok {
				t.Errorf("%s: the refusal carries %q: %v", c.what, k, out)
			}
		}
	}
	if links := again.srv.links.ListByRun(runID); len(links) != 0 {
		t.Fatalf("links held for the record's run: %v", links)
	}
	if code, out := joinStatus(t, again, before); code != http.StatusNotFound {
		t.Fatalf("the link made before the restart: %d %v", code, out)
	}
	// Still only an admin's: the refusal says nothing to anyone else.
	if resp, out := again.do("POST", "/api/runs/"+runID+"/links", before, map[string]any{"role": "view"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("with the old link's token: %d %v", resp.StatusCode, out)
	}
	resp, out = again.do("POST", "/api/runs/nope-00000000/links", adminToken, map[string]any{"role": "view"})
	wantAPIError(t, "a run with no record", resp, out, http.StatusNotFound, "not_found", "no such run")
	resp, out = again.do("GET", "/api/runs/nope-00000000/links", adminToken, nil)
	wantAPIError(t, "the list of a run with no record", resp, out, http.StatusNotFound, "not_found", "no such run")
}
