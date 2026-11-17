package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A candidate's preview over HTTP (PV1 PR-8): the two command routes and
// their receipts, the label's ?seq=, a capture of the checkout and the
// candidate it names. Each test's candidate is a run of its own over a
// repository of its own (candidateEnv), ended before its folders go.

var previewCommands atomic.Int64

// checkoutFixture is a finished run's attempt build.1, closed by its result,
// its own worktree preview gone with its close: the candidate a preview
// checkout is opened of.
type checkoutFixture struct {
	e      *testEnv
	run    string
	commit string
	dir    string // the checkout's: <worktree>.preview
}

// candidateEnv launches previewedCrew over a new repository, has its
// attempt submit its result and waits for the run to finish with its
// trees ended.
func candidateEnv(t *testing.T) *checkoutFixture {
	t.Helper()
	e := readyEnv(t, nil)
	e.gitRepo(t)
	return candidateIn(t, e, previewedCrew(e.root))
}

// candidateIn launches c in e and makes its attempt build.1 a candidate.
func candidateIn(t *testing.T, e *testEnv, c crew.Crew) *checkoutFixture {
	t.Helper()
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = e.srv.runs.Stop(ctx, run.ID)
		e.srv.runs.Drain(ctx)
		e.srv.trees.close(e.srv.log)
	})
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("result: %d %v", resp.StatusCode, out)
	}
	if !waitFor(30*time.Second, func() bool {
		r, _ := e.srv.runs.Get(run.ID)
		return r.Outcome == "succeeded" && r.Cleaning == 0
	}) {
		t.Fatal("the run did not finish with its trees ended")
	}
	st, _, err := e.srv.runs.TaskOf(run.ID, "build")
	if err != nil || st.Attempt == nil || st.Attempt.Result == "" {
		t.Fatalf("the candidate: %+v %v", st, err)
	}
	return &checkoutFixture{e: e, run: run.ID, commit: st.Attempt.Result, dir: st.Attempt.Worktree + ".preview"}
}

// path is the preview route of attempt n of build, and of its preview seq
// with the words after it.
func (c *checkoutFixture) path(n int, more string) string {
	return fmt.Sprintf("/api/runs/%s/tasks/build/attempts/%d/preview%s", c.run, n, more)
}

// command sends a preview command as the admin.
func (c *checkoutFixture) command(t *testing.T, path, id string) (int, []byte, map[string]any) {
	t.Helper()
	return c.e.callAs(t, adminToken, "POST", path, map[string]any{"commandId": id})
}

// open opens build.1 in a preview with a command of its own and waits for
// its label: the preview ready, and its label read.
func (c *checkoutFixture) open(t *testing.T) (seq int, label map[string]any) {
	t.Helper()
	status, data, out := c.command(t, c.path(1, ""), fmt.Sprintf("open-%d", previewCommands.Add(1)))
	if status != http.StatusAccepted {
		t.Fatalf("the open: %d %s", status, data)
	}
	seq = int(out["seq"].(float64))
	return seq, c.ready(t, seq)
}

// ready waits for the label of the preview seq of build.1 and returns it.
func (c *checkoutFixture) ready(t *testing.T, seq int) map[string]any {
	t.Helper()
	var label map[string]any
	if !waitFor(30*time.Second, func() bool {
		status, _, out := c.e.callAs(t, adminToken, "GET", c.path(1, fmt.Sprintf("?seq=%d", seq)), nil)
		label = out
		return status == http.StatusOK
	}) {
		t.Fatalf("the preview %d is not ready: %v", seq, label)
	}
	return label
}

// previewed is the checkout's preview as the capture helpers take one: its
// page (the label's URL without the fragment) and its tree's secret.
func (c *checkoutFixture) previewed(t *testing.T, label map[string]any) *previewed {
	t.Helper()
	u, secret, _ := strings.Cut(label["url"].(string), "#token=")
	if len(secret) != 64 {
		t.Fatalf("the admin's label URL: %q", label["url"])
	}
	return &previewed{e: c.e, run: c.run, dir: c.dir, page: u, secret: secret}
}

