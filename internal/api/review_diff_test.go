package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/gitrepo"
)

// A candidate's diff (docs/plans/review-repair.md, RV1 PR-3): the files a
// managed run's candidate changed against a base the run names, and each
// side of one file, read by commit id from the run's repository.

// diffPath is the diff route of the run's task's attempt n.
func diffPath(run, task string, n int) string {
	return fmt.Sprintf("/api/runs/%s/tasks/%s/attempts/%d/diff", run, task, n)
}

// diffFileOf reads a file of the diff route as the admin: its status, its
// body and its headers.
func diffFileOf(t *testing.T, e *testEnv, run, task string, n int, query string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest("GET", e.http.URL+diffPath(run, task, n)+"/file?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b, resp.Header
}

// succeededCandidate launches CND-1 (its check passes) and has attempt 1
// commit what write makes in its worktree and submit it; it returns the
// run, the attempt's worktree and its result commit once the ledger has
// the attempt succeeded.
func succeededCandidate(t *testing.T, l *launchTest, write func(dir string) string) (run, commit string) {
	t.Helper()
	run = entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	a := runningAttempt(t, l.testEnv, run, "build", 1)
	commit = write(a["worktree"].(string))
	l.submit(t, a["sessionId"].(string))
	if row := ledgerAttemptIn(t, l.srv, run, "build", 1, ledger.AttemptSucceeded); row.ResultCommit != commit {
		t.Fatalf("attempt 1's result %s, committed %s", row.ResultCommit, commit)
	}
	return run, commit
}

// gitRun runs git in dir for a test's setup.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitIn(t, dir, append([]string{"-c", "commit.gpgsign=false"}, args...)...)
}

