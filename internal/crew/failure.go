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
// failure classed so is recorded with the person's choice beside it. A
// hint is never a guess: a receipt that holds nothing that says why the
// check failed is unavailable, not code.

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
// continuations', Ledger.ChecksOn). It is the plan's table, read in its
// order, the first row that takes the receipt deciding:
//
//   - cancelled, from the attempt's state whatever the receipt says: the
//     attempt was stopped, cancelled or withdrawn (a check a stop
//     interrupted is a cancellation);
//   - infrastructure: the check never ran (exit -1 with an error: the
//     runner or the Runtime could not start it); it was interrupted with
//     its attempt (the server ended under it); the ledger refused it, its
//     running row (the engine ended its process: the receipt finished with
//     the ledger's words) or its outcome (the row left started or running,
//     or the attempt failed at it with the ledger's words); it exited 0 and
//     its checkout could not be compared after it; or it is a step of a
//     manifest phase that makes the tree (setup, service, ready), not a
//     check or a verify step;
//   - flaky: it failed (a fact of the code row below) and another of same,
//     of the same argv (and, for a verify step, the same manifest),
//     passed;
//   - unavailable: the receipt is not in the ledger (r is the zero row);
//     its log was dropped and no output is kept; or it holds no failing
//     fact (it did not exit non-zero or by a signal, did not time out and
//     left the checkout as it was): nothing in it says why it failed;
//   - code: it exited non-zero, by a signal, timed out (the words name the
//     check's own timeout, from the receipt's words, never the time it
//     waited for a check slot) or left the checkout changed. A process it
//     left that held its output (killed) is said, and decides no class.
func ClassifyFailure(a ledger.AttemptRow, r ledger.CheckRow, same []ledger.CheckRow) Failure {
	// cancelled
	switch {
	case a.State == ledger.AttemptStopped:
		return Failure{FailureCancelled, "the run was stopped under the attempt"}
	case a.State == ledger.AttemptCancelled && a.Stage == "withdrawn":
		return Failure{FailureCancelled, "the attempt was withdrawn: an input it rested on changed"}
	case a.State == ledger.AttemptCancelled:
		return Failure{FailureCancelled, "the attempt was cancelled"}
	}
	held := r.State != ""
	finished := r.State == ledger.CheckFinished
	stage := r.Phase + " " + r.ID
	// infrastructure
	switch {
	case !held:
	case r.State == ledger.CheckInterrupted && a.State == ledger.AttemptInterrupted:
		return Failure{FailureInfrastructure, "the server ended while it ran"}
	case r.State == ledger.CheckStarted || r.State == ledger.CheckRunning:
		return Failure{FailureInfrastructure, "the ledger never recorded its outcome" + because(attemptWords(a, stage))}
	case finished && strings.HasPrefix(r.Error, unrecordedWords):
		return Failure{FailureInfrastructure, "the engine ended it: " + r.Error}
	case finished && a.Stage == stage && strings.HasPrefix(a.Error, unrecordedWords):
		return Failure{FailureInfrastructure, "the ledger never recorded its outcome: " + a.Error}
	case finished && r.Exit == -1 && r.Error != "" && !r.TimedOut && r.Signal == "":
		return Failure{FailureInfrastructure, "it never ran: " + r.Error}
	case r.Phase == ledger.PhaseSetup || r.Phase == ledger.PhaseService || r.Phase == ledger.PhaseReady:
		return Failure{FailureInfrastructure, fmt.Sprintf("the %s step %s failed as the checkout was made ready, before any check", r.Phase, r.ID) + because(r.Error)}
	case finished && r.Exit == 0 && r.Signal == "" && !r.TimedOut && strings.Contains(r.Error, uncomparedWords):
		return Failure{FailureInfrastructure, "it exited 0, and the server could not compare its checkout after it" + because(r.Error)}
	}
	failing := finished && (r.Exit != 0 || r.Signal != "" || r.TimedOut || !r.Stable)
	// flaky
	for _, o := range same {
		if !failing {
			break
		}
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
	// unavailable
	switch {
	case !held:
		return Failure{FailureUnavailable, "the ledger has no receipt of it"}
	case r.LogDropped && r.Tail == "":
		return Failure{FailureUnavailable, "its log was dropped and none of its output is kept"}
	case !failing:
		return Failure{FailureUnavailable, "the receipt holds nothing that says why the check failed" + because(r.Error)}
	}
	// code
	var words string
	switch {
	case r.TimedOut:
		words = timeoutWords(r.Error) + ": a hang in the code, or a machine too slow"
	case r.Signal != "":
		words = "ended by the signal " + r.Signal
	case r.Exit != 0:
		words = fmt.Sprintf("exited %d", r.Exit)
	default:
		words = "left the checkout changed" + because(r.Error)
	}
	if r.Killed {
		words += "; a process it left behind held its output and was ended with its group"
	}
	return Failure{FailureCode, words}
}

// timeoutWords says a timed-out check's own timeout as its receipt's words
// name it ("timed out after 10m0s", checkWords): the time the command was
// allowed, never the receipt's span, which counts its wait for a check
// slot and the comparison after it. Words that name none say no number.
func timeoutWords(why string) string {
	const lead = "timed out after "
	rest, ok := strings.CutPrefix(why, lead)
	if !ok {
		return "timed out"
	}
	if i := strings.IndexAny(rest, ": "); i >= 0 {
		rest = rest[:i]
	}
	d, err := time.ParseDuration(rest)
	if err != nil || d <= 0 {
		return "timed out"
	}
	return fmt.Sprintf("timed out after %d s", int64(d.Round(time.Second)/time.Second))
}

// attemptWords is the attempt's error when it failed at the receipt's
// stage, "" otherwise.
func attemptWords(a ledger.AttemptRow, stage string) string {
	if a.Stage != stage {
		return ""
	}
	return a.Error
}

// because is ": " and why, or "" for no words.
func because(why string) string {
	if why = strings.TrimSpace(why); why == "" {
		return ""
	}
	return ": " + why
}
