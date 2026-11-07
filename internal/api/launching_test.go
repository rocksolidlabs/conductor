package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// An agent reports as its process starts, before the registry holds its
// session (its record is begun in between, a write synced to disk): the
// report waits for the registration and is applied, never answered 404
// and lost (the e2e asker's question, CI on #684). The launch is held at
// its "started" seam, the process's token read from the file it writes,
// and the report sent; the launch is let go only once the report is
// seen waiting (launchSeam's "reportWaits"), so a report answered at once
// fails every run.
func TestAReportAsItsProcessStartsWaitsForTheRegistration(t *testing.T) {
	e := newTestEnv(t, nil)
	tokFile := filepath.Join(t.TempDir(), "token")
	cat, err := catalog.Load(catalog.File{DisableDefaults: true, Agents: []catalog.Agent{
		{ID: "early", Name: "early", Command: []string{"/bin/sh", "-c", `printf %s "$CONDUCTOR_NOTIFY_TOKEN" > "$1.tmp" && mv "$1.tmp" "$1"; exec /bin/cat`, "sh", tokFile}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.setCatalog(cat)

	started := make(chan string, 1)
	gate := make(chan struct{})
	waits := make(chan string, 4)
	var held, released sync.Once
	release := func() { released.Do(func() { close(gate) }) }
	t.Cleanup(release)
	hook := func(stage, id string) {
		switch stage {
		case "started":
			held.Do(func() {
				started <- id
				<-gate
			})
		case "reportWaits":
			waits <- id
		}
	}
	e.srv.launchHook.Store(&hook)
	t.Cleanup(func() { e.srv.launchHook.Store(nil) })

	post := func(path, token string, body any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.http.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			return -1
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	created := make(chan int, 1)
	go func() { created <- post("/api/sessions", adminToken, map[string]any{"agentId": "early"}) }()
	id := <-started
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(t.Context())
		}
	})
	if _, ok := e.srv.registry.Get(id); ok {
		t.Fatal("the registry held the session at its process's start: the window this test is about is gone")
	}

	// The process's token, once the process has written it (a file not yet
	// renamed into place is not read).
	var token string
	for deadline := time.Now().Add(30 * time.Second); token == ""; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(tokFile); err == nil {
			token = string(b)
		} else if time.Now().After(deadline) {
			t.Fatal("the process never wrote its token")
		}
	}

	reported := make(chan int, 1)
	go func() {
		reported <- post("/api/sessions/"+id+"/attention", token, map[string]any{"state": "needs_input", "message": "Which database?"})
	}()
	select {
	case got := <-waits:
		if got != id {
			t.Fatalf("a report waited for %s, not %s", got, id)
		}
	case code := <-reported:
		t.Fatalf("the report was answered %d while its session's launch was under way: it waits for the registration", code)
	}
	release()
	if code := <-created; code != http.StatusCreated {
		t.Fatalf("the launch: %d", code)
	}
	if code := <-reported; code != http.StatusOK {
		t.Fatalf("the report, once the session was registered: %d", code)
	}
	d, ok := e.srv.registry.Get(id)
	if !ok {
		t.Fatal("the session is not in the registry")
	}
	if a := d.Info().Attention; a.State != session.AttentionNeedsInput || a.Message != "Which database?" {
		t.Fatalf("the session's attention: %+v", a)
	}
	// The mark ends with the launch: a report for the session finds it at
	// once, and one for an id never launched is a 404, waiting for nothing.
	if e.srv.starting.under(id) != nil {
		t.Fatal("the launch's mark outlived the launch")
	}
	if code := post("/api/sessions/"+session.NewID()+"/attention", token, map[string]any{"state": "working"}); code != http.StatusNotFound {
		t.Fatalf("a report for no session: %d", code)
	}
	select {
	case got := <-waits:
		t.Fatalf("a report waited on %s after the launch ended", got)
	default:
	}
}
