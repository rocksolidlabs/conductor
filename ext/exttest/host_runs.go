package exttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

// fakeServer is what a FakeHost holds as the server would, and what the
// fakes Restarted from it share: the runs a provider's launches made, the
// operation ids that made them, the crews and the settings a resolve reads.
// The server keeps them in its ledger and its data directory, which outlast
// a provider's restart.
type fakeServer struct {
	mu       sync.Mutex
	n        int                 // the runs launches with an origin made
	runs     map[string]*fakeRun // by run id
	byOp     map[string]string   // a launch's operation id to its run
	managed  map[string]bool     // by crew; a crew not in it is managed
	commits  map[string]bool     // the commits the folder holds; nil holds any
	crews    map[string]fakeCrew // what a resolve of a crew starts from
	revs     int
	yolo     bool
	selected map[string]string      // by agent
	blobs    map[string]string      // a commit ("" for HEAD) to the manifest file's blob there
	bindings map[string]fakeBinding // by member
	capacity []ext.CapacityView
}

// fakeRun is a run the fake holds: one of its own (a launch with an origin
// made it) or another's (AddForeignRun).
type fakeRun struct {
	status ext.RunStatus
	own    bool
}

// fakeCrew is a crew a resolve starts from and its revision.
type fakeCrew struct {
	r   ext.ResolvedLaunch
	rev string
}

// fakeBinding is the account and model a resolve binds a member to.
type fakeBinding struct{ account, source, model string }

func newFakeServer() *fakeServer {
	return &fakeServer{
		runs: map[string]*fakeRun{}, byOp: map[string]string{}, managed: map[string]bool{}, crews: map[string]fakeCrew{},
		selected: map[string]string{}, blobs: map[string]string{}, bindings: map[string]fakeBinding{},
	}
}

// refusal is a *ext.Refusal with code and the words why.
func refusal(code, format string, args ...any) error {
	return &ext.Refusal{Code: code, Why: fmt.Sprintf(format, args...)}
}

// now is the fake's clock for a run's times: UTC, whole seconds, as the
// server's ledger keeps them.
func now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// Restarted answers a new FakeHost for t, as a provider's Start is handed
// after a restart of the provider, and closes h: the new one holds h's runs,
// its operation ids and its crews and settings (what the server keeps), and
// none of h's scripts or records.
func (h *FakeHost) Restarted(t T) *FakeHost {
	next := NewFakeHost(t)
	next.srv = h.srv
	h.Close()
	return next
}

// SetManaged has a launch with an origin of crew take it as a crew with
// tasks (true, the default) or refuse it not_managed (false).
func (h *FakeHost) SetManaged(crew string, managed bool) {
	h.srv.mu.Lock()
	h.srv.managed[crew] = managed
	h.srv.mu.Unlock()
	h.tell()
}

// SetCommits has the launch folder's repository hold shas alone: a launch
// or a resolve at another commit is refused start_commit_missing. By
// default it holds any.
func (h *FakeHost) SetCommits(shas ...string) {
	h.srv.mu.Lock()
	h.srv.commits = map[string]bool{}
	for _, c := range shas {
		h.srv.commits[c] = true
	}
	h.srv.mu.Unlock()
	h.tell()
}

// AddForeignRun has the fake hold a run that is not this provider's (a
// person's, another provider's, a continuation): Run, RunByOp and StopRun
// answer ext.ErrNotFound for it, as for no run at all. id is a run id
// (ext.ValidRunID), running.
func (h *FakeHost) AddForeignRun(id string) {
	crew := id
	if i := strings.LastIndexByte(id, '-'); i > 0 {
		crew = id[:i]
	}
	h.srv.mu.Lock()
	h.srv.runs[id] = &fakeRun{status: ext.RunStatus{ID: id, Crew: crew, State: ext.RunRunning, StartedAt: now()}}
	h.srv.mu.Unlock()
	h.tell()
}