// The diff of a candidate against the run's base, by default and asked
// for, and against the attempt's start: every file it changed with its
// counts, renames found, the reply naming both commits.
func TestCandidateDiffAgainstTheRunsBase(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string {
		commitFile(t, dir, "a.txt", "one\ntwo\n")
		commitFile(t, dir, "README", "hello\nworld\n")
		gitRun(t, dir, "mv", "a.txt", "b.txt")
		gitRun(t, dir, "commit", "-q", "-m", "rename")
		return gitIn(t, dir, "rev-parse", "HEAD")
	})
	want := map[string]any{"candidate": c1, "base": l.head, "truncated": false, "files": []any{
		map[string]any{"path": "README", "status": "M", "added": 1.0, "removed": 0.0, "binary": false},
		map[string]any{"path": "b.txt", "status": "A", "added": 2.0, "removed": 0.0, "binary": false},
	}}
	for _, q := range []string{"", "?base=" + l.head} {
		if got := l.expect(t, "GET", diffPath(run, "build", 1)+q, nil, http.StatusOK, ""); !reflect.DeepEqual(got, want) {
			t.Fatalf("the diff%s:\n%v\nwant\n%v", q, got, want)
		}
	}
	// A rename between two results of the chain is one file.
	l2 := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Tasks[0].Checks = []crew.Check{{ID: "test", Argv: []string{"test", "-f", "fixed"}}}
	})
	run2 := entityOf(t, l2.launch(t, l2.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	a1 := runningAttempt(t, l2.testEnv, run2, "build", 1)
	r1 := commitFile(t, a1["worktree"].(string), "old.txt", strings.Repeat("a line that stays\n", 10))
	l2.submit(t, a1["sessionId"].(string))
	ledgerAttemptIn(t, l2.srv, run2, "build", 1, ledger.AttemptFailed)
	a2 := runningAttempt(t, l2.testEnv, run2, "build", 2)
	if a2["start"] != r1 {
		t.Fatalf("attempt 2 starts at %v, attempt 1's result is %s", a2["start"], r1)
	}
	dir := a2["worktree"].(string)
	gitRun(t, dir, "mv", "old.txt", "new.txt")
	r2 := commitFile(t, dir, "fixed", "yes\n")
	l2.submit(t, a2["sessionId"].(string))
	ledgerAttemptIn(t, l2.srv, run2, "build", 2, ledger.AttemptSucceeded)
	since := l2.expect(t, "GET", diffPath(run2, "build", 2)+"?base="+r1, nil, http.StatusOK, "")
	if since["candidate"] != r2 || since["base"] != r1 || !reflect.DeepEqual(since["files"], []any{
		map[string]any{"path": "fixed", "status": "A", "added": 1.0, "removed": 0.0, "binary": false},
		map[string]any{"path": "new.txt", "from": "old.txt", "status": "R", "added": 0.0, "removed": 0.0, "binary": false},
	}) {
		t.Fatalf("since attempt 1 (its start): %v", since)
	}
}

// A base the run never named is refused before git is asked: a commit of
// the repository outside the run's chain, an id that is not one in full, a
// revision; so are a base given twice and a parameter the route does not
// take. The run's base, the attempt's start and a result of the task in
// the chain are each taken.
func TestCandidateDiffRefusesABaseOutsideTheChain(t *testing.T) {
	l, run, _, commits := failingLaunch(t)
	for _, base := range []string{l.head, commits[0], commits[1]} {
		if out := l.expect(t, "GET", diffPath(run, "build", 3)+"?base="+base, nil, http.StatusOK, ""); out["base"] != base || out["candidate"] != commits[2] {
			t.Fatalf("base %s: %v", base, out)
		}
	}
	side := commitFile(t, l.repo, "side.txt", "not the run's\n")
	for _, q := range []string{"base=" + side, "base=" + strings.Repeat("ab", 20), "base=HEAD", "base=" + side[:12], "base=" + strings.ToUpper(side),
		"base=" + l.head + "&base=" + l.head, "bogus=1", "base=" + url.QueryEscape(side+"^")} {
		l.expect(t, "GET", diffPath(run, "build", 3)+"?"+q, nil, http.StatusBadRequest, "invalid_request")
		l.expect(t, "GET", diffPath(run, "build", 3)+"/file?path=README&side=base&"+q, nil, http.StatusBadRequest, "invalid_request")
	}
}

// A file of the diff at each side: its bytes, the commit read in
// X-Conductor-Commit; an added file's base side 404; a file past 1 MiB cut
// at it, X-Conductor-Truncated saying so; a binary file 415; a link its
// target's text as git keeps it, never followed.
func TestDiffFileReadsEachSide(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	big := strings.Repeat("0123456789abcde\n", gitcli.MaxShowBytes/16+4)
	run, c1 := succeededCandidate(t, l, func(dir string) string {
		commitFile(t, dir, "README", "hello\nworld\n")
		commitFile(t, dir, "big.txt", big)
		if err := os.WriteFile(filepath.Join(dir, "bin"), []byte{'x', 0, 'y'}, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
		return commitFile(t, dir, "a.txt", "one\n")
	})
	for _, c := range []struct {
		query, body, commit string
		truncated           bool
	}{
		{"path=a.txt&side=candidate", "one\n", c1, false},
		{"path=README&side=candidate", "hello\nworld\n", c1, false},
		{"path=README&side=base", "hello\n", l.head, false},
		{"side=base&path=README&base=" + l.head, "hello\n", l.head, false},
		{"path=big.txt&side=candidate", big[:gitcli.MaxShowBytes], c1, true},
		{"path=link&side=candidate", "/etc/hostname", c1, false},
	} {
		status, body, h := diffFileOf(t, l.testEnv, run, "build", 1, c.query)
		if status != http.StatusOK || string(body) != c.body || h.Get(commitHeader) != c.commit || (h.Get(truncatedHeader) == "1") != c.truncated ||
			!strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q %v", c.query, status, cutRunes(string(body), 80), h)
		}
	}
	for _, c := range []struct {
		query, code string
		status      int
	}{
		{"path=a.txt&side=base", "not_found", http.StatusNotFound},
		{"path=nope.txt&side=candidate", "not_found", http.StatusNotFound},
		{"path=bin&side=candidate", "binary", http.StatusUnsupportedMediaType},
	} {
		status, body, _ := diffFileOf(t, l.testEnv, run, "build", 1, c.query)
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		if status != c.status || errorCode(out) != c.code {
			t.Fatalf("%s: %d %s", c.query, status, body)
		}
	}
}

// A path that could name anything outside the commit's tree is refused
// before git sees it ('..', a leading '/', a NUL, an empty, '.' or doubled
// part, past 4096 bytes), and so is a side that is neither; a folder is no
// file.
func TestDiffFileRefusesAPathOutsideTheTree(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, _ := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "sub/a.txt", "one\n") })
	for _, p := range []string{"../README", "/etc/passwd", "sub/../README", "./README", "sub//a.txt", "sub/", "", "a\x00b", "..", ".", strings.Repeat("a", 4097)} {
		l.expect(t, "GET", diffPath(run, "build", 1)+"/file?side=candidate&path="+url.QueryEscape(p), nil, http.StatusBadRequest, "invalid_request")
	}
	for _, q := range []string{"path=README", "path=README&side=both", "path=README&side=candidate&side=base", "path=README&path=README&side=base", "path=README&side=base&x=1"} {
		l.expect(t, "GET", diffPath(run, "build", 1)+"/file?"+q, nil, http.StatusBadRequest, "invalid_request")
	}
	l.expect(t, "GET", diffPath(run, "build", 1)+"/file?side=candidate&path=sub", nil, http.StatusNotFound, "not_found")
	if status, body, _ := diffFileOf(t, l.testEnv, run, "build", 1, "side=candidate&path=sub/a.txt"); status != http.StatusOK || string(body) != "one\n" {
		t.Fatalf("sub/a.txt: %d %q", status, body)
	}
}

