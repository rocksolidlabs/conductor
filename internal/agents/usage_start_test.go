//go:build unix

package agents

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The starter's tests (startUsage, usageProc): nothing is started once
// the read's context has ended, every child started is reaped once, a read
// reports once (an admission granted is told Done once), and a child has
// finished when it has exited, never when its output closed.

// starterHooks sets the starter's seams for one test, put back after it.
func starterHooks(t *testing.T, starting, started, waiting, reaped func([]string)) {
	t.Helper()
	a, b, c, d := usageStarting, usageStarted, usageWaiting, usageReaped
	t.Cleanup(func() { usageStarting, usageStarted, usageWaiting, usageReaped = a, b, c, d })
	usageStarting, usageStarted, usageWaiting, usageReaped = starting, started, waiting, reaped
}

// childKind names the child an argv runs, as the stub sees it.
func childKind(argv []string) string {
	switch {
	case slices.Contains(argv, "--version"):
		return "version"
	case slices.Contains(argv, "auth"):
		return "auth"
	case slices.Contains(argv, "-p"):
		return "usage"
	case slices.Contains(argv, "app-server"):
		return "app-server"
	}
	return "other"
}

// readDeadline replaces the read's deadline with one the test ends, with
// the timeout's own cause; expire ends it.
func readDeadline(t *testing.T) (expire func()) {
	t.Helper()
	was := withUsageTimeout
	t.Cleanup(func() { withUsageTimeout = was })
	var mu sync.Mutex
	var cancel context.CancelCauseFunc
	withUsageTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancelCause(ctx)
		mu.Lock()
		cancel = c
		mu.Unlock()
		return ctx, func() { c(context.Canceled) }
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		cancel(errUsageTimeout)
	}
}

// TestUsageStartSeams drives a cancellation and a deadline at each seam of
// a read, for each kind of child: before the admission, inside it, after
// it and before the start, after the start, during the read (the child
// holding at its start), and at the end (its answer read, before its
// exit is waited for). For each it holds whether the child was started,
// that every child started was reaped once, that an admission granted was
// told Done once, and the read's outcome.
func TestUsageStartSeams(t *testing.T) {
	kinds := []string{"version", "auth", "usage", "app-server", "direct"}
	seams := []string{"before the admission", "inside the admission", "before the start", "after the start", "during the read", "at the end"}
	for _, kind := range kinds {
		for _, seam := range seams {
			if strings.Contains(seam, "admission") && kind != "usage" {
				continue
			}
			for _, cause := range []string{"cancel", "deadline"} {
				t.Run(kind+"/"+seam+"/"+cause, func(t *testing.T) { startSeam(t, kind, seam, cause) })
			}
		}
	}
}

