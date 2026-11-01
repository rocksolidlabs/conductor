package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Host and Start: a provider that needs the server (an ext.Starter) is
// started once the server is made and before it serves (StartExtensions),
// with an ext.Host, its one door into the app: launch a saved crew as a
// run through the engine, type into a server session or answer its
// question through the session's own Submit, hand a notice to the sinks,
// a directory of its own, a logger. Until a provider's Start has
// succeeded, every slot it holds answers its free default (the guard's
// ready check) and its queues hold what reaches them; one that errs,
// panics or is late is tried again with backoff for as long as the
// server runs, never while an earlier try of it is still running.

// The backoff between two tries of a provider's Start: from extStartFirst,
// doubling to extStartMost.
const (
	extStartFirst = time.Second
	extStartMost  = 5 * time.Minute
)

// The states of a provider the server starts, as extState answers them
// (and GET /api/capabilities will list them, C9).
const (
	extStarting = "starting" // its Start has not yet succeeded: its slots answer the free default
	extStarted  = "started"  // its Start succeeded, or it needs none
)

// extStarter is a provider the server starts: one per registered name
// whose value is an ext.Starter (one name is one value, the registry's
// rule), whatever slots it holds.
type extStarter struct {
	name  string
	value ext.Starter
	// started is set once a try of Start has succeeded; every guarded slot
	// of the provider reads it (ready) and answers the free default until
	// it is.
	started atomic.Bool
	// mayNotify is false for a provider one of whose sinks takes provider
	// notices (ext.TypeProvider): its Host's Notify refuses, so no notice
	// is handed back round through providers. Decided once, at start.
	mayNotify bool
	// cancel ends the context of the try that succeeded, with the server's.
	cancel context.CancelFunc
}

// extStarters answers, for p.All, a starter per distinct name whose value
// is an ext.Starter, in the registry's order.
func extStarters(p ext.Providers) []*extStarter {
	var out []*extStarter
	seen := map[string]bool{}
	for _, e := range p.All {
		st, ok := e.Value.(ext.Starter)
		if !ok || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out = append(out, &extStarter{name: e.Name, value: st, mayNotify: true})
	}
	return out
}

// readyOf is the ready flag a guarded slot of the provider name holding v
// reads: the starter's when v is one the server starts, nil (always
// ready) for a free default or a provider that needs no Start.
func (s *Server) readyOf(name string, v any) *atomic.Bool {
	if _, ok := v.(ext.Starter); !ok {
		return nil
	}
	for _, st := range s.extStarts {
		if st.name == name {
			return &st.started
		}
	}
	return nil
}

// live reports whether a slot whose ready flag is r answers through its
// provider: always for nil, once the provider's Start has succeeded
// otherwise.
func live(r *atomic.Bool) bool { return r == nil || r.Load() }

// extState answers the state of the provider name: extStarting until its
// Start has succeeded, extStarted after, and for one that needs no Start.
func (s *Server) extState(name string) string {
	for _, st := range s.extStarts {
		if st.name == name && !st.started.Load() {
			return extStarting
		}
	}
	return extStarted
}

// StartExtensions starts every provider that is an ext.Starter, once per
// distinct value (the entitlement source's slot among them), all at once,
// each on a goroutine of the server's own (track), and returns at once: it
// asks no provider anything itself, so nothing a provider does holds up
// the listeners. runServe calls it once the server is made and before the
// listeners serve, with the maintenance context: ctx's end, or the
// server's extensions stopping (Shutdown), ends every try and every retry.
// A second call does nothing.
func (s *Server) StartExtensions(ctx context.Context) {
	if !s.extStarting.CompareAndSwap(false, true) {
		return
	}
	if len(s.extStarts) == 0 {
		return
	}
	// Derived from the server's own context, so stopExtensions (Shutdown)
	// ends every try at once, before it waits for the supervisors; ctx's
	// end reaches them too.
	base, cancel := context.WithCancel(s.extCtx)
	context.AfterFunc(ctx, cancel)
	for _, st := range s.extStarts {
		s.track(func() { s.superviseStart(base, st) })
	}
}

