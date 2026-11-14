package crew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// The words a receipt's error begins with when the engine, not the
// command, is why it failed: the ledger refused the receipt's running row
// (its process ended under it) or its outcome, or the checkout could not
// be compared after the command. ClassifyFailure tells them by these.
const (
	unrecordedWords = "the ledger could not record the "
	uncomparedWords = "left a checkout that could not be compared"
)

// unrecordedOutcome is the words an attempt fails with, before the
// ledger's own, when the ledger refused the outcome of its check (phase
// check) or of its verify step id: words no refused start or running row
// has, so ClassifyFailure tells a refused outcome from them.
func unrecordedOutcome(phase, id string) string {
	if phase == ledger.PhaseVerify {
		return unrecordedWords + "outcome of the step " + id
	}
	return unrecordedWords + "check's outcome"
}

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
		tr, err = e.resolveTree(ctx, r, a, a, ledger.TreeCheckout, dir, cwd, a.result)
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
	row := ledger.CheckRow{RunID: r.id, TaskID: t.def.ID, N: a.n, ID: ch.ID, K: k, Phase: phase, Tries: 1, Argv: slices.Clone(ch.Argv), Dir: cwd, Subject: a.result, State: ledger.CheckStarted, Exit: -1, StartedAt: time.Now().UTC(),
		Where: string(a.where)}
	if tr != nil {
		row.Ports = maps.Clone(tr.ports)
		if step {
			row.Manifest = tr.man.identity()
		}
	}
	// event is the receipt's event of the kind (started, running,
	// finished), with more in its payload: each names the receipt (its
	// task, attempt, id and k), so the paths a finished one lists are read
	// back for the next attempt after a restart (ledger.CheckChanged).
	event := func(kind string, more map[string]any) tx {
		if step {
			return ev("step."+kind, stepPayload(a, tr, phase, ch.ID, k, more))
		}
		p := map[string]any{"task": t.def.ID, "attempt": a.n, "check": ch.ID, "k": k}
		maps.Copy(p, more)
		return ev("check."+kind, p)
	}
	unrecorded := unrecordedWords + "check"
	if step {
		unrecorded = unrecordedWords + "step " + ch.ID
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
		res = e.runCommand(runCtx, r, procRef(r, a, ledger.TreeCheckout), cwd, ch.Argv, checkTimeout(ch), tr.env(r.id), &log, func(p proc.Process) {
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
		// The finished transaction has no undo: refused too, its row and
		// event stay for the next commit, which writes them before the
		// attempt's failure, so the receipt is kept either way, as a verify
		// step's refused outcome keeps its (Codex's review of #559, round 2).
		fin.State = ledger.CheckFinished
		words = unrecorded + ": " + session.CleanMessage(refused.Error())
		fin.Error = words
		_ = e.transact(context.Background(), r, func() tx {
			out := event("finished", map[string]any{"exit": res.Exit, "timedOut": res.TimedOut, "stable": false, "duration": res.Duration.String(), "error": words})
			out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
			return out
		}, nil)
		e.keepFailed(r, a, fin, nil)
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
	// The finished event, within the ledger's bound with the receipt's
	// identity and the paths the next attempt is handed (fitFinished), and
	// those paths as the event keeps them (eventChanged).
	finished := event("finished", map[string]any{"exit": res.Exit, "timedOut": res.TimedOut, "stable": fin.Stable,
		"changed": bounded(changed), "changedCount": len(changed), "untracked": bounded(untracked), "untrackedCount": len(untracked), "duration": res.Duration.String(), "error": words})
	fitFinished(finished.events[0].payload, changed)
	handed := eventChanged(finished.events)
	if err := e.transact(context.Background(), r, func() tx {
		out := finished
		out.more = func(ltx LedgerTx) error { return ltx.PutCheck(fin) }
		return out
	}, undo); err != nil {
		what := unrecordedOutcome(phase, ch.ID)
		if step {
			if words != "" {
				// Its row and event kept for the next commit (no undo),
				// which writes them before the attempt's failure: the
				// receipt is the ledger's as the attempt's failure is, and
				// the next attempt is handed it (Codex's review of #559).
				e.keepFailed(r, a, fin, handed)
			}
		}
		return stage, what + ": " + session.CleanMessage(err.Error()), true, false
	}
	if words != "" {
		e.keepFailed(r, a, fin, handed)
		return stage, words, true, false
	}
	return "", "", true, true
}

// fitFinished keeps a check's or a verify step's finished payload p within
// ledger.MaxPayload as JSON, so writable never makes it a note that drops
// the receipt's k and the paths it changed (Codex's review of #559): the
// lists are bounded in bytes, not as JSON, where a control character or a
// byte that is not UTF-8 takes six. Past the bound the untracked paths go
// first (their count kept), then the error's words are cleaned and cut
// (session.CleanMessage), then the changed paths are cut to those the next
// attempt is handed (handedChanged) with a mark of how many more, then
// those, the last first. The identity (task, attempt, id or check, k) is
// never touched.
func fitFinished(p map[string]any, changed []string) {
	fits := func() bool {
		b, err := json.Marshal(p)
		return err == nil && len(b) <= ledger.MaxPayload
	}
	if fits() {
		return
	}
	if n, _ := p["untrackedCount"].(int); n > 0 {
		p["untracked"] = []string{fmt.Sprintf("… and %d more", n)}
		if fits() {
			return
		}
	}
	if w, ok := p["error"].(string); ok {
		p["error"] = session.CleanMessage(w)
		if fits() {
			return
		}
	}
	list := handedChanged(changed)
	for {
		out := list
		if len(list) < len(changed) {
			out = append(slices.Clone(list), fmt.Sprintf("… and %d more", len(changed)-len(list)))
		}
		p["changed"] = out
		if fits() || len(list) == 0 {
			return
		}
		list = list[:len(list)-1]
	}
}

// keepFailed keeps on a the receipt that failed it, as the ledger has its
// row (fin, its log left out: the next attempt reads that from the ledger)
// with the tracked paths the check changed as its finished event keeps
// them (eventChanged), for the attempt after it (failedcheck.go).
func (e *Engine) keepFailed(r *run, a *attempt, fin ledger.CheckRow, changed []string) {
	fin.Log = nil
	fc := &failedCheck{run: r.id, row: fin, changed: handedChanged(changed)}
	e.mu.Lock()
	a.failed = fc
	e.mu.Unlock()
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
		return uncomparedWords + ": " + session.CleanMessage(err.Error())
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
// paths left (ignored ones aside). git finds the checkout's repository
// from dir (checkoutIntactBy).
func checkoutIntact(ctx context.Context, dir, commit string) (moved bool, changed, untracked []string, err error) {
	return checkoutIntactBy(dir, func(env map[string]string, args ...string) (string, error) {
		return gitcli.RunEnv(ctx, dir, env, args...)
	}, commit)
}

// checkoutIntactBy is the comparison of checkoutIntact with every git
// command of it run by git, which says where git finds the checkout (a
// preview's reuse names its git directory and work tree outright).
func checkoutIntactBy(dir string, git func(env map[string]string, args ...string) (string, error), commit string) (moved bool, changed, untracked []string, err error) {
	out, err := git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return false, nil, nil, err
	}
	if head := strings.TrimSpace(out); head != commit {
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
	if _, err := git(env, "read-tree", commit); err != nil {
		return false, nil, nil, err
	}
	if _, err := git(env, "add", "-u"); err != nil {
		return false, nil, nil, err
	}
	tree, err := git(env, "write-tree")
	if err != nil {
		return false, nil, nil, err
	}
	want, err := git(nil, "rev-parse", commit+"^{tree}")
	if err != nil {
		return false, nil, nil, err
	}
	if strings.TrimSpace(tree) != strings.TrimSpace(want) {
		out, err := git(env, "diff", "--cached", "--name-only", "-z", commit)
		if err != nil {
			return false, nil, nil, err
		}
		changed = splitNUL(out)
		if len(changed) == 0 {
			changed = []string{"(the tree differs)"}
		}
	}
	out, err = git(env, "ls-files", "--others", "--exclude-standard", "-z")
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
// bit, by setting it on a file made there for the look, through the file
// it opened, and removed.
func modesKept(dir string) bool {
	f, err := os.CreateTemp(dir, ".conductor-mode-")
	if err != nil {
		return false
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o755); err != nil {
		return false
	}
	fi, err := f.Stat()
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

// A preview's tree as the label of an attempt's preview reads it (Program
// item PV1, docs/plans/browser-evidence.md "The label"): the tree's own
// facts, never what a page says it shows. TreeRevision reads what the tree
// holds now through an index of the reader's own; TreeClasses classes the
// paths a page's source hints name.

// WorktreePreview is an attempt's worktree while its preview is offered:
// the URL as the manifest gave it (naming ${CONDUCTOR_TREE_SECRET}, never
// its value), the run's working directory (Root, which the worktree is
// under) and the worktree's directory, the tree's secret and where the
// attempt runs. The secret is given to the server's own label and capture
// routes alone, which put it in the admin's URL and scrub it from what a
// capture keeps; nothing else of the engine's gives it.
type WorktreePreview struct {
	URL    string
	Root   string
	Dir    string
	Secret string
	Where  string
}

// WorktreePreview is the worktree preview of the attempt n of the task of
// the run, as AttemptState.Preview offers it: from its worktree's
// tree.ready until the attempt's close, a service of the worktree exiting
// on its own, or the run's stop; ok false otherwise, and for a run the
// engine does not hold.
func (e *Engine) WorktreePreview(runID, taskID string, n int) (WorktreePreview, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.runs[runID]
	if r == nil || r.stopping {
		return WorktreePreview{}, false
	}
	t := r.task(taskID)
	if t == nil {
		return WorktreePreview{}, false
	}
	for _, a := range t.attempts {
		if a.n != n || a.preview == "" {
			continue
		}
		for _, tr := range a.trees {
			if tr.kind == ledger.TreeWorktree && tr.preview == a.preview {
				return WorktreePreview{URL: a.preview, Root: r.cwd, Dir: tr.dir, Secret: tr.secret, Where: string(a.where)}, true
			}
		}
	}
	return WorktreePreview{}, false
}

// TreeSecrets is the secret of every tree the engine holds one for now,
// for the server's routes to scrub from what they answer or keep.
func (e *Engine) TreeSecrets() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, r := range e.runs {
		for _, t := range r.tasks {
			for _, a := range t.attempts {
				for _, tr := range a.trees {
					if tr.secret != "" {
						out = append(out, tr.secret)
					}
				}
			}
		}
	}
	for _, p := range e.previews {
		if p.t != nil && p.t.secret != "" {
			out = append(out, p.t.secret)
		}
	}
	return out
}

// TreeRead is a tree's revision as one read found it: HEAD's commit; the
// tree id of everything the tree holds that git would see (Content: the
// commit's tree with every change and every file that is not ignored);
// the paths that differ from HEAD, all of them counted (Changed) and the
// first of them kept (Paths) within treeReadPaths and treeReadPathBytes.
type TreeRead struct {
	Commit  string
	Content string
	Changed int
	Paths   []string
}

// The bounds of a read's kept paths, and of one path read (a path the
// system names is far shorter; a longer one is counted, never kept).
const (
	treeReadPaths     = 200
	treeReadPathBytes = 16 << 10
	treeReadPathMax   = 64 << 10
)

// ErrTreeMoved is a read that refused a tree: it is not reached as the
// engine made it.
var ErrTreeMoved = errors.New("the tree is not reached as the engine made it")

// Tree is an attempt's worktree opened for a read (OpenTree): its
// directory held open; and at, a path that names that open directory
// (gitcli.OpenedPath: on Linux the open handle, elsewhere its path,
// compared again after the read by Still), where every git command of the
// read runs, with the tree's own .git (at/.git) and the work tree (at)
// named outright (gitcli.RunTree): git looks for no repository.
type Tree struct {
	dir   *os.File
	at    string
	still func() bool
}

// beforeTreeOpen, when a test sets it, is called in OpenTree's walk with
// root before the run's folder is opened, and with each name beneath it
// after the name was looked at and before it is opened.
var beforeTreeOpen func(name string)

// OpenTree resolves where a read of the worktree at dir, which the engine
// made under root (the run's working directory, an absolute path), runs.
// root is opened as a directory only (root and "/.", as internal/session's
// openDirOnly does), and from it each name of dir under it is looked at
// without following it, must be a directory, is opened through the one
// before it as a directory only (the name and "/.") and must be the
// directory that was looked at (os.SameFile): anything else is
// ErrTreeMoved. A name that is no directory at the moment of its open
// fails at once and the tree is not read: for root the failure is the
// system's error, for a name beneath it ErrTreeMoved. A link put in place
// of a name between its look and its open is followed only when it leads
// to the very directory that was looked at (the comparison after the open
// holds); any other is refused (ErrTreeMoved). The server opens nothing
// else in the tree but the one file of its own the look at the executable
// bit makes and removes (modesKept): git opens every existing file of the
// tree and of its git directory, the git directory the tree's own .git
// names, whose configuration applies, as for the engine's comparison of a
// checkout (checkoutIntact); a tree with no .git at its top is no
// repository to git (gitcli.ErrNotRepo).
func OpenTree(root, dir string) (*Tree, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, ErrTreeMoved
	}
	if beforeTreeOpen != nil {
		beforeTreeOpen(root)
	}
	cur, err := os.OpenRoot(root + string(filepath.Separator) + ".")
	if err != nil {
		return nil, err
	}
	defer func() { cur.Close() }()
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		fi, err := cur.Lstat(name)
		if err != nil || !fi.IsDir() {
			return nil, ErrTreeMoved
		}
		if beforeTreeOpen != nil {
			beforeTreeOpen(name)
		}
		next, err := cur.OpenRoot(name + string(filepath.Separator) + ".")
		if err != nil {
			return nil, ErrTreeMoved
		}
		if got, err := next.Stat("."); err != nil || !os.SameFile(fi, got) {
			next.Close()
			return nil, ErrTreeMoved
		}
		cur.Close()
		cur = next
	}
	f, err := cur.Open(".")
	if err != nil {
		return nil, err
	}
	t := &Tree{dir: f}
	t.at, t.still = gitcli.OpenedPath(f, dir)
	return t, nil
}

// Close lets the tree's directory go.
func (t *Tree) Close() { t.dir.Close() }

// Still says the path the tree was opened at names it still: always where
// git runs in the open directory itself (gitcli.OpenedPath).
func (t *Tree) Still() bool { return t.still() }

// TreeIndex is a reader's own index of a tree: the file at Path, kept
// between reads, used by one read at a time. At its first read (or the
// first after a cut read's index is dropped), and at a read whose lists
// of paths pass their bound, every tracked file is compared once, its
// stat data brought up to date; after it the kept index holds the stat
// data as it is now, and no flag or count of it is carried from one read
// to the next. Edited and new files are hashed at every read; a tracked
// file whose bytes and stat data did not change is not hashed, and one
// whose stat data changed is hashed once, at the next read, as git status
// works (git itself hashes a file whose times cannot tell it from the
// index's: one changed in the same moment as the index was written), so
// a change of the attributes that say how a file is recorded shows for
// the files that change after it (checkoutIntact, which decides a
// candidate, reads with a fresh index). A read that needs the refresh on
// a very large tree can run out of its time on a busy machine: it answers
// the commit alone, and cut while the refresh holds the index's lock, the
// next read starts from a new index. A first read that was cut after its
// index was made and before the index's stat data was brought up to date
// leaves an index the next read does not refresh: that read hashes every
// tracked file once; its answer is the same. And whether the file system
// under the tree keeps the executable bit (modesKept), looked at once, at
// the index's first read, with one new file of the server's own made in
// the tree and removed.
type TreeIndex struct {
	Path          string
	looked, modes bool
}

// Objects is the folder beside the index where git writes every object
// of a read in it (gitcli.RunTree), its trees alone: TreeRevision empties
// it as a read starts, so it holds at most one read's trees, and Remove
// removes it.
func (ix *TreeIndex) Objects() string { return ix.Path + ".objects" }

// Remove removes the index, its lock, its lists of paths and its folder
// of objects.
func (ix *TreeIndex) Remove() {
	_, _, _, _, _ = os.Remove(ix.Path), os.Remove(ix.Path+".lock"), os.Remove(ix.Path+".gone"), os.Remove(ix.Path+".paths"), os.RemoveAll(ix.Objects())
}

// env is the environment of git in a read of t in ix: the reader's own
// index, no replacement object, every pathspec literal, and the forced
// configuration: the stat settings (the stat data trusted in full), the
// sparse checkout (none), the split index (none: the index is one file)
// and, where the file system keeps it, the executable bit. Every other
// setting of the repository's configuration applies.
func (t *Tree) env(ix *TreeIndex) map[string]string {
	if !ix.looked {
		ix.modes, ix.looked = modesKept(t.at), true
	}
	env := map[string]string{"GIT_INDEX_FILE": ix.Path, "GIT_LITERAL_PATHSPECS": "1", "GIT_NO_REPLACE_OBJECTS": "1"}
	cfg := [][2]string{{"core.ignoreStat", "false"}, {"core.checkStat", "default"}, {"core.trustctime", "true"}, {"core.sparseCheckout", "false"},
		{"core.splitIndex", "false"}}
	if ix.modes {
		cfg = append(cfg, [2]string{"core.fileMode", "true"})
	}
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(len(cfg))
	for i, kv := range cfg {
		env["GIT_CONFIG_KEY_"+strconv.Itoa(i)], env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = kv[0], kv[1]
	}
	return env
}

// treeGit runs every git command of a tree's read (gitcli.RunTree); a
// test wraps it.
var treeGit = gitcli.RunTree

// git runs git with args in t, in the index ix: in on its input (nil for
// none), its output written to w.
func (t *Tree) git(ctx context.Context, ix *TreeIndex, in io.Reader, w io.Writer, args ...string) error {
	env := t.env(ix)
	if args[0] == "check-ignore" {
		// check-ignore takes no pathspec magic, the literal one neither:
		// its paths come on its input, none with a magic prefix
		// (TreeClasses).
		delete(env, "GIT_LITERAL_PATHSPECS")
	}
	return treeGit(ctx, t.at, ix.Objects(), env, in, w, args...)
}

// SetTreeGitForTest makes run the runner of every git command of a
// tree's read, until the returned function puts the one before back.
func SetTreeGitForTest(run func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error) (restore func()) {
	prev := treeGit
	treeGit = run
	return func() { treeGit = prev }
}

// SetTreeOpenForTest makes f the walk's seam (beforeTreeOpen), until the
// returned function puts the one before back.
func SetTreeOpenForTest(f func(name string)) (restore func()) {
	prev := beforeTreeOpen
	beforeTreeOpen = f
	return func() { beforeTreeOpen = prev }
}

// maxTreeID bounds what a read keeps of a command that answers an id.
const maxTreeID = 1 << 10

// TreeRevision reads the tree t as it is now, in the index ix, through
// git with argv in the C locale, every command run in the tree's opened
// directory with the tree's own .git and the work tree named outright
// (gitcli.RunTree), so that git looks for no repository; each command
// writes its answer and its error stream within a bound:
// rev-parse --verify HEAD^{commit}, the commit recorded; read-tree
// --reset of that commit, the one-tree merge of -m that discards what it
// cannot merge instead of failing (an entry an earlier read staged whose
// file has changed again), so the index is the commit's entries again,
// each keeping its stat data only where the entry is the commit's, and
// nothing an earlier read staged survives into this one; where the read
// made the index anew (there was none, or it dropped one a cut read
// left), update-index -q --refresh, which brings each entry's stat data
// up to date, hashing every tracked file once and writing no object; the
// paths that differ from the index, each with its kind (diff-files
// --name-status, which compares stat data, hashes a file only where its
// times cannot tell, and starts no git in a submodule), and the new paths
// that are not ignored (ls-files -o), sorted into two lists kept in files
// beside the index within maxStagePaths together (stageList); where the
// lists pass it at a read that did not make its index anew, one
// update-index -q --refresh and the two listings again, lists still past
// it failing the read (never more than one refresh in a read; an
// attributes file listed gone or of another type fails it at once); then
// update-index --force-remove with the paths gone or of another type on
// its input, which removes each from the index without looking it up in
// the tree, and update-index --add --remove --info-only with the paths
// there (changed, of another type, new), which records each by the hash
// of its content as git converts it and writes no object; write-tree
// --missing-ok, the content; and diff --cached --name-only -z --no-renames
// of the commit, which reads trees alone (a moved file is two paths).
// What each writes is bounded by its writer: an id is kept within
// maxTreeID, read-tree's and update-index's output is dropped, and the
// diff's is read as it comes, counted whole and kept to its bounds
// (pathList). Reads of one index run one at a time, so a lock of the
// index's there as a read starts is one a cut read left: the index and its
// lock are dropped, and the read starts from none. The only objects a read
// writes are write-tree's trees, into the index's folder (ix.Objects),
// emptied as the read starts: read-tree --reset makes every entry the
// commit's again, so nothing a read needs was left there by another. Its
// time is ctx's. A read that cannot name HEAD's commit is an error; one
// that fails after gives the commit with no content.
//
// The content is the id of the tree the read stages, every path that
// differs named by the hash of its content as git converts it: the tree
// git add -A makes, with these differences. Where add would refuse the
// tree, the read still answers, with what it staged: a file whose
// conversion add refuses (a line-ending check, an encoding git cannot
// read) is hashed as it is converted without that check, and a folder
// that is a repository with no commit, in place of a tracked file, is left
// out. In a repository set to ignore case, a new path under a folder whose
// case differs from the index's is staged under the tree's own spelling.
// Where a path changed its kind and is now a file (a tracked link, or a
// submodule's entry, replaced by a file), the read removes the old entry
// before it stages the file, where git's add judges the file's line
// endings with the old entry still in the index: when that entry's
// content holds a carriage return and the tree converts line endings by
// itself (core.autocrlf, or text=auto), add keeps the file's line endings
// as they are and the read converts them, and the two ids differ.
// And the read answers the commit alone where it does not label: an
// attributes file (.gitattributes, in any folder) gone or of another type
// than the index has, and not committed (errTreeAttributes, before
// anything is staged and with no refresh for it); a tracked folder
// replaced by a link the server cannot follow (a link to itself, a loop,
// a folder it cannot search); lists that pass maxStagePaths after one
// refresh.
func TreeRevision(ctx context.Context, t *Tree, ix *TreeIndex) (TreeRead, error) {
	if _, err := os.Lstat(ix.Path + ".lock"); err == nil {
		_ = os.Remove(ix.Path)
		if err := os.Remove(ix.Path + ".lock"); err != nil {
			return TreeRead{}, err
		}
	}
	_, err := os.Lstat(ix.Path)
	anew := errors.Is(err, os.ErrNotExist)
	if err := os.RemoveAll(ix.Objects()); err != nil {
		return TreeRead{}, err
	}
	if err := os.Mkdir(ix.Objects(), 0o700); err != nil {
		return TreeRead{}, err
	}
	head := &gitcli.Head{N: maxTreeID}
	if err := t.git(ctx, ix, nil, head, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return TreeRead{}, err
	}
	out := TreeRead{Commit: strings.TrimSpace(head.String())}
	if err := t.git(ctx, ix, nil, io.Discard, "read-tree", "--reset", "--no-sparse-checkout", out.Commit); err != nil {
		return out, err
	}
	if anew {
		if err := t.git(ctx, ix, nil, io.Discard, "update-index", "-q", "--refresh"); err != nil {
			return out, err
		}
	}
	staged := &stageList{}
	for i, name := range []string{".gone", ".paths"} {
		f, err := os.OpenFile(ix.Path+name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return out, err
		}
		defer func() { _, _ = f.Close(), os.Remove(f.Name()) }()
		staged.f[i] = f
	}
	for refreshed := anew; ; refreshed = true {
		for i, args := range [][]string{{"diff-files", "--name-status", "-z", "--no-renames", "--ignore-submodules=dirty"}, {"ls-files", "-o", "--exclude-standard", "-z"}} {
			staged.status, staged.kind, staged.cur = i == 0, nil, nil
			if err := t.git(ctx, ix, nil, staged, args...); err != nil {
				return out, err
			}
		}
		if staged.err == nil || refreshed || errors.Is(staged.err, errTreeAttributes) {
			break
		}
		// A later read whose lists pass the bound brings the index's stat
		// data up to date once, as a new index's read does, and lists the
		// tree again from empty lists.
		if err := t.git(ctx, ix, nil, io.Discard, "update-index", "-q", "--refresh"); err != nil {
			return out, err
		}
		for _, f := range staged.f {
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return out, err
			}
			if err := f.Truncate(0); err != nil {
				return out, err
			}
		}
		staged.n, staged.err = 0, nil
	}
	if staged.err != nil {
		return out, staged.err
	}
	for i, args := range [][]string{{"update-index", "--force-remove", "-z", "--stdin"}, {"update-index", "--add", "--remove", "--info-only", "-z", "--stdin"}} {
		if _, err := staged.f[i].Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		if err := t.git(ctx, ix, staged.f[i], io.Discard, args...); err != nil {
			return out, err
		}
	}
	tree := &gitcli.Head{N: maxTreeID}
	if err := t.git(ctx, ix, nil, tree, "write-tree", "--missing-ok"); err != nil {
		return out, err
	}
	paths := &pathList{}
	if err := t.git(ctx, ix, nil, paths, "diff", "--cached", "--name-only", "-z", "--no-renames", out.Commit); err != nil {
		return out, err
	}
	out.Content, out.Changed, out.Paths = strings.TrimSpace(tree.String()), paths.n, paths.kept
	return out, nil
}

// pathList is a -z listing read as it comes: every path counted, the
// first kept within treeReadPaths and treeReadPathBytes, and no more of
// any one path held than treeReadPathMax.
type pathList struct {
	cur   []byte
	long  bool
	n     int
	bytes int
	kept  []string
}

func (l *pathList) Write(p []byte) (int, error) {
	for _, c := range p {
		if c != 0 {
			if len(l.cur) < treeReadPathMax {
				l.cur = append(l.cur, c)
			} else {
				l.long = true
			}
			continue
		}
		if len(l.cur) > 0 || l.long {
			l.n++
			if !l.long && len(l.kept) < treeReadPaths && l.bytes+len(l.cur) <= treeReadPathBytes {
				l.kept, l.bytes = append(l.kept, string(l.cur)), l.bytes+len(l.cur)
			}
		}
		l.cur, l.long = l.cur[:0], false
	}
	return len(p), nil
}

// errTreeAttributes is a read of a tree whose attributes file is gone or
// of another type than the index has, and not committed: the read does
// not guess which rules git's add would apply to which file, and answers
// the commit alone.
var errTreeAttributes = errors.New("the tree's attributes file changed in a way the read does not label")

// maxStagePaths bounds the lists of paths a read stages, in bytes
// together: the paths that differ from the index and the new ones, each
// NUL-ended in each list it is in.
const maxStagePaths = 16 << 20

// stageList sorts git's -z listings of the paths to stage into two files
// within maxStagePaths: f[0] the paths to remove from the index (gone, or
// of another type) and f[1] the paths to stage (every other, and those of
// another type again). Where status is set, each path follows its status
// (diff-files --name-status: D gone, T of another type); otherwise every
// path is new. A path is given as update-index takes it: a folder listed
// with a trailing slash (a repository with a commit inside the tree,
// which add -A records by its commit) is given without the slash. Past
// the bound nothing more is written, every later write is taken and
// dropped (git runs to its end), and err says the lists are past it; so
// too from an attributes file (.gitattributes, in any folder) listed gone
// or of another type, and err is errTreeAttributes.
type stageList struct {
	f      [2]*os.File
	status bool
	kind   []byte
	cur    []byte
	n      int
	err    error
}

func (l *stageList) Write(p []byte) (int, error) {
	var out [2][]byte
	for _, c := range p {
		if c != 0 {
			if len(l.cur) <= maxStagePaths {
				l.cur = append(l.cur, c)
			}
			continue
		}
		path := bytes.TrimSuffix(l.cur, []byte("/"))
		if l.status && l.kind == nil {
			l.kind, l.cur = l.cur, nil
			continue
		}
		gone := len(l.kind) > 0 && (l.kind[0] == 'D' || l.kind[0] == 'T')
		there := len(l.kind) == 0 || l.kind[0] != 'D'
		if gone && l.err == nil && (string(path) == ".gitattributes" || bytes.HasSuffix(path, []byte("/.gitattributes"))) {
			l.err = errTreeAttributes
		}
		for i, in := range []bool{gone, there} {
			if in && len(path) > 0 && l.err == nil {
				if l.n+len(path)+1 > maxStagePaths {
					l.err = fmt.Errorf("the paths to stage pass %d bytes", maxStagePaths)
					break
				}
				out[i] = append(append(out[i], path...), 0)
				l.n += len(path) + 1
			}
		}
		l.kind, l.cur = nil, l.cur[:0]
	}
	for i := range out {
		if len(out[i]) == 0 {
			continue
		}
		if _, err := l.f[i].Write(out[i]); err != nil && l.err == nil {
			l.err = err
		}
	}
	return len(p), nil
}

// ErrClassesCut is TreeClasses when git answered more than the paths it
// was asked about: the classes are not known.
var ErrClassesCut = errors.New("the classes are not known: git answered more than was asked")

// TreeClasses classes paths of the tree t (relative to its top, as
// TreeRevision left the index ix) at commit, the commit that read
// recorded, asking about exactly those paths: tracked when commit's tree
// has the path (ls-tree, the paths after --), ignored when the tree's
// ignore rules leave it out (check-ignore, the paths on its input, one
// answer for each, a path that begins with git's magic ":" not given);
// the rest are neither. Every path ls-tree is given is a literal
// pathspec. Each command's answer is read within what was asked (an entry
// and its bytes for each path); past it nothing more is read and the
// classes are not known (ErrClassesCut).
func TreeClasses(ctx context.Context, t *Tree, ix *TreeIndex, commit string, paths []string) (tracked, ignored map[string]bool, err error) {
	if len(paths) == 0 {
		return map[string]bool{}, map[string]bool{}, nil
	}
	if !fullCommit(commit) {
		return nil, nil, fmt.Errorf("the classes: %q is not a commit id", commit)
	}
	size, input := 0, ""
	for _, p := range paths {
		size += len(p) + 1
		if !strings.HasPrefix(p, ":") {
			input += p + "\x00"
		}
	}
	lists := [2]map[string]bool{}
	for i, c := range []struct {
		in   io.Reader
		args []string
	}{
		{nil, append([]string{"ls-tree", "-z", "--name-only", "--full-tree", commit, "--"}, paths...)},
		{strings.NewReader(input), []string{"check-ignore", "-z", "--stdin"}},
	} {
		out := &askedList{bytes: size, entries: len(paths)}
		err := t.git(ctx, ix, c.in, out, c.args...)
		var exit *exec.ExitError
		switch {
		case out.over:
			return nil, nil, ErrClassesCut
		case err != nil && !(i == 1 && errors.As(err, &exit) && exit.ExitCode() == 1):
			// check-ignore exits 1 when no path is ignored.
			return nil, nil, err
		}
		lists[i] = map[string]bool{}
		for _, p := range out.paths {
			if slices.Contains(paths, p) {
				lists[i][p] = true
			}
		}
	}
	return lists[0], lists[1], nil
}

// askedList is a -z answer about the paths asked: at most entries paths
// and bytes bytes; past either the write fails and over is set.
type askedList struct {
	bytes, entries int
	cur            []byte
	paths          []string
	over           bool
}

func (l *askedList) Write(p []byte) (int, error) {
	if len(p) > l.bytes {
		l.over = true
		return 0, ErrClassesCut
	}
	l.bytes -= len(p)
	for _, c := range p {
		if c != 0 {
			l.cur = append(l.cur, c)
			continue
		}
		if len(l.paths) == l.entries {
			l.over = true
			return 0, ErrClassesCut
		}
		l.paths, l.cur = append(l.paths, string(l.cur)), l.cur[:0]
	}
	return len(p), nil
}