// A commit no longer in the repository answers 410 commit_gone, on both
// routes; a run, an attempt or a result the ledger has not 404; a
// repository the server cannot read now 409 repo_unavailable.
func TestDiffOfACommitGoneIs410(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	gone := strings.Repeat("ab", 20)
	put := func(id, cwd, result string) {
		c, _ := json.Marshal(crew.Crew{ID: "synth", Name: "Synth", Cwd: cwd, Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork}}})
		putLedgerRun(t, l.srv.ledger, ledger.RunRow{ID: id, CrewID: "synth", Crew: string(c), Base: l.head, State: ledger.RunFinished})
		if _, err := l.srv.ledger.Transition(context.Background(), id, func(tx *ledger.Tx) error {
			if err := tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskFailed, Attempts: 2}); err != nil {
				return err
			}
			if err := tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: 1, Member: "lead", StartCommit: l.head, ResultCommit: result, State: ledger.AttemptFailed, Where: ledger.WhereNative}); err != nil {
				return err
			}
			return tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: 2, Member: "lead", StartCommit: l.head, State: ledger.AttemptCancelled, Where: ledger.WhereNative})
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("synth-0000abcd", l.repo, gone)
	l.expect(t, "GET", diffPath("synth-0000abcd", "build", 1), nil, http.StatusGone, "commit_gone")
	l.expect(t, "GET", diffPath("synth-0000abcd", "build", 1)+"/file?path=README&side=candidate", nil, http.StatusGone, "commit_gone")
	// Its base is there: the base's side reads.
	if status, body, h := diffFileOf(t, l.testEnv, "synth-0000abcd", "build", 1, "path=README&side=base"); status != http.StatusOK || string(body) != "hello\n" || h.Get(commitHeader) != l.head {
		t.Fatalf("the base's side: %d %q", status, body)
	}
	l.expect(t, "GET", diffPath("synth-0000abcd", "build", 2), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", diffPath("synth-0000abcd", "build", 3), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", diffPath("synth-0000abcd", "docs", 1), nil, http.StatusNotFound, "not_found")
	l.expect(t, "GET", diffPath("nope-0000abcd", "build", 1), nil, http.StatusNotFound, "not_found")
	// A repository outside the allowed roots, and a folder that is gone.
	put("synth-0000abce", t.TempDir(), l.head)
	l.expect(t, "GET", diffPath("synth-0000abce", "build", 1), nil, http.StatusConflict, "repo_unavailable")
	put("synth-0000abcf", filepath.Join(l.root, "gone"), l.head)
	l.expect(t, "GET", diffPath("synth-0000abcf", "build", 1), nil, http.StatusConflict, "repo_unavailable")
	for _, p := range []string{"/api/runs/x/tasks/build/attempts/1/diff", diffPath("synth-0000abcd", "-x", 1), "/api/runs/synth-0000abcd/tasks/build/attempts/0/diff", "/api/runs/synth-0000abcd/tasks/build/attempts/01/diff"} {
		l.expect(t, "GET", p, nil, http.StatusBadRequest, "invalid_request")
	}
}