// answerReceipt is the receipt of an answer.
func answerReceipt(out map[string]any) map[string]any {
	rc, _ := out["receipt"].(map[string]any)
	return rc
}

// Each command is taken with its receipt in the run's ledger, before the
// preview is begun, and settled with what came of it: the open 202 with
// its seq and state, its repeat 200 by the same receipt (no second open),
// the same id with other content a conflict, a second open of the
// candidate failed with its receipt (preview_open), the close 200 and
// its repeat by its receipt; a close of a preview no longer open failed
// with its receipt (no_preview). The ledger keeps each, read back by the
// command route. An open of a run neither the server nor the ledger has
// leaves no receipt.
func TestPreviewCommandsHaveReceipts(t *testing.T) {
	c := candidateEnv(t)
	status, data, out := c.command(t, c.path(1, ""), "open-1")
	if status != http.StatusAccepted || out["seq"] != float64(1) || out["state"] != ledger.PreviewResolved {
		t.Fatalf("the open: %d %s", status, data)
	}
	rc := answerReceipt(out)
	if rc["intent"] != crew.IntentPreviewOpen || rc["status"] != ledger.CommandDone || rc["repeat"] != nil {
		t.Fatalf("the open's receipt: %v", rc)
	}
	if p, _ := rc["preview"].(map[string]any); p["seq"] != float64(1) || p["task"] != "build" || p["attempt"] != float64(1) || p["run"] != c.run {
		t.Fatalf("the receipt's preview: %v", rc)
	}
	label := c.ready(t, 1)
	if label["tree"] != "preview" || label["seq"] != float64(1) || label["candidate"] != fmt.Sprintf("%s/build/1@%s", c.run, c.commit) || label["mutable"] != false {
		t.Fatalf("the checkout's label: %v", label)
	}
	status, data, out = c.command(t, c.path(1, ""), "open-1")
	if status != http.StatusOK || out["seq"] != float64(1) || out["state"] != ledger.PreviewReady || answerReceipt(out)["repeat"] != true {
		t.Fatalf("the open again: %d %s", status, data)
	}
	if status, data, out := c.command(t, c.path(2, ""), "open-1"); status != http.StatusConflict || codeOf(out) != "command_conflict" {
		t.Fatalf("the id again for another attempt: %d %s", status, data)
	}
	status, data, out = c.command(t, c.path(1, ""), "open-2")
	if status != http.StatusConflict || codeOf(out) != crew.RefusalPreviewOpen || answerReceipt(out)["status"] != ledger.CommandFailed || answerReceipt(out)["refusal"] != crew.RefusalPreviewOpen {
		t.Fatalf("a second open of the candidate: %d %s", status, data)
	}
	status, data, out = c.command(t, c.path(1, "/1/close"), "close-1")
	if status != http.StatusOK || (out["state"] != "ending" && out["state"] != ledger.PreviewEnded) || answerReceipt(out)["intent"] != crew.IntentPreviewClose {
		t.Fatalf("the close: %d %s", status, data)
	}
	if status, data, out := c.command(t, c.path(1, "/1/close"), "close-1"); status != http.StatusOK || answerReceipt(out)["repeat"] != true {
		t.Fatalf("the close again: %d %s", status, data)
	}
	if !waitFor(30*time.Second, func() bool {
		return c.e.srv.runs.PreviewNow(crew.PreviewRef{Run: c.run, Task: "build", N: 1, Seq: 1}).State == ledger.PreviewEnded
	}) {
		t.Fatal("the preview did not end")
	}
	status, data, out = c.command(t, c.path(1, "/1/close"), "close-2")
	if status != http.StatusNotFound || codeOf(out) != crew.RefusalNoPreview || answerReceipt(out)["status"] != ledger.CommandFailed {
		t.Fatalf("a close of an ended preview: %d %s", status, data)
	}
	for id, want := range map[string]string{"open-1": ledger.CommandDone, "open-2": ledger.CommandFailed, "close-1": ledger.CommandDone, "close-2": ledger.CommandFailed} {
		status, data, out := c.e.callAs(t, adminToken, "GET", "/api/runs/"+c.run+"/commands/"+id, nil)
		cmd, _ := out["command"].(map[string]any)
		if status != http.StatusOK || cmd["status"] != want {
			t.Fatalf("the command %s: %d %s", id, status, data)
		}
	}
	if status, data, out := c.e.callAs(t, adminToken, "POST", "/api/runs/previewed-ffffffff/tasks/build/attempts/1/preview", map[string]any{"commandId": "open-3"}); status != http.StatusNotFound || answerReceipt(out) != nil {
		t.Fatalf("an open of no run: %d %s", status, data)
	}
}