// superviseStart tries st's Start until one try succeeds or ctx ends,
// waiting a backoff between two tries (extStartFirst doubling to
// extStartMost) that begins once the last try has returned. On success
// the provider's slots answer through it from their next call and its
// queues begin to drain. Before the first try it decides whether the
// provider may send notices (takesNotices), on its own goroutine, so a
// sink whose Events is slow holds up only its own provider's Start.
func (s *Server) superviseStart(ctx context.Context, st *extStarter) {
	st.mayNotify = !s.takesNotices(ctx, st.name)
	delay := extStartFirst
	for ctx.Err() == nil {
		if s.tryStart(ctx, st) {
			st.started.Store(true)
			s.beginQueues(st.name)
			s.log.Info("extension started", "provider", st.name)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-s.extAfterOr(delay):
		}
		delay = min(2*delay, extStartMost)
	}
}

// takesNotices reports whether one of the provider name's sinks takes
// provider notices (ext.TypeProvider): such a provider's Host refuses to
// send one, so no notice is handed back round through providers. Each
// sink's Events is read once (guardSink keeps it), within ext.StartWait,
// and ends with ctx.
func (s *Server) takesNotices(ctx context.Context, name string) bool {
	for _, sink := range s.exts.sinks {
		if g, ok := sink.(*guardSink); ok && g.name == name && slices.Contains(g.eventsWithin(ctx), ext.TypeProvider) {
			return true
		}
	}
	return false
}

// tryStart runs one try of st's Start with a context and a Host of its
// own, and reports whether it succeeded: returned nil within
// ext.StartWait. A try that errs or panics is logged (once a minute, as
// the guard logs) and its context cancelled, which revokes its Host. One
// that has not returned by ext.StartWait is given up the same way, and
// tryStart then waits for it to return (or for ctx to end), so a
// provider never has two Starts running, logging once a minute that it
// has not; its answer, once it comes, is ignored.
func (s *Server) tryStart(ctx context.Context, st *extStarter) bool {
	tctx, cancel := context.WithCancel(ctx)
	h := &extHost{s: s, name: st.name, ctx: tctx, mayNotify: st.mayNotify}
	// Asked for before the try begins, so a test's clock sees the wait of
	// each try ahead of what the try does.
	wait := s.extAfterOr(ext.StartWait)
	type result struct {
		why, text string // "" for a success
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{why: "panic", text: "panic: " + extText(p)}
			}
			done <- r
		}()
		if err := st.value.Start(tctx, h); err != nil {
			// The error's text is the provider's code: read here.
			r = result{why: "error", text: extText(err)}
		}
	}()
	select {
	case r := <-done:
		if r.why == "" && ctx.Err() == nil {
			st.cancel = cancel
			return true
		}
		cancel()
		if ctx.Err() == nil {
			s.extFailed(extKindStart, st.name, r.why, r.text)
		}
		return false
	case <-wait:
		cancel()
		s.extFailed(extKindStart, st.name, "late", fmt.Sprintf("Start did not return within %s", ext.StartWait))
		if f := extAbandoned.Load(); f != nil {
			(*f)(st.name)
		}
		for {
			select {
			case <-done:
				return false
			case <-ctx.Done():
				return false
			case <-s.extAfterOr(extLogEvery):
				s.extFailed(extKindStart, st.name, "late", "Start has not returned since it was given up")
			}
		}
	case <-ctx.Done():
		cancel()
		return false
	}
}

// extKindStart is the kind the guard's log gives a failed Start.
const extKindStart = "start"

// extAbandoned, when a test sets it, is told the name of a provider whose
// try of Start was given up, as tryStart begins to wait for it to return;
// nil outside tests.
var extAbandoned atomic.Pointer[func(name string)]

// extAfterOr is time.After, or the server's clock seam when a test set
// one (extAfter): the waits of the providers' starts and their backoffs.
func (s *Server) extAfterOr(d time.Duration) <-chan time.Time {
	if s.extAfter != nil {
		return s.extAfter(d)
	}
	return time.After(d)
}

// extHost is the ext.Host of one try of a provider's Start: it acts for
// the provider name while ctx, the try's, lasts, and answers every call
// context.Canceled once it has ended (the try given up, or the server
// shutting down), so goroutines a given-up try began cannot act.
type extHost struct {
	s         *Server
	name      string
	ctx       context.Context
	mayNotify bool
}

var _ ext.Host = (*extHost)(nil)

// gone is context.Canceled once the host's try has ended.
func (h *extHost) gone() error {
	if h.ctx.Err() != nil {
		return context.Canceled
	}
	return nil
}

