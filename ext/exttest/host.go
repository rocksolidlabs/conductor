package exttest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/rocksolidlabs/conductor/ext"
)

// FakeHost is an ext.Host for a provider under test: it records every
// Launch, Submit and Notify and answers what a test scripts (by default a
// run of the crew asked, "entered", and nil), refusing a request its
// record's Valid refuses as the server does (ext.ErrRefused with the
// words); its DataDir is a t.TempDir() and its Log writes into the test's
// log. While blocked, every call waits until it is let go, its context
// ends or the host is closed. Once closed, every call answers
// context.Canceled, as the Host of a try the server gave up does. Safe for
// concurrent use.
type FakeHost struct {
	changes
	dir  string
	log  *slog.Logger
	logf func(format string, args ...any)

	mu       sync.Mutex
	closed   bool
	done     chan struct{} // closed by Close
	gate     chan struct{} // non-nil while blocked
	waiting  int
	runs     int
	launches []ext.LaunchRequest
	submits  []ext.SubmitRequest
	notices  []ext.Notification
	approved []ext.ApprovedLaunch
	launch   func(ext.LaunchRequest) (ext.RunRef, error)
	submit   func(ext.SubmitRequest) (ext.SubmitResult, error)
	notify   func(ext.Notification) error
	approve  func(ext.ApprovedLaunch) (ext.RunRef, error)
	decision func(string) (ext.DecisionState, error)
	token    string
	mode     string
}

// SetHostToken has HostToken answer t ("" by default).
func (h *FakeHost) SetHostToken(t string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.token = t
}

// SetMode has Mode answer m (ext.ModeServer by default).
func (h *FakeHost) SetMode(m string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mode = m
}

// HostToken answers what SetHostToken set.
func (h *FakeHost) HostToken() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token
}

// Mode answers what SetMode set, ext.ModeServer by default.
func (h *FakeHost) Mode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mode == "" {
		return ext.ModeServer
	}
	return h.mode
}

// NewFakeHost is an open FakeHost whose DataDir is t.TempDir(). When t
// can log (Logf, as *testing.T), Log writes there until the host is
// closed; when t takes cleanups (Cleanup), the host is closed in one, so
// a provider's goroutine that outlives the test logs nowhere.
func NewFakeHost(t T) *FakeHost {
	h := &FakeHost{dir: t.TempDir(), done: make(chan struct{})}
	if l, ok := t.(interface{ Logf(string, ...any) }); ok {
		h.logf = l.Logf
	}
	h.log = slog.New(slog.NewTextHandler(hostLog{h}, nil))
	if c, ok := t.(interface{ Cleanup(func()) }); ok {
		c.Cleanup(h.Close)
	}
	return h
}

// hostLog writes a FakeHost's log lines into the test's log while the host
// is open.
type hostLog struct{ h *FakeHost }

func (w hostLog) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	if !w.h.closed && w.h.logf != nil {
		w.h.logf("%s", strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// OnLaunch has Launch answer fn's answer for a valid request.
func (h *FakeHost) OnLaunch(fn func(ext.LaunchRequest) (ext.RunRef, error)) {
	h.mu.Lock()
	h.launch = fn
	h.mu.Unlock()
}

// OnLaunchApproved has LaunchApproved answer fn's answer for a valid
// request.
func (h *FakeHost) OnLaunchApproved(fn func(ext.ApprovedLaunch) (ext.RunRef, error)) {
	h.mu.Lock()
	h.approve = fn
	h.mu.Unlock()
}

// OnSubmit has Submit answer fn's answer for a valid request.
func (h *FakeHost) OnSubmit(fn func(ext.SubmitRequest) (ext.SubmitResult, error)) {
	h.mu.Lock()
	h.submit = fn
	h.mu.Unlock()
}

// OnDecision has Decision answer fn's answer for a decision's id.
func (h *FakeHost) OnDecision(fn func(request string) (ext.DecisionState, error)) {
	h.mu.Lock()
	h.decision = fn
	h.mu.Unlock()
}

// OnNotify has Notify answer fn's answer for a valid notice.
func (h *FakeHost) OnNotify(fn func(ext.Notification) error) {
	h.mu.Lock()
	h.notify = fn
	h.mu.Unlock()
}

// Block has every call from now on wait until Unblock, its context's end
// (its error) or Close (context.Canceled), as a server that is slow to
// answer.
func (h *FakeHost) Block() {
	h.mu.Lock()
	if h.gate == nil {
		h.gate = make(chan struct{})
	}
	h.mu.Unlock()
}

// Unblock lets every waiting call go.
func (h *FakeHost) Unblock() {
	h.mu.Lock()
	if h.gate != nil {
		close(h.gate)
		h.gate = nil
	}
	h.mu.Unlock()
}

// Close ends the host: every call waiting is let go, and every call from
// now on answers context.Canceled; Log writes nowhere. Closing it again
// does nothing.
func (h *FakeHost) Close() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
	h.mu.Unlock()
	h.tell()
}

// Waiting answers how many calls Block holds now.
func (h *FakeHost) Waiting() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waiting
}

// Closed reports whether the host is closed.
func (h *FakeHost) Closed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// enter waits while the host is blocked and answers whether a call may
// act: context.Canceled once closed, ctx's error once it ended.
func (h *FakeHost) enter(ctx context.Context) error {
	h.mu.Lock()
	closed, gate := h.closed, h.gate
	h.mu.Unlock()
	if closed {
		return context.Canceled
	}
	if gate != nil {
		h.mu.Lock()
		h.waiting++
		h.mu.Unlock()
		h.tell()
		var err error
		select {
		case <-gate:
		case <-h.done:
			err = context.Canceled
		case <-ctx.Done():
			err = ctx.Err()
		}
		h.mu.Lock()
		h.waiting--
		h.mu.Unlock()
		h.tell()
		if err != nil {
			return err
		}
	}
	if h.Closed() {
		return context.Canceled
	}
	return ctx.Err()
}