// A preview command's body is {commandId} and nothing else, and it takes
// no parameter; its path a run id, a task id and numbers from 1.
func TestAPreviewCommandsBodyIsItsIDAlone(t *testing.T) {
	c := candidateEnv(t)
	for name, tc := range map[string]struct {
		path string
		body any
	}{
		"no body":            {c.path(1, ""), nil},
		"another member":     {c.path(1, ""), map[string]any{"commandId": "x", "expectedRevision": 1}},
		"an id not of form":  {c.path(1, ""), map[string]any{"commandId": "a b"}},
		"an id too long":     {c.path(1, ""), map[string]any{"commandId": strings.Repeat("a", 33)}},
		"a parameter":        {c.path(1, "?seq=1"), map[string]any{"commandId": "x"}},
		"attempt 0":          {c.path(0, ""), map[string]any{"commandId": "x"}},
		"seq 0":              {c.path(1, "/0/close"), map[string]any{"commandId": "x"}},
		"a seq not a number": {c.path(1, "/one/close"), map[string]any{"commandId": "x"}},
		// Valid but for its size: refused by the bound alone.
		"a body past 1 KiB": {c.path(1, ""), []byte(`{"commandId": "x"` + strings.Repeat(" ", 2<<10) + `}`)},
	} {
		if status, data, out := c.e.callAs(t, adminToken, "POST", tc.path, tc.body); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Errorf("%s: %d %s", name, status, data)
		}
	}
}

// The candidate is the checkout's at the save, by the engine's own
// comparison: an intact checkout's capture names it; a tracked file
// changed under skip-worktree, which git status does not show, is a
// change all the same, and the capture names no candidate. Both are of
// origin candidate, tree preview; the second is mutable.
func TestAHiddenTrackedChangeIsNoCandidate(t *testing.T) {
	c := candidateEnv(t)
	seq, label := c.open(t)
	p := c.previewed(t, label)
	b := p.body(t, label, button())
	b.From.Seq = seq
	first := originOf(created(t, c.e, b, nil))
	if first["kind"] != "candidate" || first["tree"] != "preview" || first["candidate"] != fmt.Sprintf("%s/build/1@%s", c.run, c.commit) || first["mutable"] != false {
		t.Fatalf("the intact checkout's capture: %v", first)
	}
	gitIn(t, c.dir, "update-index", "--skip-worktree", "README")
	writeIn(t, c.dir, "README", "changed where git status does not look\n")
	if st := gitIn(t, c.dir, "status", "--porcelain", "--untracked-files=no"); strings.TrimSpace(st) != "" {
		t.Fatalf("git status shows the hidden change: %q", st)
	}
	label = c.ready(t, seq)
	b = p.body(t, label, button())
	b.From.Seq = seq
	second := originOf(created(t, c.e, b, nil))
	if second["kind"] != "candidate" || second["tree"] != "preview" || second["candidate"] != nil || second["mutable"] != true {
		t.Fatalf("the capture of a hidden change: %v", second)
	}
}

