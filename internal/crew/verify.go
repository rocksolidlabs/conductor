package crew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proc"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Check capture bounds (proc.Spec).
const (
	checkHead = 64 << 10
	checkTail = 2 << 10
)

// checkSlots is the bound on checks running at once across the server's
// managed runs: one, until SetCheckConcurrency says otherwise.
func (e *Engine) checkSlots() chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.checkSem == nil {
		e.checkSem = make(chan struct{}, 1)
	}
	return e.checkSem
}

// SetCheckConcurrency bounds the checks running at once across the
// server's managed runs (config checks.maxConcurrent; a go test beside
// four agents on a laptop wants one). Set it before the first launch.
func (e *Engine) SetCheckConcurrency(n int) {
	n = max(n, 1)
	e.mu.Lock()
	e.checkSem = make(chan struct{}, n)
	e.mu.Unlock()
}

func checkTimeout(ch Check) time.Duration {
	return stepTimeout(ledger.PhaseCheck, ch)
}

// verify decides a's submitted result. A review succeeds at once. A work
// task's result is checked out detached (<worktree>.check), checks or
// none: a result whose checkout is refused (the crew's directory a link or
// a file, as this machine's filesystem resolves it) or that the project
// manifest refuses would fail every attempt made at it, a dependent's too,
// before its agent could act. With a manifest the checkout is a tree
// (steps.go): its manifest resolved at the result commit (so a result that
// changes the manifest is checked by the one it changed), then its setup
// phase, each step a receipt and the checkout compared with the result
// after each (what the worker had, the checks have; a step that changes
// tracked content fails the attempt at it), its services and its ready
// probes, the checkout compared once after them (a change is the phase's:
// stage "ready"), its ports, its state directory and the manifest's
// approved env names given to its steps and checks as to the worker's
// session; its services run beside the checks, and a change found after a
// check names them as its other possible author. Then the checks, and after them the manifest's verify
// steps (runCheck), one at a time across the server (checkSlots), each recorded before it starts, with its
// process once started, and with its outcome when it ends. After each one
// the checkout is compared with the commit as git would store it
// (checkoutIntact): a check that cannot run, exits non-zero, times out,
// moves the checkout or changes tracked content fails the attempt at that
// check ("check <id>", "verify <id>"), and the rest do not run; untracked outputs are listed with the
// check. A failure before the checks is a refused result, counted, the
// next attempt starting where the refused one did. The phases and the
// checks hold the tree, which is quiet when the decision is taken. It runs
// on a goroutine the run's stop waits for (r.checks) and ends with the
// attempt's context.
func (e *Engine) verify(r *run, a *attempt) {
	defer r.checks.Done()
	t := a.t
	if t.def.Kind == TaskReview {
		e.accept(r, a, true, "", "")
		return
	}
	ctx := a.ctx
	dir := a.worktree + ".check"
	e.gitMu.Lock()
	err := AddDetachedWorktree(ctx, r.cwd, dir, a.result)
	e.gitMu.Unlock()
	if err != nil {
		e.accept(r, a, false, "checkout", session.CleanMessage(err.Error()))
		return
	}
	if head, err := headCommit(ctx, dir); err != nil || head != a.result {
		e.accept(r, a, false, "checkout", fmt.Sprintf("the checkout is at %s, not the result %s", short(head), short(a.result)))
		return
	}
	// The crew's directory in the checkout, as in the worktree: not
	// through a link the result commits, which would send the checks
	// elsewhere than the tree compared, nor a file, which no check and no
	// next attempt can work in. A result with no tracked file left there
	// has none in the checkout: it is made, as an attempt's is.
	if err := checkMemberDir(dir, r.prefix); err != nil {
		e.accept(r, a, false, "checkout", session.CleanMessage(err.Error()))
		return
	}
	cwd := filepath.Join(dir, r.prefix)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		e.accept(r, a, false, "checkout", session.CleanMessage(err.Error()))
		return
	}
	var tr *tree
	if r.hasManifest() {
		// Held from its resolution (resolveTree) through the checks: a
		// close that comes meanwhile ends it once they have returned.
		tr, err = e.resolveTree(ctx, r, a, ledger.TreeCheckout, dir, cwd, a.result)
		if err != nil && tr != nil {
			tr.busy.Done()
		}
		if errors.Is(err, errAttemptClosed) || (err != nil && ctx.Err() != nil) {
			return // closed meanwhile: cancelled or stopped
		}
		if err != nil {
			e.accept(r, a, false, "manifest", session.CleanMessage(err.Error()))
			return
		}
	}
	stage, words, decided := e.checkTree(ctx, r, a, tr, dir, cwd)
	if tr != nil {
		tr.busy.Done()
	}
	if !decided {
		return // closed meanwhile: cancelled or stopped, the check's end recorded
	}
	e.accept(r, a, stage == "", stage, words)
}