// SetRunState sets a run's state and outcome ("" for none), as the engine
// moves it: a run set stopped, finished or interrupted is not running, and
// a stop of it answers ext.ErrNotRunning. A run the fake does not hold is
// left alone.
func (h *FakeHost) SetRunState(id, state, outcome string) {
	h.srv.mu.Lock()
	if r := h.srv.runs[id]; r != nil {
		r.status.State, r.status.Outcome = state, outcome
		if state == ext.RunStopped && r.status.StoppedAt.IsZero() {
			r.status.StoppedAt = now()
		}
	}
	h.srv.mu.Unlock()
	h.tell()
}

// OnRun has Run answer fn's answer for a run id of a run's shape.
func (h *FakeHost) OnRun(fn func(id string) (ext.RunStatus, error)) {
	h.mu.Lock()
	h.onRun = fn
	h.mu.Unlock()
}

// OnRunByOp has RunByOp answer fn's answer for an operation id of its
// shape.
func (h *FakeHost) OnRunByOp(fn func(opID string) (ext.RunStatus, error)) {
	h.mu.Lock()
	h.onRunByOp = fn
	h.mu.Unlock()
}

// OnStopRun has StopRun answer fn's answer for a valid request.
func (h *FakeHost) OnStopRun(fn func(ext.StopRequest) error) {
	h.mu.Lock()
	h.onStop = fn
	h.mu.Unlock()
}

// launchOwn is Launch of a valid request with an origin, as the server
// makes it: the run its operation id made, whatever the request asks
// (RunStatus.Origin says what that run was launched with); else, in the
// server's order, prior_unknown, not_managed, start_commit_missing, and with a
// digest, the launch resolved again with the settings now and refused
// launch_changed on another digest; else a new run, kept under the
// operation id.
func (h *FakeHost) launchOwn(r ext.LaunchRequest) (ext.RunRef, error) {
	s := h.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byOp[r.OpID]; ok {
		return runRef(s.runs[id].status), nil
	}
	if r.Subject.Prior != "" {
		if p := s.runs[r.Subject.Prior]; p == nil || !p.own {
			return ext.RunRef{}, refusal(ext.RefusalPriorUnknown, "the earlier run is not one this provider launched")
		}
	}
	if managed, set := s.managed[r.Crew]; set && !managed {
		return ext.RunRef{}, refusal(ext.RefusalNotManaged, "the crew %s has no tasks: a launch with an origin is of a crew with tasks", r.Crew)
	}
	if r.StartCommit != "" && !s.holds(r.StartCommit) {
		return ext.RunRef{}, refusal(ext.RefusalStartCommitMissing, "the folder's repository does not hold %s", r.StartCommit)
	}
	if r.ResolvedDigest != "" {
		got, err := s.resolve(ext.ResolveRequest{Crew: r.Crew, Project: r.Project, Commit: r.StartCommit})
		if err != nil {
			return ext.RunRef{}, err
		}
		if got.Digest != r.ResolvedDigest {
			return ext.RunRef{}, refusal(ext.RefusalLaunchChanged, "the launch resolves to another digest now")
		}
	}
	s.n++
	st := ext.RunStatus{
		ID: fmt.Sprintf("%s-%08x", r.Crew, s.n), Crew: r.Crew, State: ext.RunRunning, StartedAt: now(),
		Origin: ext.RunOrigin{
			OpID: r.OpID, Ref: r.Origin.Ref, Label: r.Origin.Label, Subject: r.Subject, Project: r.Project,
			StartCommit: r.StartCommit, ResolvedDigest: r.ResolvedDigest,
		},
	}
	if r.StopAfter > 0 {
		st.Origin.Deadline = st.StartedAt.Add(r.StopAfter)
	}
	s.runs[st.ID] = &fakeRun{status: st, own: true}
	s.byOp[r.OpID] = st.ID
	return runRef(st), nil
}

// runRef is the run's reference, as a launch answers it.
func runRef(s ext.RunStatus) ext.RunRef {
	return ext.RunRef{ID: s.ID, Crew: s.Crew, URL: "/runs/" + s.ID}
}

// holds reports whether the folder's repository holds commit. Under s.mu.
func (s *fakeServer) holds(commit string) bool { return s.commits == nil || s.commits[commit] }

