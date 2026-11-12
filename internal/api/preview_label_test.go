package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// previewedCrew is servedCrew whose preview names the tree's secret in its
// fragment, as a preview that hands its page a credential does.
func previewedCrew(root string) crew.Crew {
	c := servedCrew(root)
	c.ID, c.Name = "previewed", "Previewed"
	c.Manifest.Preview = "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/#token=${CONDUCTOR_TREE_SECRET}"
	return c
}

// previewed is a run whose attempt's worktree preview is open: the
// worktree, the page (the preview without its fragment) and the tree's
// secret, read from the admin's label.
type previewed struct {
	e      *testEnv
	run    string
	dir    string
	page   string
	secret string
}

// previewEnv launches previewedCrew over a repository that also holds
// files (committed), and waits for its attempt's preview.
func previewEnv(t *testing.T, files map[string]string) *previewed {
	t.Helper()
	e := readyEnv(t, nil)
	e.gitRepo(t)
	for name, body := range files {
		writeIn(t, e.root, name, body)
		gitIn(t, e.root, "add", name)
	}
	if len(files) > 0 {
		gitIn(t, e.root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "files")
	}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), previewedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	// The run's service ends with the test, before its directories go: a
	// stopped run's trees are ended, and Drain waits for their ends.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = e.srv.runs.Stop(ctx, run.ID)
		e.srv.runs.Drain(ctx)
		// The reads' indexes go as the server's Shutdown removes them.
		e.srv.trees.close()
	})
	a := runningAttempt(t, e, run.ID, "build", 1)
	p := &previewed{e: e, run: run.ID, dir: a["worktree"].(string)}
	u, secret, _ := strings.Cut(p.label(t)["url"].(string), "#token=")
	if len(secret) != 64 || strings.Contains(u, "#") {
		t.Fatalf("the admin's label URL: %q", u+"#token="+secret)
	}
	p.page, p.secret = u, secret
	return p
}

// writeIn writes the file name (slash-separated) under dir.
func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// label reads the attempt's label as the admin.
func (p *previewed) label(t *testing.T) map[string]any {
	t.Helper()
	status, data, out := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
	if status != http.StatusOK {
		t.Fatalf("the label: %d %s", status, data)
	}
	return out
}

// A label is the tree now: a file edited and a file made between two
// reads change the content and the changed paths, at the same commit.
func TestThePreviewLabelReadsTheTreeNow(t *testing.T) {
	p := previewEnv(t, nil)
	first := p.label(t)
	if first["tree"] != "worktree" || first["mutable"] != true || first["changed"] != float64(0) || first["content"] == "" || first["unread"] != nil {
		t.Fatalf("the first read: %v", first)
	}
	writeIn(t, p.dir, "README", "changed\n")
	writeIn(t, p.dir, "new.txt", "new\n")
	second := p.label(t)
	if second["commit"] != first["commit"] || second["content"] == first["content"] || second["changed"] != float64(2) ||
		fmt.Sprint(second["changedPaths"]) != "[README new.txt]" || second["unread"] != nil {
		t.Fatalf("the second read: %v (the first %v)", second, first)
	}
}

// A file a first read counted leaves a later read once an ignore rule
// covers it, HEAD unmoved: nothing an earlier read staged survives.
func TestANewIgnoreRuleLeavesTheLabel(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "junk.log", "junk\n")
	if l := p.label(t); fmt.Sprint(l["changedPaths"]) != "[junk.log]" {
		t.Fatalf("the first read: %v", l)
	}
	writeIn(t, p.dir, ".gitignore", "junk.log\n")
	ignored := p.label(t)
	if fmt.Sprint(ignored["changedPaths"]) != "[.gitignore]" || ignored["changed"] != float64(1) {
		t.Fatalf("the read after the rule: %v", ignored)
	}
	if err := os.Remove(filepath.Join(p.dir, "junk.log")); err != nil {
		t.Fatal(err)
	}
	if without := p.label(t); without["content"] != ignored["content"] {
		t.Fatalf("the content with the file ignored %v, without it %v", ignored["content"], without["content"])
	}
}

// The label's URL has the tree's secret for the admin alone; nothing but
// the label gives it, and the run's read keeps its name.
func TestTheLabelGivesTheSecretToTheAdminAlone(t *testing.T) {
	p := previewEnv(t, nil)
	path := "/api/runs/" + p.run + "/tasks/build/attempts/1/preview"
	for who, tok := range map[string]string{"none": "", "host": "test-host-token", "a stranger": "not-a-token"} {
		if status, data, _ := p.e.callAs(t, tok, "GET", path, nil); status != http.StatusUnauthorized || bytes.Contains(data, []byte(p.secret)) {
			t.Errorf("the label for %s: %d %s", who, status, data)
		}
	}
	status, data, _ := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte(p.secret)) || !bytes.Contains(data, []byte("#token=${CONDUCTOR_TREE_SECRET}")) {
		t.Fatalf("the run's read: %d %s", status, data)
	}
	if u := labelURL("http://h:1/#token="+treeSecretName, p.secret, false); u != "http://h:1/" {
		t.Fatalf("the label's URL for another than the admin: %q", u)
	}
}