// checkTree runs the checkout's phases, when it is a tree, then the task's
// checks in it and, after them, the manifest's verify steps (checks of
// the crew's: rows of phase verify, each under the server's check slot and
// the checkout compared after it, a failure at stage "verify <id>" decided
// as a failing check's is): the stage and words of the first failure, ""
// for none; decided is false when the attempt closed under them (cancelled
// or stopped), which takes no decision. The caller holds the tree.
func (e *Engine) checkTree(ctx context.Context, r *run, a *attempt, tr *tree, dir, cwd string) (stage, words string, decided bool) {
	if tr != nil {
		stage, err := e.prepareTree(ctx, r, a, tr)
		if ctx.Err() != nil {
			return "", "", false
		}
		if err != nil {
			return stage, session.CleanMessage(err.Error()), true
		}
	}
	for _, ch := range a.t.def.Checks {
		if stage, words, decided, ok := e.runCheck(ctx, r, a, tr, dir, cwd, ledger.PhaseCheck, ch); !ok {
			return stage, words, decided
		}
	}
	if tr != nil {
		for _, ch := range tr.man.m.Verify {
			if stage, words, decided, ok := e.runCheck(ctx, r, a, tr, dir, cwd, ledger.PhaseVerify, ch); !ok {
				return stage, words, decided
			}
		}
	}
	return "", "", true
}

