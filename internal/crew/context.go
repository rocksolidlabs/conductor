package crew

import (
	"context"
	"path/filepath"

	"github.com/rocksolidlabs/conductor/ext"
)

// ContextFunc answers the context blocks an attempt about to start is to
// read, from the context providers (Program item C4): the server's, which
// asks every provider within ext.ContextWait, checks what each answers and
// bounds them together (internal/api, extcontext.go). It never fails the
// attempt: no block is an answer. It is called outside the engine's lock.
type ContextFunc func(ctx context.Context, q ext.ContextQuery) []TaskContext

// SetContext makes f what the engine asks for an attempt's context before
// its task files are written; until it is set, nothing is asked and the
// task files hold no context, as before context existed.
func (e *Engine) SetContext(f ContextFunc) {
	e.mu.Lock()
	e.contextOf = f
	e.mu.Unlock()
}

// askContext asks the engine's context function for a's context, the
// query read under e.mu and the function called outside it, so a provider
// that takes its time holds no other run, task or request of the engine's;
// nil when none is set. The blocks are a view a provider derived as the
// attempt starts: they go into its task files and nowhere else, never the
// ledger, and an attempt neither fails nor changes state for them.
func (e *Engine) askContext(ctx context.Context, r *run, a *attempt) []TaskContext {
	e.mu.Lock()
	f := e.contextOf
	var q ext.ContextQuery
	if f != nil {
		q = contextQuery(r, a)
	}
	e.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(ctx, q)
}

// contextQuery is a's attempt as a context provider is asked about it: its
// run, task and number, the crew's goal and the task's instructions, where
// it works and the commit it starts at, and the results it starts from.
// The caller holds e.mu.
func contextQuery(r *run, a *attempt) ext.ContextQuery {
	t := a.t
	q := ext.ContextQuery{Run: r.id, Crew: r.crewID, Task: t.def.ID, Attempt: a.n, Kind: t.def.Kind, Goal: r.goal, Instructions: t.def.Instructions,
		Worktree: a.worktree, Dir: filepath.Join(a.worktree, r.prefix), Start: a.start}
	for _, in := range a.inputs {
		q.Inputs = append(q.Inputs, ext.ContextInput{Task: in.task, Commit: in.commit})
	}
	return q
}