// A capture names a checkout by its run, task, attempt and seq together:
// another attempt's, or a seq not open, is no preview; the checkout's
// secret, held while it is open, is kept nowhere in the store.
func TestACaptureReachesItsOwnCheckoutAlone(t *testing.T) {
	c := candidateEnv(t)
	seq, label := c.open(t)
	p := c.previewed(t, label)
	for name, from := range map[string]captureFrom{
		"another attempt": {Run: c.run, Task: "build", Attempt: 2, Seq: seq},
		"another task":    {Run: c.run, Task: "docs", Attempt: 1, Seq: seq},
		"another seq":     {Run: c.run, Task: "build", Attempt: 1, Seq: seq + 1},
		"the worktree":    {Run: c.run, Task: "build", Attempt: 1},
	} {
		b := p.body(t, label, button())
		b.From = from
		if got, out := postCapture(t, c.e, b, nil); got != http.StatusNotFound || codeOf(out) != "no_preview" {
			t.Errorf("%s: %d %v", name, got, out)
		}
	}
	if !slices.Contains(c.e.srv.runs.TreeSecrets(), p.secret) {
		t.Fatal("the checkout's secret is not among the secrets held")
	}
	b := p.body(t, label, button())
	b.From.Seq = seq
	b.Annotations[0].Note = "see " + p.page + "#token=" + p.secret
	created(t, c.e, b, nil)
	p.keptNowhere(t, p.secret)
}

// The label takes seq alone: any other parameter, seq twice or empty, or
// a seq that is not a number from 1 in at most four digits is refused; a
// seq not open is no preview.
func TestTheLabelTakesSeqAlone(t *testing.T) {
	c := candidateEnv(t)
	for _, q := range []string{"?other=1", "?seq=1&seq=1", "?seq=", "?seq=0", "?seq=01", "?seq=-1", "?seq=%2B1", "?seq=12345", "?seq=one", "?seq=1&x=", "?seq=1%00"} {
		if status, data, out := c.e.callAs(t, adminToken, "GET", c.path(1, q), nil); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Errorf("%s: %d %s", q, status, data)
		}
	}
	for _, q := range []string{"?seq=1", "?seq=9999", ""} {
		if status, data, out := c.e.callAs(t, adminToken, "GET", c.path(1, q), nil); status != http.StatusNotFound || codeOf(out) != "no_preview" {
			t.Errorf("%s with no preview open: %d %s", q, status, data)
		}
	}
}

// The two command routes and the checkout's label are the admin's: a share
// link, a session's token, a host token, no token, a Brakeman turn and a
// provider's principal with a grant are 401; a switchyard 403.
func TestPreviewCommandRoutesAreAdminOnly(t *testing.T) {
	fake := exttest.NewFakeIdentity("cloud")
	fake.Add(passAna, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}})
	fake.Add(passHostOnly, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantHost}})
	provided := newTestEnvAgents(t, nil, slog.New(slog.DiscardHandler), nil, withIdentity(fake))
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
	base := "/api/runs/previewed-0123abcd/tasks/build/attempts/1/preview"
	routes := [][2]string{{"POST", base}, {"POST", base + "/1/close"}, {"GET", base + "?seq=1"}}
	for _, r := range routes {
		var body any
		if r[0] == "POST" {
			body = map[string]any{"commandId": "x"}
		}
		for who, tok := range tokens {
			if status, data, _ := b.callAs(t, tok, r[0], r[1], body); status != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: %d %s", r[0], r[1], who, status, data)
			}
		}
		raw, _ := json.Marshal(body)
		if status, data, _ := b.callAsTurn(t, btok, bsid, r[0], r[1], raw); status != http.StatusUnauthorized {
			t.Errorf("%s %s as a Brakeman turn: %d %s", r[0], r[1], status, data)
		}
		for who, pass := range map[string]string{"the decide grant alone": passAna, "the host grant alone": passHostOnly} {
			if status, data, _ := provided.callAs(t, pass, r[0], r[1], body); status != http.StatusUnauthorized {
				t.Errorf("%s %s as a provider's principal with %s: %d %s", r[0], r[1], who, status, data)
			}
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, r := range routes {
		if status, data, out := yard.callAs(t, adminToken, r[0], r[1], map[string]any{"commandId": "x"}); status != http.StatusForbidden || codeOf(out) != "switchyard" {
			t.Errorf("%s %s on a switchyard: %d %s", r[0], r[1], status, data)
		}
	}
}