// bound is ctx ending also when the host's try does, so a launch already
// under way when its try is given up (waiting on an agent's lookup) stops
// waiting then. It is passed on by a goroutine of its own, so it only
// shortens the wait: whether the call acts is asked of the try itself (h.gone
// as the engine is asked; Submission.Withdrawn as the writer takes a line),
// never of this context.
func (h *extHost) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.ctx, cancel)
	return cctx, func() { stop(); cancel() }
}

// refused is ext.ErrRefused with the words why.
func refused(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ext.ErrRefused}, args...)...)
}

// Launch launches the saved crew r.Crew as a run, as POST
// /api/crews/{id}/launch does (launchSaved), notes in the run's log who
// launched it through which provider, and answers the run's id, its crew
// and its page's URL on the public base. A switchyard answers
// ext.ErrUnsupported; a crew the server does not hold, ext.ErrNotFound;
// a request past its bounds or a launch the server refuses, ext.ErrRefused
// with the words why. A launch whose try is given up before the engine is
// asked answers context.Canceled and launches nothing.
func (h *extHost) Launch(ctx context.Context, r ext.LaunchRequest) (ext.RunRef, error) {
	if err := h.gone(); err != nil {
		return ext.RunRef{}, err
	}
	if h.s.cfg.Switchyard.Enabled {
		return ext.RunRef{}, fmt.Errorf("%w: this server coordinates hosted sessions and launches none", ext.ErrUnsupported)
	}
	r.Label = strings.TrimSpace(r.Label)
	if err := r.Valid(); err != nil {
		return ext.RunRef{}, refused("%v", err)
	}
	if !crew.ValidID(r.Crew) {
		return ext.RunRef{}, refused("crew %q is not a crew id", r.Crew)
	}
	bctx, end := h.bound(ctx)
	defer end()
	// The try's end is checked once more as the engine is asked (admit):
	// a lookup the try's end cut short counts as an answer, and the launch
	// itself runs on to its end once admitted.
	run, c, release, err := h.s.launchSaved(bctx, r.Crew, r.Label, h.gone)
	defer release()
	if err != nil {
		if h.gone() != nil {
			return ext.RunRef{}, context.Canceled
		}
		return ext.RunRef{}, h.launchError(r.Crew, err)
	}
	// Noted while the engine holds the run (release, deferred), as the
	// route notes its view link.
	h.s.runs.Note(run.ID, session.ActivityStatus, "launched by "+r.By.Name+" through "+h.name)
	h.s.log.Info("crew launched", "crew", c.ID, "run", run.ID, "provider", h.name)
	return ext.RunRef{ID: run.ID, Crew: c.ID, URL: h.s.publicBase(nil) + "/runs/" + url.PathEscape(run.ID)}, nil
}

// launchError is a launch's error as the provider is told it.
func (h *extHost) launchError(id string, err error) error {
	var refusal launchRefused
	var stored crewStoreErr
	switch {
	case errors.As(err, &refusal) && refusal.e.Code == "store_unavailable":
		return fmt.Errorf("%w: %s", ext.ErrUnsupported, refusal.e.Message)
	case errors.As(err, &refusal):
		return refused("%s", refusal.e.Message)
	case errors.As(err, &stored) && errors.Is(stored.err, crew.ErrNotFound):
		return fmt.Errorf("%w: no such crew", ext.ErrNotFound)
	case errors.As(err, &stored) && (errors.Is(stored.err, crew.ErrUnreadable) || errors.Is(stored.err, crew.ErrInvalid)):
		return refused("%v", stored.err)
	case errors.As(err, &stored):
		h.s.log.Error("crew read failed", "crew", id, "provider", h.name, "err", stored.err)
		return refused("could not read the crews")
	}
	h.s.log.Warn("crew launch failed", "crew", id, "provider", h.name, "err", err)
	return refused("%v", err)
}

