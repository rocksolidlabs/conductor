package crew

import (
	"context"
	"errors"
	"regexp"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A run's class (docs/plans/brakeman.md, A turn is a run Yardmaster
// launches): the default class is a person's run; ClassBrakeman a
// Brakeman turn, an unsaved one-member crew the desk in internal/api
// launches for one request. The class and the request are stamped on the
// run before its member starts (LaunchNamedWith), kept in its record and
// handed to the Launcher in the LaunchSpec, so a turn's member is known
// from the spec, never from the crew's id or goal, which a person's crew
// could match. Each class is bounded on its own, in the engine (evict)
// and in the records (prune), so a day of turns never pushes a person's
// run out of either.
const ClassBrakeman = "brakeman"

// LaunchOpts are what LaunchNamedWith stamps on a run: its class ("" for
// the default) and, for a Brakeman turn, the request it does (br-<n>).
//
// Origin, for a provider's launch of a crew with tasks, is where the run
// comes from, written with the run's first row (run_origin): the engine
// sets its run and, for a limit, its deadline; nil for every other launch.
type LaunchOpts struct {
	Class   string
	Request string
	Origin  *ledger.OriginRow
	// FirstCommit is set by a launch the operator makes (Crews' Launch,
	// the command line's up, through the admin's route), with the crew's
	// folder as the launch opened it: a crew with worktrees in a new folder
	// or a repository just started gets its first commit there
	// (EnsureFirstCommit) and launches. Every other launch (an agent's, a
	// Brakeman turn's, a provider's, a continuation's) meets the refusal it
	// always met.
	FirstCommit *FirstCommitIn
}

// The caps of a Brakeman turn's runs: the engine keeps at most
// maxBrakemanRuns of them and the records at most maxBrakemanRecords,
// apart from the default class's maxRuns and maxRecords. Variables, so a
// test may lower them.
var (
	maxBrakemanRuns    = 10
	maxBrakemanRecords = 50
)

// ErrBrakemanRun refuses what would make a Brakeman turn's run a
// scheduler's: it is never resumed and takes no member (ask Brakeman
// again instead).
var ErrBrakemanRun = errors.New("the run is a Brakeman turn: it is never resumed and takes no member; ask Brakeman again")

// requestPattern is a Brakeman request's id (internal/brakeman's br-<n>).
var requestPattern = regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`)

// check holds the options to their shapes: a known class, and a request
// exactly when the class is Brakeman's.
func (o LaunchOpts) check() error {
	if o.FirstCommit != nil && (o.Class != "" || o.Origin != nil) {
		return invalidf("a first commit is made for the operator's own launch alone")
	}
	switch o.Class {
	case "":
		if o.Request != "" {
			return invalidf("a request belongs to a Brakeman turn's run")
		}
	case ClassBrakeman:
		if !requestPattern.MatchString(o.Request) {
			return invalidf("a Brakeman turn names its request (br-<n>)")
		}
	default:
		return invalidf("no run class %s", quote(o.Class))
	}
	if o.Origin != nil && o.Class != "" {
		return invalidf("a provider's launch is a person's class of run")
	}
	return nil
}

// runsCap is how many runs of class the engine keeps.
func runsCap(class string) int {
	if class == ClassBrakeman {
		return maxBrakemanRuns
	}
	return maxRuns
}

// recordsCap is how many records of class the records keep.
func recordsCap(class string) int {
	if class == ClassBrakeman {
		return maxBrakemanRecords
	}
	return maxRecords
}

// LaunchNamedWith is LaunchNamed with the run's class and request
// stamped on it (LaunchOpts) before its member starts, and handed to the
// Launcher in each member's LaunchSpec. LaunchNamed is it with none.
func (e *Engine) LaunchNamedWith(ctx context.Context, c Crew, label string, opts LaunchOpts) (*Run, func(), error) {
	noop := func() {}
	if err := e.checkLaunch(c, opts); err != nil {
		return nil, noop, err
	}
	return e.launchNamed(ctx, c, label, opts)
}

// checkLaunch holds opts to c: their shapes, a Brakeman turn of
// Brakeman's own crew and never a managed one, and a provider's launch of
// a crew with tasks.
func (e *Engine) checkLaunch(c Crew, opts LaunchOpts) error {
	if err := opts.check(); err != nil {
		return err
	}
	if opts.Class == ClassBrakeman && c.Managed() {
		return invalidf("a Brakeman turn is never a managed run")
	}
	if opts.Origin != nil && !c.Managed() {
		return invalidf("a provider's launch is of a crew with tasks")
	}
	// A turn's crew is Brakeman's own id (the store reserves it), so its
	// runs and their records are named brakeman-<n>: the records' prune
	// counts them by that name before it reads any.
	if opts.Class == ClassBrakeman && c.ID != ClassBrakeman {
		return invalidf("a Brakeman turn's crew is %s", ClassBrakeman)
	}
	return nil
}