func startSeam(t *testing.T, kind, seam, cause string) {
	stubKind := "claude"
	if kind == "app-server" {
		stubKind = "codex"
	}
	holdKind := kind
	if kind == "direct" {
		holdKind = "version"
	}
	holdfile := filepath.Join(t.TempDir(), "hold")
	var env []string
	if seam == "during the read" {
		env = append(env, "USAGE_HOLD="+holdKind, "USAGE_HOLDFILE="+holdfile)
	}
	s := newStub(t, stubKind, env...)

	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	expire := readDeadline(t)
	// The direct child's context, as the recipes tier makes one.
	directCtx, directCancel := context.WithCancelCause(parent)
	defer directCancel(context.Canceled)
	var fired sync.Once
	fire := func() {
		fired.Do(func() {
			switch {
			case cause == "cancel":
				cancelParent()
			case kind == "direct":
				directCancel(errUsageTimeout)
			default:
				expire()
			}
		})
	}
	mine := func(argv []string) bool { return kind == "direct" || childKind(argv) == kind }

	var mu sync.Mutex
	var started, reaped, startedAll, reapedAll int
	starterHooks(t,
		func(argv []string) {
			if seam == "before the start" && mine(argv) {
				fire()
			}
		},
		func(argv []string) {
			mu.Lock()
			startedAll++
			if mine(argv) {
				started++
			}
			mu.Unlock()
			if seam == "after the start" && mine(argv) {
				fire()
			}
		},
		func(argv []string) {
			if seam == "at the end" && mine(argv) {
				fire()
			}
		},
		func(argv []string) {
			mu.Lock()
			reapedAll++
			if mine(argv) {
				reaped++
			}
			mu.Unlock()
			if seam == "before the admission" && childKind(argv) == "auth" {
				fire()
			}
		})
	if seam == "during the read" {
		// The child holds at its start, having written its pid: the read
		// is then waiting on it.
		go func() {
			for parent.Err() == nil {
				if _, err := os.Stat(holdfile); err == nil {
					fire()
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	adm := &fakeAdmit{}
	admit := adm.admit
	if seam == "inside the admission" {
		admit = func(v string) (Admission, error) {
			fire()
			return adm.admit(v)
		}
	}

	var rep UsageReport
	var directOK, directStarted bool
	switch kind {
	case "app-server":
		rep = readCodexUsage(parent, s.input(self(t), nil))
	case "direct":
		// As the recipes tier runs a command (usageRecipeRun).
		ctx := directCtx
		proc, err := startUsage(ctx, append(self(t), "--version"), s.input(nil, nil), false, true)
		if err == nil {
			directStarted = true
			_, rerr := io.ReadAll(io.LimitReader(proc.lines.r, usageRecipeMaxTest+1))
			proc.end()
			directOK = rerr == nil && ctx.Err() == nil && proc.cmd.ProcessState.Success()
		} else if !errors.Is(err, errNotStarted) {
			t.Fatalf("not started for another reason: %v", err)
		}
	default:
		rep = readClaudeUsage(parent, s.input(self(t), admit))
	}

	wantStarted := 1
	if strings.HasPrefix(seam, "before") || strings.HasPrefix(seam, "inside") {
		wantStarted = 0
	}
	mu.Lock()
	defer mu.Unlock()
	if started != wantStarted {
		t.Errorf("%d %s children started, want %d", started, kind, wantStarted)
	}
	if reaped != started || reapedAll != startedAll {
		t.Errorf("reaped %d of %d %s children, %d of %d in all: each child started is reaped once", reaped, started, kind, reapedAll, startedAll)
	}
	if seam == "during the read" {
		waitGone(t, readPid(t, holdfile))
	}
	if kind == "direct" {
		if directStarted != (wantStarted == 1) || directOK {
			t.Errorf("direct: started %v, ok %v", directStarted, directOK)
		}
		return
	}
	// The read's outcome: at the end the answer is in hand and stands,
	// for the children whose answer is the read's (the /usage run, the app
	// server); everywhere else the read ends in the context's words.
	answered := seam == "at the end" && (kind == "usage" || kind == "app-server")
	words := "the read was stopped"
	if cause == "deadline" {
		words = "did not answer within"
	}
	switch {
	case answered && (rep.State != UsageOK || rep.Words != ""):
		t.Errorf("the answer read before the end: %+v", rep)
	case !answered && (rep.State != UsageError || !strings.Contains(rep.Words, words)):
		t.Errorf("want error with %q: %+v", words, rep)
	}
	wantOutcome := Inconclusive
	if answered && kind == "usage" {
		wantOutcome = Local
	}
	if rep.Outcome != wantOutcome {
		t.Errorf("outcome %v, want %v", rep.Outcome, wantOutcome)
	}
	// An admission is asked only by a read that reached it alive, and told
	// Done exactly once.
	var wantDone []Outcome
	if kind == "usage" && seam != "before the admission" {
		wantDone = []Outcome{wantOutcome}
	}
	if got := adm.outcomes(); !slices.Equal(got, wantDone) || len(adm.asked) != len(wantDone) {
		t.Errorf("admissions asked %q, told %v; want told %v", adm.asked, got, wantDone)
	}
}

// usageRecipeMaxTest is the recipes tier's bound (usageRecipeMax), which
// this file cannot see under its build tags.
const usageRecipeMaxTest = 1 << 20

// versionEnd reads Claude Code's version from a stub run with kv, the
// read's end seams handed to the test: atWait is called once the version's
// end begins (its output read), with the version's deadline's expiry.
func versionEnd(t *testing.T, atWait func(expire func()), kv ...string) UsageReport {
	t.Helper()
	was := withVersionTimeout
	t.Cleanup(func() { withVersionTimeout = was })
	var mu sync.Mutex
	var cancel context.CancelCauseFunc
	withVersionTimeout = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancelCause(ctx)
		mu.Lock()
		cancel = c
		mu.Unlock()
		return ctx, func() { c(context.Canceled) }
	}
	expire := func() {
		mu.Lock()
		defer mu.Unlock()
		cancel(errVersionTimeout)
	}
	none := func([]string) {}
	starterHooks(t, none, none, func(argv []string) {
		if childKind(argv) == "version" {
			atWait(expire)
		}
	}, none)
	s := newStub(t, "claude", kv...)
	return claudeRead(t, s, &fakeAdmit{})
}

// TestUsageEndWaitsForExit: a child has finished when it has exited. Its
// output closed first, it is waited for up to the deadline, and a clean
// exit then qualifies; one that never exits is ended at the deadline, a
// timeout; a leader that exits leaving a child, or a grandchild, holding
// the output has its group ended at its exit, and its own status judged.
func TestUsageEndWaitsForExit(t *testing.T) {
	t.Run("output closed, then a clean exit", func(t *testing.T) {
		gate := filepath.Join(t.TempDir(), "gate")
		open := func(func()) {
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Error(err)
			}
		}
		rep := versionEnd(t, open, "USAGE_VERSION_THEN=close-gate", "USAGE_GATE="+gate)
		if rep.State != UsageOK || rep.Version != claudeUsageQualified {
			t.Fatalf("a program that closed its output and then exited 0: %+v", rep)
		}
	})
	t.Run("output closed, never exits", func(t *testing.T) {
		rep := versionEnd(t, func(expire func()) { expire() }, "USAGE_VERSION_THEN=close-hang")
		if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "did not print its version within") {
			t.Fatalf("a program that closed its output and never exited: %+v", rep)
		}
	})
	for _, hold := range []string{"child", "grandchild"} {
		for _, exit := range []string{"0", "3"} {
			t.Run(hold+" holds the output, exit "+exit, func(t *testing.T) {
				pidfile := filepath.Join(t.TempDir(), "pid")
				// The deadline is never reached: the exit ends the wait.
				rep := versionEnd(t, func(func()) {}, "USAGE_VERSION_PIDFILE="+pidfile, "USAGE_VERSION_HOLD="+hold, "USAGE_VERSION_EXIT="+exit)
				waitGone(t, readPid(t, pidfile))
				switch exit {
				case "0":
					if rep.State != UsageOK || rep.Version != claudeUsageQualified {
						t.Fatalf("the leader exited 0: %+v", rep)
					}
				default:
					if rep.State != UsageUnqualified || !strings.Contains(rep.Words, "failed (exit status 3)") {
						t.Fatalf("the leader exited 3: %+v", rep)
					}
				}
			})
		}
	}
}