// runCheck runs one of the checkout's checks, of phase check (the task's)
// or verify (the manifest's), and keeps its receipt: put started before it
// runs, running with its process once started, and finished with its
// outcome, the checkout compared with the result after it (a change is its
// failure, naming the tree's services as its other possible author). A
// task's check's events are check.started, check.running and
// check.finished, as they always were; a verify step's are the steps'
// (step.started, step.running, step.finished) with its phase and the
// manifest that ran it. A running row the ledger refuses ends its process
// and fails it with the ledger's words, as runStep does; one the attempt's
// close interrupts, under it, while it waits for its slot or while its
// checkout is compared, is concluded
// interrupted with what its process left (its identity, its output). ok is
// true when it passed; else the stage ("check <id>", "verify <id>") and
// words of its failure, decided false when the attempt closed under it.
func (e *Engine) runCheck(ctx context.Context, r *run, a *attempt, tr *tree, dir, cwd, phase string, ch Check) (stage, words string, decided, ok bool) {
	t := a.t
	stage = phase + " " + ch.ID
	if ctx.Err() != nil {
		return "", "", false, false // closed meanwhile: cancelled or stopped
	}
	step := phase == ledger.PhaseVerify
	e.mu.Lock()
	k := a.nextK(ch.ID)
	e.mu.Unlock()
	row := ledger.CheckRow{RunID: r.id, TaskID: t.def.ID, N: a.n, ID: ch.ID, K: k, Phase: phase, Tries: 1, Argv: slices.Clone(ch.Argv), Dir: cwd, Subject: a.result, State: ledger.CheckStarted, Exit: -1, StartedAt: time.Now().UTC()}
	if tr != nil {
		row.Ports = maps.Clone(tr.ports)
		if step {
			row.Manifest = tr.man.identity()
		}
	}
	// event is the receipt's event of the kind (started, running,
	// finished), with more in its payload.
	event := func(kind string, more map[string]any) tx {
		if step {
			return ev("step."+kind, stepPayload(a, tr, phase, ch.ID, k, more))
		}
		p := map[string]any{"task": t.def.ID, "attempt": a.n, "check": ch.ID}
		maps.Copy(p, more)
		return ev("check."+kind, p)
	}
	unrecorded := "the ledger could not record the check"
	if step {
		unrecorded = "the ledger could not record the step " + ch.ID
	}
	if err := e.transact(ctx, r, func() tx {
		more := map[string]any{"k": k, "subject": a.result}
		if step {
			more = map[string]any{"manifest": row.Manifest, "subject": a.result}
		}
		out := event("started", more)
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(row) }
		return out
	}, giveUp); err != nil {
		return stage, unrecorded + ": " + session.CleanMessage(err.Error()), true, false
	}
	// The check's own context: a running row the ledger refuses ends the
	// process under it, as a manifest step's does (runStep), so nothing
	// runs that the ledger does not record (Codex's review of #498).
	runCtx, endRun := context.WithCancel(ctx)
	defer endRun()
	var refused error
	var log bytes.Buffer
	var res proc.Result
	sem := e.checkSlots()
	select {
	case sem <- struct{}{}:
		res = e.runCommand(runCtx, cwd, ch.Argv, checkTimeout(ch), tr.env(r.id), &log, func(p proc.Process) {
			running := row
			running.State, running.PID, running.Group, running.ProcessStartedAt = ledger.CheckRunning, p.PID, p.Group, p.StartedAt
			if err := e.transact(context.Background(), r, func() tx {
				out := event("running", map[string]any{"pid": p.PID, "group": p.Group})
				out.more = func(ltx LedgerTx) error { return ltx.PutCheck(running) }
				return out
			}, nil); err != nil {
				refused = err
				endRun()
			}
		})
		<-sem
	case <-ctx.Done():
		// Closed while it waited for its slot: never started, its
		// receipt concluded below as interrupted all the same.
		res = proc.Result{Exit: -1, Canceled: true}
	}
	// What the process left, whichever way it ended: its identity, its
	// output and its exit, so a receipt interrupted or refused keeps them.
	fin := row
	fin.FinishedAt = time.Now().UTC()
	fin.PID, fin.Group, fin.ProcessStartedAt = res.Process.PID, res.Process.Group, res.Process.StartedAt
	fin.Exit, fin.Signal, fin.TimedOut, fin.Killed = res.Exit, res.Signal, res.TimedOut, res.Killed
	fin.Truncated, fin.Total = res.Truncated, res.Total
	fin.Log = bytes.Clone(log.Bytes())
	sum := sha256.Sum256(fin.Log)
	fin.LogSHA256 = hex.EncodeToString(sum[:])
	fin.Tail = string(lastOutput(res, checkTail))
	// interrupted concludes the receipt as the attempt's close left it:
	// cancelled or stopped under the check, before it had its slot or
	// while its checkout was compared. The attempt is closed by what
	// cancelled it, with the check's end recorded.
	interrupted := func() {
		fin.State = ledger.CheckInterrupted
		fin.Error = "the attempt closed under the check"
		if step {
			fin.Error = e.interruptedWhy(r, a, ch)
		}
		_ = e.transact(context.Background(), r, func() tx {
			more := map[string]any{"interrupted": true, "duration": res.Duration.String()}
			if step {
				more["exit"], more["error"] = res.Exit, fin.Error
			}
			out := event("finished", more)
			out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
			return out
		}, nil)
	}
	if ctx.Err() != nil {
		interrupted()
		return "", "", false, false
	}
	if refused != nil {
		// Its running row refused: its process ended under it, and the
		// check fails with the ledger's words, its receipt finished
		// with them.
		fin.State = ledger.CheckFinished
		words = unrecorded + ": " + session.CleanMessage(refused.Error())
		fin.Error = words
		_ = e.transact(context.Background(), r, func() tx {
			out := event("finished", map[string]any{"exit": res.Exit, "timedOut": res.TimedOut, "stable": false, "duration": res.Duration.String(), "error": words})
			out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
			return out
		}, nil)
		return stage, words, true, false
	}
	if e.beforeCompare != nil {
		e.beforeCompare(ctx, ch.ID)
	}
	moved, changed, untracked, cerr := checkoutIntact(ctx, dir, a.result)
	if ctx.Err() != nil {
		// Closed while the checkout was compared: its git commands were
		// cut, so the comparison says nothing of the check, and the
		// receipt is interrupted, not a checkout that could not be
		// compared (Codex's review of #498).
		interrupted()
		return "", "", false, false
	}
	fin.State, fin.FinishedAt, fin.Stable = ledger.CheckFinished, time.Now().UTC(), cerr == nil && !moved && len(changed) == 0
	words = checkWords(ch, res, moved, changed, cerr)
	if ids := e.serviceIDs(tr); len(ids) > 0 && words != "" && words == intactWords(moved, changed, cerr) {
		// The tree's services ran beside the check: the comparison
		// cannot tell their writes from its own.
		words = fmt.Sprintf("%s, or a service of the tree (%s), %s", stage, strings.Join(ids, ", "), words)
	}
	fin.Error = session.CleanMessage(words)
	// The check's outcome is evidence: not recorded, the result is not
	// taken on it. A task's check gives its refused receipt up, as it
	// always did; a verify step's is a manifest step's receipt, kept for
	// the next commit as runStep keeps one (no undo), so the attempt's
	// failure writes it, its log with it, and it is never left running
	// (Codex's review of #498).
	undo := giveUp
	if step {
		undo = nil
	}
	if err := e.transact(context.Background(), r, func() tx {
		out := event("finished", map[string]any{"exit": res.Exit, "timedOut": res.TimedOut, "stable": fin.Stable,
			"changed": bounded(changed), "changedCount": len(changed), "untracked": bounded(untracked), "untrackedCount": len(untracked), "duration": res.Duration.String(), "error": words})
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
		return out
	}, undo); err != nil {
		what := "the ledger could not record the check's outcome"
		if step {
			what = "the ledger could not record the outcome of the step " + ch.ID
		}
		return stage, what + ": " + session.CleanMessage(err.Error()), true, false
	}
	if words != "" {
		return stage, words, true, false
	}
	return "", "", true, true
}

