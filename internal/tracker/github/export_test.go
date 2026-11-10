package github

import (
	"context"
	"net/http"
	"time"

	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// SetClock gives the adapter the clock of every request's own deadline:
// after runs f once d has passed (as the test decides) and gives the stop
// of that timer.
func SetClock(a *Adapter, after func(d time.Duration, f func()) (stop func() bool)) {
	a.after = after
}

// BodilessWrite sends a write that has no body to the credential's API
// host, as a REST write (a DELETE) would.
func BodilessWrite(ctx context.Context, a *Adapter, c tracker.Credential) error {
	ep, err := a.endpointFor(c, c.Host())
	if err != nil {
		return err
	}
	r := request{method: http.MethodDelete, path: "/repos/octo-example/widgets/issues/42/lock", write: true, max: MaxErrorAnswer}
	if _, err = a.do(ctx, c, ep, r); err != nil {
		_, err = a.sortWrite(ep, r, 0, gqlAnswer{}, err, "")
	}
	return err
}