// own is the run id names if it is one of this provider's. Under s.mu.
func (s *fakeServer) own(id string) (*fakeRun, bool) {
	r := s.runs[id]
	return r, r != nil && r.own
}

// Run answers a run this provider launched (a Launch with an origin made
// it), or what OnRun set: ext.ErrNotFound for any other run and for none;
// a refusal invalid_request for an id that has no run id's shape.
func (h *FakeHost) Run(ctx context.Context, id string) (ext.RunStatus, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunStatus{}, err
	}
	if !ext.ValidRunID(id) {
		return ext.RunStatus{}, refusal(ext.RefusalInvalidRequest, "%q is not a run's id", id)
	}
	h.mu.Lock()
	fn := h.onRun
	h.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	h.srv.mu.Lock()
	defer h.srv.mu.Unlock()
	r, ok := h.srv.own(id)
	if !ok {
		return ext.RunStatus{}, fmt.Errorf("%w: no run %s of this provider's", ext.ErrNotFound, id)
	}
	return r.status, nil
}

// RunByOp answers the run this provider's launch with the operation id
// opID made, as Run does, or what OnRunByOp set: ext.ErrNotFound for none.
// It never launches.
func (h *FakeHost) RunByOp(ctx context.Context, opID string) (ext.RunStatus, error) {
	if err := h.enter(ctx); err != nil {
		return ext.RunStatus{}, err
	}
	if !ext.ValidOpID(opID) {
		return ext.RunStatus{}, refusal(ext.RefusalInvalidRequest, "%q is not an operation's id", opID)
	}
	h.mu.Lock()
	fn := h.onRunByOp
	h.mu.Unlock()
	if fn != nil {
		return fn(opID)
	}
	h.srv.mu.Lock()
	defer h.srv.mu.Unlock()
	if r, ok := h.srv.own(h.srv.byOp[opID]); ok {
		return r.status, nil
	}
	return ext.RunStatus{}, fmt.Errorf("%w: no run of the operation %s", ext.ErrNotFound, opID)
}

// StopRun records r and stops a run this provider launched, or answers
// what OnStopRun set: ext.ErrNotFound for any other run and for none,
// ext.ErrNotRunning for a run that is stopped, finished or interrupted
// (nothing changes), a refusal invalid_request for a request its Valid
// refuses. A stopped run reads ext.RunStopped.
func (h *FakeHost) StopRun(ctx context.Context, r ext.StopRequest) error {
	if err := h.enter(ctx); err != nil {
		return err
	}
	rec := r
	rec.By = copyPrincipal(r.By)
	r.By = copyPrincipal(r.By)
	h.mu.Lock()
	h.stops = append(h.stops, rec)
	fn := h.onStop
	h.mu.Unlock()
	h.tell()
	if err := r.Valid(); err != nil {
		return refusal(ext.RefusalInvalidRequest, "%v", err)
	}
	if fn != nil {
		return fn(r)
	}
	h.srv.mu.Lock()
	run, ok := h.srv.own(r.Run)
	switch {
	case !ok:
		h.srv.mu.Unlock()
		return fmt.Errorf("%w: no run %s of this provider's", ext.ErrNotFound, r.Run)
	case run.status.State != ext.RunRunning && run.status.State != ext.RunNeedsInput:
		h.srv.mu.Unlock()
		return fmt.Errorf("%w: the run is %s", ext.ErrNotRunning, run.status.State)
	}
	run.status.State, run.status.StoppedAt, run.status.NeedsInput = ext.RunStopped, now(), 0
	h.srv.mu.Unlock()
	h.tell()
	return nil
}

// Stops answers every StopRun's request, in order.
func (h *FakeHost) Stops() []ext.StopRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyAll(h.stops, func(r ext.StopRequest) ext.StopRequest { r.By = copyPrincipal(r.By); return r })
}

// crewRevision is a revision for the n-th crew a fake was given: 64
// lower-case hex, as the server names a stored copy of a crew.
func crewRevision(crew string, n int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("exttest crew %s %d", crew, n)))
	return hex.EncodeToString(sum[:])
}
