package api

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// The helper serves HTTP on the loopback at the port CONDUCTOR_PORT_WEB
// names, as a project's dev server would: a service of a tree. It runs only
// when its arguments say so (after --), never in a test run.
func TestHelperServesHTTP(t *testing.T) {
	if args := flag.Args(); len(args) != 1 || args[0] != "conductor-test-http" {
		t.Skip("a helper")
	}
	srv := &http.Server{Addr: "127.0.0.1:" + os.Getenv("CONDUCTOR_PORT_WEB"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exit" {
			// The service ends on its own, as a dev server that crashed.
			os.Exit(3)
		}
		io.WriteString(w, "served "+r.URL.Path+"\n")
	})}
	srv.ListenAndServe()
	os.Exit(0)
}

// servedCrew is a managed crew of one task whose project manifest runs the
// helper as its service on its port web, probes it over HTTP and offers it
// as the preview.
func servedCrew(root string) crew.Crew {
	return crew.Crew{ID: "served", Name: "Served", Goal: "show it", Cwd: root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks:   []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it."}},
		Manifest: &crew.Manifest{Ports: []string{"web"},
			Services: []crew.Check{{ID: "web", Argv: []string{os.Args[0], "-test.run=^TestHelperServesHTTP$", "--", "conductor-test-http"}}},
			Ready:    []crew.Probe{{ID: "page", URL: "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready", IntervalSeconds: 1}},
			Preview:  "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"}}
}

// previewPattern is a preview URL on the loopback, the port drawn.
var previewPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/$`)

// servicePid waits up to 10 s for the pid a service writes to path, a
// positive one, and returns it: a tree with no ready probe is ready once
// its service's process exists, before its shell has necessarily written
// the file (Codex's review of #441, round 1).
func servicePid(t *testing.T, path string) int {
	t.Helper()
	pid := 0
	if !waitFor(10*time.Second, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && n > 0
	}) {
		t.Fatalf("no pid in %s after 10 s", path)
	}
	return pid
}

// A tree's service, its probe and the preview, through the server (EN1
// PR-4): the service, a real HTTP server on the tree's port, answers the
// probe over HTTP; the run's read offers the attempt's preview, which
// answers; the steps route has the service's row running and the probe's
// with its expanded URL as its command. The run stopped, the preview is
// gone and the service's row finished; the routes answer the same once the
// engine has let the run go.
func TestAServiceItsProbeAndThePreviewReadThroughTheRoutes(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), servedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	preview, _ := a["preview"].(string)
	if !previewPattern.MatchString(preview) {
		t.Fatalf("the attempt's preview: %v", a)
	}
	resp, err := http.Get(preview)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "served /\n" {
		t.Fatalf("the preview answered %d %q", resp.StatusCode, body)
	}
	byID := func(steps []map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, s := range steps {
			out[s["id"].(string)] = s
		}
		return out
	}
	steps := byID(stepsOf(t, e, run.ID, "build", 1))
	if web := steps["web"]; web == nil || web["phase"] != "service" || web["state"] != "running" {
		t.Fatalf("the service's receipt: %v", steps)
	}
	if page := steps["page"]; page == nil || page["phase"] != "ready" || page["state"] != "finished" || page["url"] != preview+"ready" || page["argv"] != nil || page["tries"].(float64) < 1 {
		t.Fatalf("the probe's receipt: %v", steps)
	}
	resp2, out := e.do("POST", "/api/runs/"+run.ID+"/stop", adminToken, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp2.StatusCode, out)
	}
	stopped := out["run"].(map[string]any)
	attempt := stopped["tasks"].([]any)[0].(map[string]any)["attempt"].(map[string]any)
	if attempt["preview"] != nil || stopped["cleaning"] != nil {
		t.Fatalf("the run as the stop answers it: preview %v, cleaning %v", attempt["preview"], stopped["cleaning"])
	}
	if web := byID(stepsOf(t, e, run.ID, "build", 1))["web"]; web["state"] != "finished" {
		t.Fatalf("the service's receipt after the stop: %v", web)
	}
	e.srv.runs.ForgetForTest(run.ID)
	after := byID(stepsOf(t, e, run.ID, "build", 1))
	if after["web"]["state"] != "finished" || after["page"]["url"] != preview+"ready" {
		t.Fatalf("the receipts once the engine let the run go: %v", after)
	}
}

// A server that ends with a service running (its ledger closed under it,
// as the owner's death leaves it) is followed by one whose start names the
// service in its log as possibly alive, with its pid, and signals nothing:
// the process is still there after the start.
func TestTheNextStartNamesAServiceTheLastServerLeftRunning(t *testing.T) {
	var logs logBuffer
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(&logs, nil)), []catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}})
	e.gitRepo(t)
	pidFile := t.TempDir() + "/pid"
	c := servedCrew(e.root)
	c.Manifest = &crew.Manifest{Services: []crew.Check{{ID: "web", Argv: []string{"sh", "-c", `echo $$ > "` + pidFile + `"; while :; do sleep 0.05; done`}}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	n := servicePid(t, pidFile)
	pid := strconv.Itoa(n)
	// The owner ends here: its ledger closed with the service running.
	if err := e.srv.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	again := e.restart()
	logs.wait(t, "a service the last server started may still be running")
	line := ""
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "a service the last server started may still be running") {
			line = l
		}
	}
	for _, want := range []string{"run=" + run.ID, "task=build", "attempt=1", "tree=worktree", "service=web", "pid=" + pid, "group=" + pid, "startedAt="} {
		if !strings.Contains(line, want) {
			t.Fatalf("the start's log line has no %s: %s", want, line)
		}
	}
	if syscall.Kill(n, 0) != nil {
		t.Fatalf("the service %s was signalled at the next start", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	again.srv.Shutdown(ctx)
	// The first server's engine still holds the service: its shutdown
	// stops it (its ledger gone, nothing more is recorded).
	e.srv.Shutdown(ctx)
	if syscall.Kill(n, 0) == nil {
		t.Fatalf("the service %s outlived its server's shutdown", pid)
	}
}
