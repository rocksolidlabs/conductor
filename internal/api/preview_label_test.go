package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
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
	return previewEnvLogging(t, files, io.Discard)
}

// previewEnvLogging is previewEnv with the server's log written to logs.
func previewEnvLogging(t *testing.T, files map[string]string, logs io.Writer) *previewed {
	t.Helper()
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(logs, nil)),
		[]catalog.Agent{{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}})
	e.gitRepo(t)
	for name, body := range files {
		writeIn(t, e.root, name, body)
		gitIn(t, e.root, "add", name)
	}
	if len(files) > 0 {
		gitIn(t, e.root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "files")
	}
	return launchPreview(t, e, previewedCrew(e.root))
}

// launchPreview launches c in e and waits for its attempt's preview.
func launchPreview(t *testing.T, e *testEnv, c crew.Crew) *previewed {
	t.Helper()
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
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

// The label answers the tree's secret in its URL and nowhere else: a file
// whose name holds it, in either case, is named with the secret's name.
func TestTheLabelsChangedPathsHoldNoSecret(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "a-"+p.secret+".txt", "x\n")
	writeIn(t, p.dir, "b-"+strings.ToUpper(p.secret)+".txt", "x\n")
	status, data, out := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
	rest := strings.Replace(string(data), p.page+"#token="+p.secret, "", 1)
	if status != http.StatusOK || strings.Contains(strings.ToLower(rest), p.secret) ||
		fmt.Sprint(out["changedPaths"]) != "[a-${CONDUCTOR_TREE_SECRET}.txt b-${CONDUCTOR_TREE_SECRET}.txt]" {
		t.Fatalf("the label: %d %s", status, data)
	}
}

// A changed path whose name holds the secret of ANOTHER tree the engine
// holds is named with the secret's name in the first tree's label.
func TestTheLabelsChangedPathsHoldNoOtherTreesSecret(t *testing.T) {
	p := previewEnv(t, nil)
	c := previewedCrew(p.e.root)
	c.ID, c.Name = "previewed-too", "Previewed too"
	q := launchPreview(t, p.e, c)
	if q.secret == p.secret {
		t.Fatal("the two trees have one secret")
	}
	writeIn(t, p.dir, "c-"+q.secret+".txt", "x\n")
	status, data, out := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
	if status != http.StatusOK || strings.Contains(strings.ToLower(string(data)), q.secret) ||
		fmt.Sprint(out["changedPaths"]) != "[c-${CONDUCTOR_TREE_SECRET}.txt]" {
		t.Fatalf("the first tree's label: %d %s", status, data)
	}
}

// A tree whose .git is gone, inside the run's checkout, is not read as
// that checkout: git looks for a repository in the tree alone, the label
// is unread and the log names the kind.
func TestALabelOfATreeWithNoGitOfItsOwnIsUnread(t *testing.T) {
	logs := &logBuffer{}
	p := previewEnvLogging(t, nil, logs)
	dotGit, aside := filepath.Join(p.dir, ".git"), filepath.Join(p.e.root, "dotgit-aside")
	if err := os.Rename(dotGit, aside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(aside, dotGit) })
	writeIn(t, p.dir, "new.txt", "x\n")
	if l := p.label(t); l["unread"] != "the tree could not be read" || l["commit"] != "" || l["content"] != "" || l["changed"] != float64(0) {
		t.Fatalf("the label: %v", l)
	}
	if !strings.Contains(logs.String(), `why="not a repository"`) {
		t.Fatalf("the log: %s", logs.String())
	}
}

// labelAnswer is the label's answer as labelAsync got it.
type labelAnswer struct {
	status int
	body   []byte
	err    error
}

