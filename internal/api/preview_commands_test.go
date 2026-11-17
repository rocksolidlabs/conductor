package api

import (
	"context"
	"fmt"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
