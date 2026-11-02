package crew

import (
	"context"
	"errors"
	"regexp"
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
type LaunchOpts struct {
	Class   string
	Request string
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
	if err := opts.check(); err != nil {
		return nil, noop, err
	}
	if opts.Class == ClassBrakeman && c.Managed() {
		return nil, noop, invalidf("a Brakeman turn is never a managed run")
	}
	// A turn's crew is Brakeman's own id (the store reserves it), so its
	// runs and their records are named brakeman-<n>: the records' prune
	// counts them by that name before it reads any.
	if opts.Class == ClassBrakeman && c.ID != ClassBrakeman {
		return nil, noop, invalidf("a Brakeman turn's crew is %s", ClassBrakeman)
	}
	return e.launchNamed(ctx, c, label, opts)
}