// labelAsync asks the attempt's label as the admin and gives its answer
// on the returned channel.
func (p *previewed) labelAsync() <-chan labelAnswer {
	done := make(chan labelAnswer, 1)
	go func() {
		req, err := http.NewRequest("GET", p.e.http.URL+"/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
		if err != nil {
			done <- labelAnswer{err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := p.e.client.Do(req)
		if err != nil {
			done <- labelAnswer{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		done <- labelAnswer{resp.StatusCode, b, err}
	}()
	return done
}

// holdAfter holds the first git command named sub of a tree's read once
// it has run, until release is closed; at is closed when it is held.
func holdAfter(t *testing.T, sub string) (at, release chan struct{}) {
	at, release = make(chan struct{}), make(chan struct{})
	held := false
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		err := gitcli.RunTree(ctx, workTree, env, in, w, args...)
		if args[0] == sub && !held {
			held = true
			close(at)
			<-release
		}
		return err
	}))
	return at, release
}

// unreadLabel fails the test unless a is a label that is unread, with no
// content and no changed path.
func unreadLabel(t *testing.T, what string, a labelAnswer) {
	t.Helper()
	var l map[string]any
	if a.err != nil || a.status != http.StatusOK || json.Unmarshal(a.body, &l) != nil {
		t.Fatalf("%s: the label: %d %s %v", what, a.status, a.body, a.err)
	}
	if l["unread"] != "the tree could not be read" || l["content"] != "" || l["changed"] != float64(0) || fmt.Sprint(l["changedPaths"]) != "[]" {
		t.Fatalf("%s: the label: %s", what, a.body)
	}
}

// A read held after any of its commands (rev-parse, read-tree, add,
// write-tree, the diff) while the server's stop begins (close marks it
// and removes the indexes) is unread once it is let go: a later command
// fails with the index gone, or the stop's mark is asked after the last.
func TestAReadThatEndsAfterTheStopBeganIsUnread(t *testing.T) {
	logs := &logBuffer{}
	p := previewEnvLogging(t, nil, logs)
	for _, sub := range []string{"rev-parse", "read-tree", "add", "write-tree", "diff"} {
		at, release := holdAfter(t, sub)
		done := p.labelAsync()
		select {
		case <-at:
		case a := <-done:
			t.Fatalf("after %s: answered before it was held: %d %s %v", sub, a.status, a.body, a.err)
		}
		p.e.srv.trees.close()
		close(release)
		unreadLabel(t, "the read held after "+sub+" across the stop", <-done)
		// The next row's read begins before a stop.
		p.e.srv.trees.mu.Lock()
		p.e.srv.trees.stopped = false
		p.e.srv.trees.mu.Unlock()
	}
	if !strings.Contains(logs.String(), `why=refused`) {
		t.Fatalf("the log: %s", logs.String())
	}
}

// A read held at the runner's seam while the server's Shutdown begins and
// is held in its waits (a run start under way, which it waits for before
// it lists the runs to stop) is unread once let go: the stop is marked at
// Shutdown's entry, before any wait.
func TestAReadThatEndsWhileTheShutdownWaitsIsUnread(t *testing.T) {
	p := previewEnv(t, nil)
	at, release := holdAfter(t, "write-tree")
	done := p.labelAsync()
	select {
	case <-at:
	case a := <-done:
		t.Fatalf("answered before it was held: %d %s %v", a.status, a.body, a.err)
	}
	launches := &p.e.srv.launches
	launches.starting.Add(1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p.e.srv.Shutdown(ctx)
	}()
	waiting := waitFor(30*time.Second, func() bool { return gateSeam(launches, &launches.closed) })
	close(release)
	a := <-done
	launches.starting.Done()
	<-stopped
	if !waiting {
		t.Fatal("the shutdown never reached its wait for the runs being started")
	}
	unreadLabel(t, "the read that ended while the shutdown waited", a)
}

// A named pipe put in place of the tree's folder between the walk's look
// and its open is refused at once: the label is unread and the log says
// refused.
func TestALabelOfATreeMadeAPipeAtItsOpenIsUnread(t *testing.T) {
	logs := &logBuffer{}
	p := previewEnvLogging(t, nil, logs)
	aside, placed := p.dir+"-aside", false
	t.Cleanup(crew.SetTreeOpenForTest(func(name string) {
		if name != filepath.Base(p.dir) || placed {
			return
		}
		placed = true
		if err := os.Rename(p.dir, aside); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(p.dir, 0o600); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		if placed {
			_ = os.Remove(p.dir)
			_ = os.Rename(aside, p.dir)
		}
	})
	done := p.labelAsync()
	var a labelAnswer
	select {
	case a = <-done:
	case <-time.After(time.Minute):
		t.Errorf("the label was not answered within a minute: its read's open waits on the pipe at %s", p.dir)
		if w, err := os.OpenFile(p.dir, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		a = <-done
	}
	if !placed {
		t.Fatal("the walk never reached the tree's folder")
	}
	unreadLabel(t, "the label of a pipe", a)
	if !strings.Contains(logs.String(), `why=refused`) {
		t.Fatalf("the log: %s", logs.String())
	}
}

// A read git fails in an ordinary way (a file it cannot open) is logged
// in fixed words, nothing of git's message, and the label says the tree
// could not be read.
func TestAFailedReadIsLoggedInFixedWords(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	logs := &logBuffer{}
	p := previewEnvLogging(t, nil, logs)
	writeIn(t, p.dir, "closed.txt", "x\n")
	path := filepath.Join(p.dir, "closed.txt")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if l := p.label(t); l["unread"] != "the tree could not be read" || l["content"] != "" {
		t.Fatalf("the label: %v", l)
	}
	got := logs.String()
	i := strings.Index(got, "the preview label's tree was not read")
	if i < 0 {
		t.Fatalf("no line for the read: %s", got)
	}
	line, _, _ := strings.Cut(got[i:], "\n")
	if !strings.Contains(line, `why="git failed"`) || strings.Contains(line, "closed.txt") || strings.Contains(line, "Permission") || strings.Contains(line, "git add") {
		t.Fatalf("the line: %s", line)
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
