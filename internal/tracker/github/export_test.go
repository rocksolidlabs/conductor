package github

import "time"

// SetClock gives the adapter the clock of every request's own deadline:
// after runs f once d has passed (as the test decides) and gives the stop
// of that timer.
func SetClock(a *Adapter, after func(d time.Duration, f func()) (stop func() bool)) {
	a.after = after
}
