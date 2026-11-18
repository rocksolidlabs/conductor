package crew

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A launch its caller holds to what it resolved (internal/api's resolved
// launch): the engine works out what it would launch of a crew before the
// run is made (PrepareLaunch), the caller compares it, and the engine
// launches exactly that (LaunchPrepared), nothing read again between the
// compare and the run.

// ErrStartCommitMissing says the launch folder's repository holds no commit of
// the id a launch named as its base.
var ErrStartCommitMissing = errors.New("the folder's repository does not hold the commit")

// ErrStartCommitNotACommit says the id a launch named as its base is an
// object of the folder's repository that is not a commit (an annotated
// tag's): a start commit is a commit's own full id.
var ErrStartCommitNotACommit = errors.New("the start commit is not a commit's own id")

// The sources of a prepared launch's project manifest (Prepared.Manifest).
const (
	ManifestNone = "none"
	ManifestCrew = sourceCrew
	ManifestFile = sourceFile
)

// Prepared is a launch of a crew as the engine worked it out before its
// run is made: where the crew's folder lies in its repository and, for a
// crew with tasks, the commit the run starts from and its project manifest
// there.
type Prepared struct {
	crewID string
	cwd    string
	p      prepared
}

// Base is the commit a crew with tasks' run starts from; "" for a crew
// without.
func (p *Prepared) Base() string { return p.p.base }

// Manifest is the project manifest the run runs with: ManifestNone,
// ManifestCrew with the crew's manifest revision, or ManifestFile with the
// file's blob at the base.
func (p *Prepared) Manifest() (source string, revision int64, blob string) {
	switch {
	case p.p.man.source == sourceFile:
		return ManifestFile, 0, p.p.man.digest
	case p.p.man.source == sourceCrew && !p.p.man.m.empty():
		return ManifestCrew, p.p.man.revision, ""
	}
	return ManifestNone, 0, ""
}

// PrepareLaunch works out what a launch of c, whose folder the caller has
// resolved, would run, as LaunchNamed does before its run is made (the
// same checks, ErrInvalid, ErrNotRepo, ErrNoGit, ErrNoLedger among them):
// for a crew with tasks, its base, base when given (a full commit id; one
// the folder's repository does not hold is ErrStartCommitMissing), else the
// folder's HEAD, read once, and its project manifest there. It makes
// nothing and keeps nothing.
func (e *Engine) PrepareLaunch(ctx context.Context, c Crew, base string) (*Prepared, error) {
	if base != "" {
		if !c.Managed() {
			return nil, invalidf("a start commit is a crew with tasks' base")
		}
		if !fullCommit(base) {
			return nil, invalidf("the start commit %s is not a full commit id", quote(base))
		}
		if c.Isolation == IsolationWorktree && c.Cwd != "" {
			// The commit itself: an annotated tag's id (or any object's
			// that is not a commit) peels to a commit, but no worktree is
			// ever at it.
			out, err := git(ctx, c.Cwd, "rev-parse", "--verify", "--quiet", base+"^{commit}")
			switch {
			case err != nil && ctx.Err() != nil:
				return nil, ctx.Err()
			case err != nil:
				return nil, fmt.Errorf("%w: %s", ErrStartCommitMissing, base)
			case strings.TrimSpace(out) != base:
				return nil, fmt.Errorf("%w: %s names a tag or another object, not a commit", ErrStartCommitNotACommit, base)
			}
		}
	}
	p, err := e.prepare(ctx, c, base, nil)
	if err != nil {
		return nil, err
	}
	return &Prepared{crewID: c.ID, cwd: c.Cwd, p: p}, nil
}

// LaunchPrepared launches c as LaunchNamedWith does, from what
// PrepareLaunch worked out of it: its base and its manifest as prepared,
// nothing of them read again. c is the crew p was prepared of (its id and
// its folder the same; its members may be bound since); another is
// refused (ErrInvalid).
func (e *Engine) LaunchPrepared(ctx context.Context, c Crew, p *Prepared, label string, opts LaunchOpts) (*Run, func(), error) {
	noop := func() {}
	if p == nil || p.crewID != c.ID || p.cwd != c.Cwd {
		return nil, noop, invalidf("the launch was prepared of another crew")
	}
	if err := e.checkLaunch(c, opts); err != nil {
		return nil, noop, err
	}
	if err := ValidateLabel(label); err != nil {
		return nil, noop, err
	}
	return e.launchPrepared(ctx, c, p.p, label, opts)
}