// serviceIDs is the ids of t's services, in the order they started; none
// for no tree.
func (e *Engine) serviceIDs(t *tree) []string {
	if t == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []string
	for _, ts := range t.services {
		ids = append(ids, ts.ch.ID)
	}
	return ids
}

// checkWords says why a check failed, "" for a pass.
func checkWords(ch Check, res proc.Result, moved bool, changed []string, cerr error) string {
	switch {
	case res.Err != "" && !res.TimedOut && !res.Canceled:
		return "could not run: " + session.CleanMessage(res.Err)
	case res.TimedOut:
		return fmt.Sprintf("timed out after %s%s", checkTimeout(ch), tailWords(lastOutput(res, checkTail)))
	case res.Canceled:
		return "cancelled"
	case res.Exit != 0:
		return fmt.Sprintf("exited %d%s", res.Exit, tailWords(lastOutput(res, checkTail)))
	}
	return intactWords(moved, changed, cerr)
}

// intactWords says what a command did to the checkout, "" for nothing.
func intactWords(moved bool, changed []string, err error) string {
	switch {
	case err != nil:
		return "left a checkout that could not be compared: " + session.CleanMessage(err.Error())
	case moved:
		return "moved the checkout to another commit"
	case len(changed) > 0:
		return "changed tracked files: " + changedList(changed)
	}
	return ""
}

// changedList is the paths a comparison found changed, as words name them:
// the first ten, and a mark when there are more.
func changedList(changed []string) string {
	list := changed
	if len(list) > 10 {
		list = append(slices.Clone(list[:10]), "…")
	}
	return strings.Join(list, ", ")
}

