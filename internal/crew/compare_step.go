package crew

// compareState is what a compare run keeps of its lanes' settling.
type compareState struct {
	// settled is the lanes' shown candidates once every lane has ended,
	// nil while a lane has not.
	settled []SettledCandidate
}

// compareStep settles a compare run's lanes inside each change of it. The
// caller holds e.mu, inside a change, after its mutate and before its
// ended work.
func (e *Engine) compareStep(r *run) {
	// The selection kept current first: a lane changed since supersedes it
	// (compare_select.go).
	e.selectStep(r)
	// Then the selection opened once every lane has ended, and the run
	// settled (compare_select.go).
	e.selectEnd(r)
}