// A run finished under a first server is previewed by a second over the
// same data directory, which does not hold it: the open's receipt in the
// ledger, the attempt's previewing in the task's read and in the run's
// record, the label and a capture of the checkout (its scope the run's
// crew and repository), and the close.
func TestAPreviewOfARunOfAnEarlierServerOverHTTP(t *testing.T) {
	c := candidateEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.e.srv.Shutdown(ctx)
	again := c.e.restart()
	if _, ok := again.srv.runs.Get(c.run); ok {
		t.Fatal("the second server holds the run")
	}
	next := &checkoutFixture{e: again, run: c.run, commit: c.commit, dir: c.dir}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		again.srv.runs.Drain(ctx)
		again.srv.trees.close(again.srv.log)
	})
	var status int
	var data []byte
	var out map[string]any
	// Its folder no longer a work tree: refused in words, no receipt, and
	// opened once it is one again.
	gitDir := filepath.Join(c.e.root, ".git")
	if err := os.Rename(gitDir, gitDir+".away"); err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		status, data, out = next.command(t, next.path(1, ""), "open-unreadable")
		return codeOf(out) != "ledger_unavailable"
	}) || status != http.StatusConflict || codeOf(out) != crew.RefusalRunUnreadable || answerReceipt(out) != nil || strings.Contains(string(data), "fatal") {
		t.Fatalf("an open of a run whose folder moved: %d %s", status, data)
	}
	if err := os.Rename(gitDir+".away", gitDir); err != nil {
		t.Fatal(err)
	}
	if !waitFor(30*time.Second, func() bool {
		status, data, out = next.command(t, next.path(1, ""), "open-earlier")
		return codeOf(out) != "ledger_unavailable"
	}) || status != http.StatusAccepted || out["seq"] != float64(1) {
		t.Fatalf("the open of the earlier server's run: %d %s", status, data)
	}
	label := next.ready(t, 1)
	if label["candidate"] != fmt.Sprintf("%s/build/1@%s", c.run, c.commit) {
		t.Fatalf("the label: %v", label)
	}
	status, data, out = again.callAs(t, adminToken, "GET", "/api/runs/"+c.run+"/tasks/build", nil)
	attempts, _ := out["attempts"].([]any)
	if status != http.StatusOK || len(attempts) != 1 {
		t.Fatalf("the task's read: %d %s", status, data)
	}
	if pv, _ := attempts[0].(map[string]any)["previewing"].(map[string]any); pv["seq"] != float64(1) || pv["state"] != ledger.PreviewReady || pv["preview"] == "" {
		t.Fatalf("the attempt's previewing: %s", data)
	}
	status, data, out = again.callAs(t, adminToken, "GET", "/api/runs/"+c.run, nil)
	run, _ := out["run"].(map[string]any)
	tasks, _ := run["tasks"].([]any)
	if status != http.StatusOK || out["record"] != true || len(tasks) != 1 {
		t.Fatalf("the run's read: %d %s", status, data)
	}
	if a, _ := tasks[0].(map[string]any)["attempt"].(map[string]any); a["previewing"] == nil {
		t.Fatalf("the record's attempt has no previewing: %s", data)
	}
	p := next.previewed(t, label)
	b := p.body(t, label, button())
	b.From.Seq = 1
	cp := created(t, again, b, nil)
	scope, _ := cp["scope"].(map[string]any)
	if originOf(cp)["candidate"] == nil || scope["crew"] != "previewed" || scope["repo"] == nil || scope["repo"] == "" {
		t.Fatalf("the capture of the earlier server's run: %v", cp)
	}
	if status, data, _ := next.command(t, next.path(1, "/1/close"), "close-earlier"); status != http.StatusOK {
		t.Fatalf("the close: %d %s", status, data)
	}
	if status, data, out := again.callAs(t, adminToken, "GET", "/api/runs/"+c.run+"/commands/open-earlier", nil); status != http.StatusOK || out["command"].(map[string]any)["status"] != ledger.CommandDone {
		t.Fatalf("the open's receipt: %d %s", status, data)
	}
	if _, err := os.Stat(filepath.Join(c.dir, ".git")); err != nil {
		t.Fatalf("the checkout: %v", err)
	}
}
