package hostagent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
)

// HostIdentity is what conductor host hands its identity provider: the
// configured host token (--token), the host's state directory
// (config.HostStateDir), its logger, and, for tests, its clock and the
// seam told as a try's answer is about to be judged, with whether it is
// there yet.
type HostIdentity struct {
	Token    string
	StateDir string
	Log      *slog.Logger
	after    func(time.Duration) <-chan time.Time
	judging  func(answered func() bool)
}

// StartIdentity starts p, the identity provider registered as name, in
// host mode, as the server starts a provider: Start within ext.StartWait,
// again after a backoff (1 s doubling to 5 minutes) while it errs, panics
// or is late, never two tries at once, a given-up try's Host refusing
// every call (context.Canceled). Before anything else it makes
// <state>/ext 0700 and writes config.ProviderStateMark in it, so no export
// takes what the provider keeps there; when either fails the provider is
// never started. A failed try is logged by its kind and count alone,
// never its words, which may carry the token it was handed. started waits
// for the first try to return or be given up (or ctx) and answers whether
// a try has succeeded: true at once for a provider that is no ext.Starter,
// false at once for one that cannot be started. stop ends the tries and
// waits for the one under way at most ext.StartWait.
func StartIdentity(ctx context.Context, p ext.IdentityProvider, name string, h HostIdentity) (started func(context.Context) bool, stop func()) {
	st, ok := p.(ext.Starter)
	if !ok {
		return func(context.Context) bool { return true }, func() {}
	}
	if h.Log == nil {
		h.Log = slog.New(slog.DiscardHandler)
	}
	after := h.after
	if after == nil {
		after = time.After
	}
	extDir := filepath.Join(h.StateDir, "ext")
	if err := markProviderState(h.StateDir, extDir); err != nil {
		h.Log.Warn("the identity provider is not started: its state directory could not be made", "provider", name, "failure", "state")
		return func(context.Context) bool { return false }, func() {}
	}
	var (
		ok2   atomic.Bool
		tried = make(chan struct{})
		once  sync.Once
	)
	markTried := func() { once.Do(func() { close(tried) }) }
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer markTried()
		delay, tries := time.Second, 0
		for ctx.Err() == nil {
			tries++
			why := tryHostStart(ctx, st, &hostExtHost{name: name, token: h.Token, dir: filepath.Join(extDir, name), log: h.Log.With("provider", name)}, after, markTried, h.judging)
			if why == "" {
				ok2.Store(true)
				markTried()
				return
			}
			markTried()
			if ctx.Err() == nil {
				h.Log.Warn("the identity provider's start failed; trying again", "provider", name, "failure", why, "tries", tries)
			}
			select {
			case <-ctx.Done():
				return
			case <-after(delay):
			}
			delay = min(2*delay, 5*time.Minute)
		}
	}()
	started = func(c context.Context) bool {
		select {
		case <-tried:
		case <-c.Done():
		}
		return ok2.Load()
	}
	// The tries' goroutine waits for the try under way at most
	// ext.StartWait once ctx has ended (tryHostStart), and then returns.
	stop = func() {
		cancel()
		<-done
	}
	return started, stop
}

// markProviderState makes extDir 0700 under state and writes the mark in
// it, an empty file, 0600.
func markProviderState(state, extDir string) error {
	if !filepath.IsAbs(state) {
		return fmt.Errorf("the host's state directory %q is not absolute", state)
	}
	if err := os.MkdirAll(extDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(extDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(extDir, config.ProviderStateMark), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// tryHostStart runs one try of st's Start with h, its Host, and answers
// "" for a success or the failure's kind: error, panic or late. A late
// try is given up at ext.StartWait (its Host revoked, tried marked) and
// waited for before tryHostStart returns, so two tries never overlap.
func tryHostStart(ctx context.Context, st ext.Starter, h *hostExtHost, after func(time.Duration) <-chan time.Time, markTried func(), judging func(answered func() bool)) string {
	tctx, cancel := context.WithCancel(ctx)
	h.ctx = tctx
	wait := after(ext.StartWait)
	done := make(chan string, 1)
	go func() {
		why := "panic"
		defer func() {
			recover()
			done <- why
		}()
		err := st.Start(tctx, h)
		why = ""
		if err != nil {
			why = "error"
		}
	}()
	// stopped waits, once ctx has ended (stop), for the try under way, at
	// most ext.StartWait from then.
	stopped := func() {
		select {
		case <-done:
		case <-after(ext.StartWait):
		}
	}
	if judging != nil {
		judging(func() bool { return len(done) > 0 })
	}
	select {
	case why := <-done:
		if passed(wait) {
			// The answer and its deadline both there: late, as one after it.
			cancel()
			markTried()
			return "late"
		}
		if why == "" && ctx.Err() == nil {
			// The try that succeeded lasts as long as the host: its
			// context ends with ctx.
			context.AfterFunc(ctx, cancel)
			return ""
		}
		cancel()
		if why == "" {
			why = "stopped"
		}
		return why
	case <-wait:
		cancel()
		markTried()
		select {
		case <-done:
		case <-ctx.Done():
			stopped()
		}
		return "late"
	case <-ctx.Done():
		cancel()
		stopped()
		return "stopped"
	}
}

// hostExtHost is the ext.Host of one try of the identity provider's Start
// in host mode: a host launches nothing and keeps no ledger, so every call
// but its token, its mode, its directory and its logger answers
// ext.ErrUnsupported; every call answers context.Canceled once its try has
// ended.
type hostExtHost struct {
	ctx   context.Context
	name  string
	token string
	dir   string
	log   *slog.Logger
}

var _ ext.Host = (*hostExtHost)(nil)

func (h *hostExtHost) refuse() error {
	if h.ctx.Err() != nil {
		return context.Canceled
	}
	return fmt.Errorf("%w: conductor host launches nothing and keeps no ledger", ext.ErrUnsupported)
}

func (h *hostExtHost) Launch(context.Context, ext.LaunchRequest) (ext.RunRef, error) {
	return ext.RunRef{}, h.refuse()
}
func (h *hostExtHost) LaunchTurn(context.Context, ext.TurnLaunch) (ext.RunRef, error) {
	return ext.RunRef{}, h.refuse()
}
func (h *hostExtHost) LaunchApproved(context.Context, ext.ApprovedLaunch) (ext.RunRef, error) {
	return ext.RunRef{}, h.refuse()
}
func (h *hostExtHost) Submit(context.Context, ext.SubmitRequest) (ext.SubmitResult, error) {
	return ext.SubmitResult{}, h.refuse()
}
func (h *hostExtHost) Notify(context.Context, ext.Notification) error { return h.refuse() }
func (h *hostExtHost) Decision(context.Context, string) (ext.DecisionState, error) {
	return ext.DecisionState{}, h.refuse()
}

// DataDir is <state>/ext/<name>, made 0700.
func (h *hostExtHost) DataDir() (string, error) {
	if h.ctx.Err() != nil {
		return "", context.Canceled
	}
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return "", err
	}
	return h.dir, nil
}

func (h *hostExtHost) Log() *slog.Logger { return h.log }

// HostToken is conductor host's --token; "" once the try has ended.
func (h *hostExtHost) HostToken() string {
	if h.ctx.Err() != nil {
		return ""
	}
	return h.token
}

func (h *hostExtHost) Mode() string { return ext.ModeHost }
