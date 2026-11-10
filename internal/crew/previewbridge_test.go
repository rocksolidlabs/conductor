package crew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The preview bridge's URL and the tree's secret (PV1 PR-2): Conductor's
// own names of a tree, given in its commands' and its session's
// environment.

const testBridge = "http://127.0.0.1:8080/preview-bridge.js"

// namesService is a service that writes the names it was given to
// <id>.bridge and then <id>.secret (whole, by a rename) in its tree's state
// directory, then runs until it is stopped.
func namesService(id string) Check {
	return shStep(id, `printf %s "$CONDUCTOR_PREVIEW_BRIDGE" > "$CONDUCTOR_STATE_DIR/`+id+`.bridge"; printf %s "$CONDUCTOR_TREE_SECRET" > "$CONDUCTOR_STATE_DIR/`+id+`.secret.new"; `+
		`mv "$CONDUCTOR_STATE_DIR/`+id+`.secret.new" "$CONDUCTOR_STATE_DIR/`+id+`.secret"; while :; do sleep 0.05; done`)
}

// namesManifest serves namesService("web") on the port web, ready once the
// service wrote its names, with the preview given.
func namesManifest(preview string) Manifest {
	return Manifest{Ports: []string{"web"}, Services: []Check{namesService("web")},
		Setup: []Check{shStep("setup", `printf %s "$CONDUCTOR_PREVIEW_BRIDGE" > "$CONDUCTOR_STATE_DIR/setup.bridge"`)},
		Ready: []Probe{byFile("up", "web.secret")}, Preview: preview}
}

// readState is name in the tree's state directory, which the test waited
// for (a ready probe passed on it).
func readState(t *testing.T, tree, name string) string {
	t.Helper()
	b, err := os.ReadFile(stateFile(tree, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lastInject is the tree's names the latest session was given.
func lastInject(fl *fakeLauncher) map[string]string {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.calls[len(fl.calls)-1].inject
}

// A tree with a preview is named the bridge's URL the engine was given: its
// setup steps, its services and its session; the checkout of its result
// too, a tree of its own.
func TestTheBridgeURLReachesServices(t *testing.T) {
	dir := newRepo(t)
	e, fl, _ := managedEngine(t)
	e.SetPreviewBridge(testBridge)
	if e.PreviewBridge() != testBridge {
		t.Fatalf("the engine's bridge: %q", e.PreviewBridge())
	}
	run := launchManaged(t, e, repoManifestCrew(dir, namesManifest("http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"), work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	wt := a.Attempt.Worktree
	if got := readState(t, wt, "web.bridge"); got != testBridge {
		t.Fatalf("the service's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
	}
	if got := readState(t, wt, "setup.bridge"); got != testBridge {
		t.Fatalf("the setup step's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
	}
	if got := lastInject(fl)["CONDUCTOR_PREVIEW_BRIDGE"]; got != testBridge {
		t.Fatalf("the session's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
	}
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if got := readState(t, wt+".check", "web.bridge"); got != testBridge {
		t.Fatalf("the checkout's service's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
	}
	waitTreesEnded(t, e, run.ID)
	stopRun(t, e, run.ID)
}

// A tree without a preview is named no bridge, whatever the engine was
// given; nor is a tree with one on an engine given none (a server with no
// loopback listener, or whose embedded app has no bridge).
func TestNoBridgeWithoutAPreview(t *testing.T) {
	for _, tc := range []struct {
		name, bridge, preview string
	}{
		{"no preview", testBridge, ""},
		{"no bridge", "", "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			e, fl, _ := managedEngine(t)
			e.SetPreviewBridge(tc.bridge)
			run := launchManaged(t, e, repoManifestCrew(dir, namesManifest(tc.preview), work("build", "lead")))
			a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			if got := readState(t, a.Attempt.Worktree, "web.bridge"); got != "" {
				t.Fatalf("the service's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
			}
			if got, ok := lastInject(fl)["CONDUCTOR_PREVIEW_BRIDGE"]; ok {
				t.Fatalf("the session's CONDUCTOR_PREVIEW_BRIDGE: %q", got)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// Each tree has a secret of its own, minted as it is resolved: the
// worktree's and its result's checkout differ, each service and the
// session see their tree's in their environment, and the value is in no
// step receipt, ledger row, event, task file, attempt state or log line of
// the run. A preview names it, ${CONDUCTOR_TREE_SECRET}, and keeps the name.
func TestTheTreeSecretIsInNoReceipt(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	e.SetPreviewBridge(testBridge)
	const preview = "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/#token=${CONDUCTOR_TREE_SECRET}"
	run := launchManaged(t, e, repoManifestCrew(dir, namesManifest(preview), work("build", "lead")))
	a := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	wt := a.Attempt.Worktree
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	worktree := readState(t, wt, "web.secret")
	if !hex64.MatchString(worktree) {
		t.Fatalf("the worktree's secret: %q", worktree)
	}
	if got := lastInject(fl)["CONDUCTOR_TREE_SECRET"]; got != worktree {
		t.Fatalf("the session's secret is not its tree's")
	}
	want := fmt.Sprintf("http://127.0.0.1:%d/#token=${CONDUCTOR_TREE_SECRET}", fakePortsFirst)
	if a.Attempt.Preview != want {
		t.Fatalf("the attempt's preview: %q, want %q", a.Attempt.Preview, want)
	}
	md, _ := taskFileOf(t, a)
	taskJSON, err := os.ReadFile(filepath.Join(wt, taskDir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	commitResult(t, e, a, "built.txt", "yes\n", "built")
	waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	checkout := readState(t, wt+".check", "web.secret")
	if !hex64.MatchString(checkout) || checkout == worktree {
		t.Fatalf("the checkout's secret is not its own")
	}
	waitTreesEnded(t, e, run.ID)
	// Everything the run left: the fake ledger's rows and events, the task
	// files, the run as the engine reads it (its attempts and its log).
	l.mu.Lock()
	rows, err := json.Marshal(map[string]any{"events": fakeEvents(l.events), "runs": l.runs, "tasks": l.tasks, "attempts": l.attempts, "checks": l.checks,
		"commands": l.commands, "messages": l.messages, "prerequisites": l.prerequisites, "trees": l.trees})
	l.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := e.Get(run.ID)
	if !ok {
		t.Fatal("the run is gone")
	}
	state, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for what, b := range map[string]string{"the ledger": string(rows), "task.md": md, "task.json": string(taskJSON), "the run": string(state)} {
		for _, s := range []string{worktree, checkout} {
			if strings.Contains(b, s) {
				t.Errorf("%s holds a tree's secret", what)
			}
		}
	}
	if tr, _ := l.treeRow(run.ID, "build", 1, ledger.TreeWorktree); tr.Preview != want {
		t.Fatalf("the worktree's row keeps the preview's name: %q", tr.Preview)
	}
	if !strings.Contains(string(rows), "${CONDUCTOR_TREE_SECRET}") {
		t.Fatal("the ledger lost the preview's name")
	}
	stopRun(t, e, run.ID)
}

// fakeEvents is the events as JSON can hold them.
func fakeEvents(evs []fakeEvent) []map[string]any {
	out := make([]map[string]any, len(evs))
	for i, ev := range evs {
		out[i] = map[string]any{"run": ev.runID, "kind": ev.kind, "payload": ev.payload}
	}
	return out
}