// 200 changed paths of 4,000 bytes: all counted, the list cut within its
// bytes.
func TestLongChangedPathsAreCutByBytes(t *testing.T) {
	p := previewEnv(t, nil)
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var dir []string
	for len(strings.Join(dir, "/")) < 3800 {
		dir = append(dir, strings.Repeat("d", 200))
	}
	deep := strings.Join(dir, "/")
	if err := root.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		name := deep + "/" + fmt.Sprintf("%03d", i) + strings.Repeat("f", 4000-len(deep)-4)
		if err := root.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := p.label(t)
	paths, _ := json.Marshal(l["changedPaths"])
	if l["changed"] != float64(200) || l["changedCut"] != true || len(paths) > dispatch.MaxChangedPathsBytes || len(l["changedPaths"].([]any)) == 0 {
		t.Fatalf("the label: changed %v, cut %v, %d bytes of paths", l["changed"], l["changedCut"], len(paths))
	}
}

// An entry a read has been handed is not let go before the read takes its
// gate: eight other trees read meanwhile let others go, and the read then
// takes its gate.
func TestAnEntryAReadAwaitsIsNotLetGo(t *testing.T) {
	tr := &treeReads{}
	t.Cleanup(tr.close)
	handed, on := make(chan struct{}), make(chan struct{})
	tr.seam = func(at, dir string) {
		if at == "handed" && dir == "first" {
			close(handed)
			<-on
		}
	}
	done := make(chan error, 1)
	go func() {
		e, err := tr.take(t.Context(), "first")
		if err == nil {
			tr.give(e, true)
		}
		done <- err
	}()
	<-handed
	for i := range maxTreeIndexes {
		e, err := tr.take(t.Context(), fmt.Sprintf("tree-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		tr.give(e, true)
	}
	tr.mu.Lock()
	first, n, evicted := tr.entries["first"], len(tr.entries), tr.entries["tree-0"] == nil
	tr.mu.Unlock()
	if first == nil || len(first.gate) != 0 || n != maxTreeIndexes || !evicted {
		t.Fatalf("the awaited entry: %v, %d entries, tree-0 let go %v", first, n, evicted)
	}
	close(on)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// With eight entries held, a read of a ninth tree makes no ninth: it
// waits within its own time and is answered without a read when that
// ends, and takes an entry once one is let go.
func TestANinthTreeWaitsWhileEightAreHeld(t *testing.T) {
	tr := &treeReads{}
	t.Cleanup(tr.close)
	var held []*treeEntry
	for i := range maxTreeIndexes {
		e, err := tr.take(t.Context(), fmt.Sprintf("tree-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, e)
	}
	waiting := make(chan struct{}, maxTreeIndexes)
	tr.seam = func(at, dir string) {
		if at == "waiting" {
			waiting <- struct{}{}
		}
	}
	entries := func() int {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		return len(tr.entries)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := tr.take(ctx, "ninth")
		done <- err
	}()
	<-waiting
	if n := entries(); n != maxTreeIndexes {
		t.Fatalf("%d entries while the ninth waits", n)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the ninth when its time ended: %v", err)
	}
	go func() {
		e, err := tr.take(t.Context(), "ninth")
		if err == nil {
			tr.give(e, true)
		}
		done <- err
	}()
	<-waiting
	tr.give(held[0], true)
	if err := <-done; err != nil || entries() != maxTreeIndexes {
		t.Fatalf("the ninth once an entry was let go: %v, %d entries", err, entries())
	}
}

// A read that begins once the server's stop has begun makes no folder and
// no index; the stop removes the ones made before.
func TestAReadAfterTheStopMakesNothing(t *testing.T) {
	tr := &treeReads{}
	e, err := tr.take(t.Context(), "tree")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ix.Path, []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.give(e, true)
	dir := tr.dir
	tr.close()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the reads' folder after the stop: %v", err)
	}
	for _, name := range []string{"tree", "another"} {
		if _, err := tr.take(t.Context(), name); !errors.Is(err, errReadsStopped) || tr.dir != "" || tr.entries != nil {
			t.Fatalf("a read of %s after the stop: %v, folder %q", name, err, tr.dir)
		}
	}
}

// The label is the admin's: a share link's viewer and controller, an
// agent's session token, a host token and a Brakeman turn are refused 401,
// and a switchyard answers 403.
func TestThePreviewLabelIsTheAdminsAlone(t *testing.T) {
	b := brakemanEnv(t, nil)
	sid := b.createSession("cat")
	tokens := map[string]string{"none": "", "session": b.agentToken(sid), "host": "test-host-token"}
	for _, role := range []string{"view", "control"} {
		status, data, out := b.callAs(t, adminToken, "POST", "/api/sessions/"+sid+"/links", map[string]any{"role": role})
		if status != http.StatusCreated {
			t.Fatalf("a %s link: %d %s", role, status, data)
		}
		tokens["link "+role] = out["token"].(string)
	}
	b.enable(t, "bk", nil)
	bsid, btok, _ := b.turnSession(t, b.briefing(t))
	path := "/api/runs/r-1/tasks/build/attempts/1/preview"
	for who, tok := range tokens {
		if status, data, _ := b.callAs(t, tok, "GET", path, nil); status != http.StatusUnauthorized {
			t.Errorf("the label with %s: %d %s", who, status, data)
		}
	}
	if status, data, _ := b.callAsTurn(t, btok, bsid, "GET", path, nil); status != http.StatusUnauthorized {
		t.Errorf("the label as a Brakeman turn: %d %s", status, data)
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if status, data, out := yard.callAs(t, adminToken, "GET", path, nil); status != http.StatusForbidden || codeOf(out) != "switchyard" {
		t.Errorf("the label on a switchyard: %d %s", status, data)
	}
}