// checkoutIntact compares the checkout at dir with the commit as git
// would store it: HEAD still the commit, and every tracked path's
// content, mode and link target as the commit's tree has them, read
// through an index of its own (read-tree, add -u, write-tree) that
// carries no flag a command could set to hide a change. It reports
// whether HEAD moved, the tracked paths that differ, and the untracked
// paths left (ignored ones aside).
func checkoutIntact(ctx context.Context, dir, commit string) (moved bool, changed, untracked []string, err error) {
	head, err := headCommit(ctx, dir)
	if err != nil {
		return false, nil, nil, err
	}
	if head != commit {
		return true, nil, nil, nil
	}
	f, err := os.CreateTemp("", "conductor-index-")
	if err != nil {
		return false, nil, nil, err
	}
	index := f.Name()
	f.Close()
	os.Remove(index) // git makes it
	defer os.Remove(index)
	env := map[string]string{"GIT_INDEX_FILE": index}
	if modesKept(dir) {
		// The filesystem keeps the executable bit: the comparison keeps it
		// too, whatever core.fileMode the repository set, so a check that
		// makes a file executable is seen.
		env["GIT_CONFIG_COUNT"], env["GIT_CONFIG_KEY_0"], env["GIT_CONFIG_VALUE_0"] = "1", "core.fileMode", "true"
	}
	if _, err := gitcli.RunEnv(ctx, dir, env, "read-tree", commit); err != nil {
		return false, nil, nil, err
	}
	if _, err := gitcli.RunEnv(ctx, dir, env, "add", "-u"); err != nil {
		return false, nil, nil, err
	}
	tree, err := gitcli.RunEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return false, nil, nil, err
	}
	want, err := gitcli.Run(ctx, dir, "rev-parse", commit+"^{tree}")
	if err != nil {
		return false, nil, nil, err
	}
	if strings.TrimSpace(tree) != strings.TrimSpace(want) {
		out, err := gitcli.RunEnv(ctx, dir, env, "diff", "--cached", "--name-only", "-z", commit)
		if err != nil {
			return false, nil, nil, err
		}
		changed = splitNUL(out)
		if len(changed) == 0 {
			changed = []string{"(the tree differs)"}
		}
	}
	out, err := gitcli.RunEnv(ctx, dir, env, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, changed, nil, err
	}
	return false, changed, splitNUL(out), nil
}

// maxListed and maxListedBytes bound a path list in an event's payload: a
// path may be 4 KiB, and an event at most ledger.MaxPayload.
const (
	maxListed      = 50
	maxListedBytes = 8 << 10
)

// bounded is the first of paths, at most maxListed and maxListedBytes of
// them, with a mark saying how many more there are.
func bounded(paths []string) []string {
	n, size := 0, 0
	for n < len(paths) && n < maxListed && size+len(paths[n]) <= maxListedBytes {
		size += len(paths[n])
		n++
	}
	if n == len(paths) {
		return paths
	}
	return append(slices.Clone(paths[:n]), fmt.Sprintf("… and %d more", len(paths)-n))
}

// modesKept reports whether the filesystem under dir keeps the executable
// bit, by setting it on a file made there for the look and removed.
func modesKept(dir string) bool {
	f, err := os.CreateTemp(dir, ".conductor-mode-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	if err := os.Chmod(name, 0o755); err != nil {
		return false
	}
	fi, err := os.Lstat(name)
	return err == nil && fi.Mode().Perm()&0o100 != 0
}

// splitNUL splits a -z listing into its paths.
func splitNUL(out string) []string {
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// lastOutput is the last n bytes of what the process printed, whether or
// not its output outgrew the head proc.Result keeps first: a short output
// is all in Head and Tail is empty then, which left a failed check's words
// and its recorded tail empty (#355).
func lastOutput(res proc.Result, n int) []byte {
	all := res.Tail
	if !res.Truncated {
		all = append(append([]byte(nil), res.Head...), res.Tail...)
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