// Launch records r and answers a run of its crew (run-1, run-2, …), or
// what OnLaunch set. The record, the callback and the caller each hold a
// copy of their own: a callback that changes its request changes neither
// the record nor the caller's.
func (h *FakeHost) Launch(ctx context.Context, r ext.LaunchRequest) (ext.RunRef, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunRef{}, err
	}
	rec := r
	rec.By = copyPrincipal(r.By)
	r.By = copyPrincipal(r.By)
	h.mu.Lock()
	h.launches = append(h.launches, rec)
	h.runs++
	n, fn := h.runs, h.launch
	h.mu.Unlock()
	h.tell()
	if err := r.Valid(); err != nil {
		return ext.RunRef{}, fmt.Errorf("%w: %v", ext.ErrRefused, err)
	}
	if fn != nil {
		return fn(r)
	}
	id := fmt.Sprintf("run-%d", n)
	return ext.RunRef{ID: id, Crew: r.Crew, URL: "/crews/" + r.Crew + "/runs/" + id}, nil
}

// LaunchTurn answers ext.ErrUnsupported, as a provider's Host does: a
// Brakeman turn is launched by its desk alone.
func (h *FakeHost) LaunchTurn(ctx context.Context, r ext.TurnLaunch) (ext.RunRef, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunRef{}, err
	}
	return ext.RunRef{}, fmt.Errorf("%w: a Brakeman turn is launched by its desk", ext.ErrUnsupported)
}

// LaunchApproved records r and answers a run (run-1, run-2, … counted
// with Launch's), or what OnLaunchApproved set; the record and the
// callback each hold a copy of their own, as Launch's.
func (h *FakeHost) LaunchApproved(ctx context.Context, r ext.ApprovedLaunch) (ext.RunRef, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunRef{}, err
	}
	rec := r
	rec.By = copyPrincipal(r.By)
	r.By = copyPrincipal(r.By)
	h.mu.Lock()
	h.approved = append(h.approved, rec)
	h.runs++
	n, fn := h.runs, h.approve
	h.mu.Unlock()
	h.tell()
	if err := r.Valid(); err != nil {
		return ext.RunRef{}, fmt.Errorf("%w: %v", ext.ErrRefused, err)
	}
	if fn != nil {
		return fn(r)
	}
	id := fmt.Sprintf("run-%d", n)
	return ext.RunRef{ID: id, URL: "/runs/" + id}, nil
}

// Submit records r and answers "entered", or what OnSubmit set; the
// record and the callback each hold a copy of their own, as Launch's.
func (h *FakeHost) Submit(ctx context.Context, r ext.SubmitRequest) (ext.SubmitResult, error) {
	if err := h.enter(ctx); err != nil {
		return ext.SubmitResult{}, err
	}
	rec := r
	rec.By = copyPrincipal(r.By)
	r.By = copyPrincipal(r.By)
	h.mu.Lock()
	h.submits = append(h.submits, rec)
	fn := h.submit
	h.mu.Unlock()
	h.tell()
	if err := r.Valid(); err != nil {
		return ext.SubmitResult{}, fmt.Errorf("%w: %v", ext.ErrRefused, err)
	}
	if fn != nil {
		return fn(r)
	}
	return ext.SubmitResult{Status: "entered"}, nil
}

// Decision answers what OnDecision set, or ext.ErrNotFound: the fake
// holds no ledger.
func (h *FakeHost) Decision(ctx context.Context, request string) (ext.DecisionState, error) {
	if err := h.enter(ctx); err != nil {
		return ext.DecisionState{}, err
	}
	h.mu.Lock()
	fn := h.decision
	h.mu.Unlock()
	h.tell()
	if fn != nil {
		return fn(request)
	}
	return ext.DecisionState{}, fmt.Errorf("%w: no decision %s", ext.ErrNotFound, request)
}

// Notify records n and answers nil, or what OnNotify set.
func (h *FakeHost) Notify(ctx context.Context, n ext.Notification) error {
	if err := h.enter(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	h.notices = append(h.notices, n)
	fn := h.notify
	h.mu.Unlock()
	h.tell()
	if err := n.Valid(); err != nil {
		return fmt.Errorf("%w: %v", ext.ErrRefused, err)
	}
	if fn != nil {
		return fn(n)
	}
	return nil
}

// DataDir answers the host's t.TempDir(); context.Canceled once closed.
func (h *FakeHost) DataDir() (string, error) {
	if h.Closed() {
		return "", context.Canceled
	}
	return h.dir, nil
}

// Log answers a logger into the test's log.
func (h *FakeHost) Log() *slog.Logger { return h.log }

// Launches answers every Launch's request, in order.
func (h *FakeHost) Launches() []ext.LaunchRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyAll(h.launches, func(r ext.LaunchRequest) ext.LaunchRequest { r.By = copyPrincipal(r.By); return r })
}

// ApprovedLaunches answers every LaunchApproved's request, in order.
func (h *FakeHost) ApprovedLaunches() []ext.ApprovedLaunch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyAll(h.approved, func(r ext.ApprovedLaunch) ext.ApprovedLaunch { r.By = copyPrincipal(r.By); return r })
}

// Submits answers every Submit's request, in order.
func (h *FakeHost) Submits() []ext.SubmitRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyAll(h.submits, func(r ext.SubmitRequest) ext.SubmitRequest { r.By = copyPrincipal(r.By); return r })
}

// Notices answers every Notify's notice, in order.
func (h *FakeHost) Notices() []ext.Notification {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ext.Notification(nil), h.notices...)
}