// A run the engine let go (stopped, the server restarted) answers the same
// diff and the same file, from the ledger and the repository.
func TestDiffOfARunTheEngineLetGo(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run, c1 := succeededCandidate(t, l, func(dir string) string { return commitFile(t, dir, "a.txt", "one\n") })
	held := l.expect(t, "GET", diffPath(run, "build", 1), nil, http.StatusOK, "")
	again := l.restartAfterStop(t, run)
	if _, ok := again.srv.runs.Get(run); ok {
		t.Fatal("the engine holds the run after the restart")
	}
	if got := again.expect(t, "GET", diffPath(run, "build", 1), nil, http.StatusOK, ""); !reflect.DeepEqual(got, held) {
		t.Fatalf("let go:\n%v\nheld:\n%v", got, held)
	}
	if status, body, h := diffFileOf(t, again, run, "build", 1, "path=a.txt&side=candidate"); status != http.StatusOK || string(body) != "one\n" || h.Get(commitHeader) != c1 {
		t.Fatalf("the file: %d %q", status, body)
	}
}

// The diff routes, the prepared request and Check again are the admin's:
// an attempt's session token and a run's view link are refused, and a
// switchyard answers 403 on each.
func TestReviewDiffRoutesAreTheAdmins(t *testing.T) {
	l := newLaunchTest(t, nil, nil)
	run := entityOf(t, l.launch(t, l.testEnv, nil, http.StatusCreated, ""), "launch")["runId"].(string)
	a := runningAttempt(t, l.testEnv, run, "build", 1)
	link := l.expect(t, "POST", "/api/runs/"+run+"/links", map[string]any{"role": "view"}, http.StatusCreated, "")["token"].(string)
	routes := []struct{ method, path string }{
		{"GET", diffPath(run, "build", 1)},
		{"GET", diffPath(run, "build", 1) + "/file?path=README&side=base"},
		{"GET", "/api/runs/" + run + "/tasks/build/attempts/1/steps/test/1/repair"},
		{"POST", "/api/runs/" + run + "/tasks/build/recheck"},
	}
	for _, tok := range []string{l.agentToken(a["sessionId"].(string)), link, "", "wrong"} {
		for _, rt := range routes {
			var body any
			if rt.method == "POST" {
				body = map[string]any{"commandId": "c1", "attempt": 1, "commit": l.head}
			}
			if status, data, _ := l.callAs(t, tok, rt.method, rt.path, body); status != http.StatusUnauthorized {
				t.Fatalf("%s %s with a token not the admin's: %d %.200s", rt.method, rt.path, status, data)
			}
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, rt := range routes {
		var body any
		if rt.method == "POST" {
			body = map[string]any{"commandId": "c1", "attempt": 1, "commit": l.head}
		}
		if status, data, out := yard.callAs(t, adminToken, rt.method, rt.path, body); status != http.StatusForbidden || errorCode(out) != "switchyard" {
			t.Fatalf("%s %s on a switchyard: %d %.200s", rt.method, rt.path, status, data)
		}
	}
}

// The diff's reply is bounded by its encoded size, not only by
// gitrepo.MaxFiles: renames whose paths and sources JSON escapes to
// several times their length (a quote, a backslash, '<' as \u003c, a
// control byte as \u0001) are kept in order until the next would take the
// reply as writeJSON writes it past MaxDiffReply, truncated then; a reply
// of exactly MaxDiffReply keeps every file, a byte more drops the last.
func TestFitDiffBoundsTheEncodedReply(t *testing.T) {
	candidate, base := strings.Repeat("c", 40), strings.Repeat("b", 40)
	escaped := strings.Repeat("\"\\<\x01", 60) // 240 bytes, 960 encoded
	change := func(i int) gitrepo.Change {
		return gitrepo.Change{
			Path: fmt.Sprintf("new/%03d/%s", i, escaped), From: fmt.Sprintf("old/%03d/%s", i, escaped),
			Status: "R", Added: i, Removed: 1,
		}
	}
	asFiles := func(cs []gitrepo.Change) []diffFile {
		files := make([]diffFile, 0, len(cs))
		for _, c := range cs {
			files = append(files, diffFile{Path: c.Path, From: c.From, Status: c.Status, Added: c.Added, Removed: c.Removed, Binary: c.Binary})
		}
		return files
	}
	encoded := func(files []diffFile, truncated bool) []byte {
		rec := httptest.NewRecorder()
		writeJSON(rec, http.StatusOK, diffReply(candidate, base, files, truncated))
		return rec.Body.Bytes()
	}
	var d gitrepo.Detail
	for i := range 300 {
		d.Changes = append(d.Changes, change(i))
	}
	if len(d.Changes) >= gitrepo.MaxFiles {
		t.Fatal("the test must stay under gitrepo.MaxFiles, so that only the bytes cut it")
	}

	files, truncated := fitDiff(candidate, base, d)
	body := encoded(files, truncated)
	kept := len(files)
	if !truncated || kept == 0 || kept >= len(d.Changes) {
		t.Fatalf("%d of %d files kept, truncated %v", kept, len(d.Changes), truncated)
	}
	if !reflect.DeepEqual(files, asFiles(d.Changes[:kept])) {
		t.Fatal("the files kept are not the diff's first ones, in order")
	}
	if len(body) > MaxDiffReply {
		t.Fatalf("the reply is %d bytes, past %d", len(body), MaxDiffReply)
	}
	if n := len(encoded(asFiles(d.Changes[:kept+1]), false)); n <= MaxDiffReply {
		t.Fatalf("with the next file the reply would be %d bytes, within %d: it was left out for nothing", n, MaxDiffReply)
	}
	var reply struct {
		Files     []diffFile `json:"files"`
		Truncated bool       `json:"truncated"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || !reply.Truncated || !reflect.DeepEqual(reply.Files, files) {
		t.Fatalf("the reply does not decode to what was kept: %v %v", reply.Truncated, err)
	}

	// The kept files, the last one's path padded so that the reply not cut
	// is exactly MaxDiffReply: every file kept, not truncated; one byte
	// more and the last is left out, truncated, the reply within the bound.
	edge := gitrepo.Detail{Changes: slices.Clone(d.Changes[:kept])}
	pad := MaxDiffReply - len(encoded(asFiles(edge.Changes), false))
	edge.Changes[kept-1].Path += strings.Repeat("a", pad)
	files, truncated = fitDiff(candidate, base, edge)
	if body := encoded(files, truncated); truncated || len(files) != kept || len(body) != MaxDiffReply {
		t.Fatalf("at the bound: %d of %d files, truncated %v, %d bytes", len(files), kept, truncated, len(body))
	}
	edge.Changes[kept-1].Path += "a"
	files, truncated = fitDiff(candidate, base, edge)
	if body := encoded(files, truncated); !truncated || len(files) != kept-1 || len(body) > MaxDiffReply ||
		!reflect.DeepEqual(files, asFiles(edge.Changes[:kept-1])) {
		t.Fatalf("a byte past the bound: %d of %d files, truncated %v, %d bytes", len(files), kept, truncated, len(body))
	}

	// Every file within the bound, the diff itself cut at
	// gitrepo.MaxFiles: truncated still.
	small := gitrepo.Detail{Changes: d.Changes[:3], Truncated: true}
	if files, truncated := fitDiff(candidate, base, small); !truncated || len(files) != 3 {
		t.Fatalf("a diff cut by gitrepo: %d files, truncated %v", len(files), truncated)
	}
	small.Truncated = false
	if files, truncated := fitDiff(candidate, base, small); truncated || len(files) != 3 {
		t.Fatalf("a small diff: %d files, truncated %v", len(files), truncated)
	}
}
