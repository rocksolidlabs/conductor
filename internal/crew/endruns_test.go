package crew

import (
	"context"
	"slices"
)

// endRuns ends the engine's runs and its processes, once, at the first of
// its processes' cleanups. The runs end first (cancelRuns), so that nothing
// reacts to a process's end; then every process the launcher started ends,
// so that no start under way waits on one still running (a prompt the
// cancel cut short waits up to a second for its session to end, endsSoon);
// then, for a test that passed, the starts, handoffs and checks under way
// are waited out (waitRuns).
//
// An engine left running past its test saw its processes end at the test's
// cleanup, failed the attempt they ran and dispatched the next one, whose
// `git worktree add` wrote into the test's repository while t.TempDir
// removed it: "TempDir RemoveAll cleanup: ... .git: directory not empty"
// (#391).
//
// A test that failed is not waited for: it may have stopped with a gate of
// its own still shut (a fake ledger's held transaction, a hook's release),
// and waiting on the work behind that gate would hang the package's run
// where the test only failed. What it leaves behind misleads nothing.
func (f *fakeLauncher) endRuns() {
	f.ended.Do(func() {
		if f.engine == nil {
			return
		}
		runs := cancelRuns(f.engine)
		f.mu.Lock()
		procs := slices.Clone(f.all)
		f.mu.Unlock()
		for _, p := range procs {
			p.End(0)
		}
		if !f.t.Failed() {
			waitRuns(f.engine, runs)
		}
	})
}

// cancelRuns ends e's runs as the server's end would, recording nothing:
// each run stops dispatching and its context ends, so that no attempt's
// watcher takes its session's end for a failure (watchAttempt returns with
// the run) and nothing is dispatched after the test. The ledger keeps what
// the test left in it: a test that abandoned an engine, as the server that
// ended before a continuation, wrote the rows the recovery would.
func cancelRuns(e *Engine) []*run {
	e.mu.Lock()
	runs := make([]*run, 0, len(e.runs))
	cancels := make([]context.CancelFunc, 0, len(e.runs))
	for _, r := range e.runs {
		r.stopping = true
		runs = append(runs, r)
		cancels = append(cancels, r.cancel)
		// The services of its trees end with the teardown context they
		// run under, killed with their groups, as at the shutdown's cut.
		cancels = append(cancels, r.teardownCancel)
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
	return runs
}

// waitRuns waits out the starts (the prerequisites among them), handoffs
// and checks under way in runs, which cancelRuns ended: none is added after
// it (each Add is made under e.mu by code that refuses a stopping run);
// then the trees being ended, whose last commits follow their attempts';
// then the watchers of every service a tree of theirs started (a
// preview's among them), killed with the teardown, each recording its end.
func waitRuns(e *Engine, runs []*run) {
	for _, r := range runs {
		waitFor(context.Background(), &r.starts)
		waitFor(context.Background(), &r.deliveries)
		waitFor(context.Background(), &r.checks)
		e.waitTrees(context.Background(), r)
		e.mu.Lock()
		var watched []chan struct{}
		for _, t := range r.tasks {
			for _, a := range t.attempts {
				trees := slices.Clone(a.trees)
				if p := a.previewing; p != nil && p.t != nil {
					trees = append(trees, p.t) // its preview's, never a.trees
				}
				for _, tr := range trees {
					for _, ts := range tr.services {
						watched = append(watched, ts.watched)
					}
				}
			}
		}
		e.mu.Unlock()
		for _, w := range watched {
			<-w
		}
	}
}
