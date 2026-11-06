package crew

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A failed receipt's class (docs/plans/review-repair.md, RV1): what the
// ledger keeps of a failing check or step says whether the code is at
// fault, the machine was, the check is flaky, the attempt was cancelled,
// or the evidence is gone. The class is a hint a person may change; a
// failure classed so is recorded with the person's choice beside it.

// The classes of a failure.
const (
	FailureCancelled      = "cancelled"
	FailureInfrastructure = "infrastructure"
	FailureFlaky          = "flaky"
	FailureUnavailable    = "unavailable"
	FailureCode           = "code"
)

// Failure is a failed receipt's class and the words that say why.
type Failure struct {
	Class string `json:"class"`
	Words string `json:"words"`
}

// ClassifyFailure classes the receipt r of attempt a, given same, other
// receipts of the same task's check on the same commit (the run's and its
// continuations', Ledger.ChecksOn): read in this order,
//
//   - cancelled: the attempt was stopped, cancelled or withdrawn, whatever
//     the receipt says (a check a stop interrupted is a cancellation);
//   - infrastructure: the check never ran (exit -1 with an error: the
//     runner or the Runtime could not start it), it was interrupted with
//     its attempt (the server ended under it), its outcome was never
//     recorded, or it is a step of a manifest phase that makes the tree
//     (setup, service, ready), not a check or a verify step;
//   - flaky: another of same, of the same argv (and, for a verify step,
//     the same manifest), passed;
//   - unavailable: the receipt is not in the ledger (r is the zero row),
//     or its log was dropped and no output is kept;
//   - code: it exited non-zero, by a signal, timed out or left the
//     checkout changed. A process it left that held its output (killed)
//     is said, and decides no class.
func ClassifyFailure(a ledger.AttemptRow, r ledger.CheckRow, same []ledger.CheckRow) Failure {
	switch {
	case a.State == ledger.AttemptStopped:
		return Failure{FailureCancelled, "the run was stopped under the attempt"}
	case a.State == ledger.AttemptCancelled && a.Stage == "withdrawn":
		return Failure{FailureCancelled, "the attempt was withdrawn: an input it rested on changed"}
	case a.State == ledger.AttemptCancelled:
		return Failure{FailureCancelled, "the attempt was cancelled"}
	case r.State == "":
		return Failure{FailureUnavailable, "the ledger has no receipt of it"}
	case r.State == ledger.CheckInterrupted && a.State == ledger.AttemptInterrupted:
		return Failure{FailureInfrastructure, "the server ended while it ran"}
	case r.State != ledger.CheckFinished:
		return Failure{FailureInfrastructure, "its outcome was never recorded"}
	case r.Exit == -1 && r.Error != "" && !r.TimedOut && r.Signal == "":
		return Failure{FailureInfrastructure, "it never ran: " + r.Error}
	case r.Phase == ledger.PhaseSetup || r.Phase == ledger.PhaseService || r.Phase == ledger.PhaseReady:
		return Failure{FailureInfrastructure, fmt.Sprintf("the %s step %s failed as the checkout was made ready, before any check", r.Phase, r.ID) + because(r.Error)}
	}
	for _, o := range same {
		if o.RunID == r.RunID && o.TaskID == r.TaskID && o.N == r.N && o.ID == r.ID && o.K == r.K {
			continue // the receipt itself
		}
		if o.TaskID != r.TaskID || o.Phase != r.Phase || o.ID != r.ID || o.Subject != r.Subject || !slices.Equal(o.Argv, r.Argv) || (r.Phase == ledger.PhaseVerify && o.Manifest != r.Manifest) {
			continue
		}
		if o.State == ledger.CheckFinished && !failedOutcome(o) {
			return Failure{FailureFlaky, fmt.Sprintf("the same check passed on the same commit as attempt %d of run %s", o.N, o.RunID)}
		}
	}
	if r.LogDropped && r.Tail == "" {
		return Failure{FailureUnavailable, "its log was dropped and none of its output is kept"}
	}
	var words string
	switch {
	case r.TimedOut:
		words = fmt.Sprintf("timed out after %s: a hang in the code, or a machine too slow", r.FinishedAt.Sub(r.StartedAt).Round(time.Second))
	case r.Signal != "":
		words = "ended by the signal " + r.Signal
	case r.Exit != 0:
		words = fmt.Sprintf("exited %d", r.Exit)
	case !r.Stable:
		words = "left the checkout changed"
	default:
		words = "failed" + because(r.Error)
	}
	if r.Killed {
		words += "; a process it left behind held its output and was ended with its group"
	}
	return Failure{FailureCode, words}
}

// because is ": " and why, or "" for no words.
func because(why string) string {
	if why = strings.TrimSpace(why); why == "" {
		return ""
	}
	return ": " + why
}