// Submit types r.Text into the server session r.Session, never into a
// question it shows (Local.Submit with UnlessWaiting), or, with
// r.Question, answers that question with the line, through the
// question's claim as a viewer's reply does: the first answer wins, and
// one to a question that no longer waits answers ext.ErrQuestionAnswered.
// Either is recorded as by r.By's name. A choice's keys (r.Input) and a
// hosted session answer ext.ErrUnsupported until the decision router's
// seam (C5) adds them, and a line blank once cleaned is refused; a line
// accepted while its program reads slowly answers "pending". The try's end withdraws the submission as a revoked
// link does (session.Submission.Withdrawn, looked at as the writer takes
// the text): one whose text is not yet typed when its try is given up
// answers context.Canceled and types nothing; one whose text is typed and
// whose Enter is not answers context.Canceled too, its Enter left out and
// the session's turn held for a person (a reply's claim let go as the
// hold shows), so that no later line joins the text.
func (h *extHost) Submit(ctx context.Context, r ext.SubmitRequest) (ext.SubmitResult, error) {
	if err := h.gone(); err != nil {
		return ext.SubmitResult{}, err
	}
	if err := r.Valid(); err != nil {
		return ext.SubmitResult{}, refused("%v", err)
	}
	d, ok := h.s.registry.Get(r.Session)
	if !ok {
		return ext.SubmitResult{}, fmt.Errorf("%w: no such session", ext.ErrNotFound)
	}
	local, ok := d.(*session.Local)
	if !ok {
		return ext.SubmitResult{}, fmt.Errorf("%w: a hosted session takes no submission from a provider yet", ext.ErrUnsupported)
	}
	if r.Input != "" {
		return ext.SubmitResult{}, fmt.Errorf("%w: a choice's keys are not answered through the host yet", ext.ErrUnsupported)
	}
	// A text blank once cleaned (spaces, line breaks) would be typed as a
	// bare Enter, which into a question picks what the question
	// highlights, as only a choice's keys may: refused, nothing written.
	if session.SubmitLine(r.Text) == "" {
		return ext.SubmitResult{}, refused("the line is blank")
	}
	sctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	// The try's end is the submission's withdrawal, not its context's: a
	// closed channel the writer sees at once, and that leaves a typed text
	// held rather than its turn given to the next line.
	sub := session.Submission{Text: r.Text, ByName: r.By.Name, Question: r.Question, Withdrawn: h.ctx.Done()}
	// A line that answers no question is never typed into one.
	sub.UnlessWaiting = r.Question == ""
	res, err := local.Submit(sctx, sub)
	if err != nil && !errors.Is(err, session.ErrSubmitPending) && h.gone() != nil {
		return ext.SubmitResult{}, context.Canceled
	}
	switch {
	case errors.Is(err, session.ErrSubmitPending):
		return ext.SubmitResult{Status: "pending"}, nil
	case errors.Is(err, session.ErrQuestionAnswered):
		return ext.SubmitResult{}, fmt.Errorf("%w: %v", ext.ErrQuestionAnswered, err)
	case errors.Is(err, session.ErrSessionEnded):
		return ext.SubmitResult{}, refused("the session has ended")
	case err != nil:
		return ext.SubmitResult{}, refused("%v", err)
	case !res.Typed:
		return ext.SubmitResult{}, refused("the session asks a question")
	case !res.Entered:
		return ext.SubmitResult{}, refused("%s", session.LineStrandedWords)
	}
	return ext.SubmitResult{Status: "entered"}, nil
}

// Notify checks n as a provider's notice (at most ext.MaxNoticeMessage
// bytes of message, ext.MaxNoticeURL of URL, one line each), types it
// ext.TypeProvider from this provider at the time it is handed over, and
// hands it to the sinks that take provider notices. A provider one of
// whose sinks takes them answers ext.ErrUnsupported: it may take notices
// or send them, never both.
func (h *extHost) Notify(_ context.Context, n ext.Notification) error {
	if err := h.gone(); err != nil {
		return err
	}
	if !h.mayNotify {
		return fmt.Errorf("%w: a provider that takes provider notices cannot send them", ext.ErrUnsupported)
	}
	if err := n.Valid(); err != nil {
		return refused("%v", err)
	}
	n.Type, n.Origin, n.At = ext.TypeProvider, h.name, time.Now().UTC()
	h.s.deliverNotice(n)
	return nil
}

// DataDir is <dataDir>/ext/<name>, made 0700 through the store; a server
// with no data directory answers ext.ErrUnsupported.
func (h *extHost) DataDir() (string, error) {
	if err := h.gone(); err != nil {
		return "", err
	}
	if h.s.store == nil {
		return "", fmt.Errorf("%w: no data directory is configured", ext.ErrUnsupported)
	}
	root, err := h.s.store.Sub("ext")
	if err != nil {
		return "", err
	}
	own, err := root.Sub(h.name)
	if err != nil {
		return "", err
	}
	return own.Dir(), nil
}

// Log is the server's logger, naming the provider.
func (h *extHost) Log() *slog.Logger { return h.s.log.With("provider", h.name) }
