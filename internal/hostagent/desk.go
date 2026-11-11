package hostagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// A run's desk at the switchyard (docs/plans/decision-inbox.md, PR-6):
// one more host connection of the uplink's, for a run with a live
// decision link there, that has no PTY, no output, no input and no
// terminal frames, only a decision link's lane. A member's publication
// ends with its session, so a run whose last attempt failed, or whose
// first has not started, would have nothing to carry its decisions: the
// desk carries them, whether or not a member lives. It registers with
// register_desk under the uplink's instance and the run's local id
// (proto.DeskLocalID), so it is the publisher's and takes the same id
// after its connection drops (resume) and after the switchyard restarts
// (a fresh register_desk), and a run link minted over it names it
// (proto.RunGroup.Desk). Its viewers are a run decision link's, each
// welcomed with decisions and served by the Uplink's Decisions; a viewer
// of any other link is refused. The caller ends it (Stop): at the run's
// end, its last decision link's, or the server's stop.

// deskRegistration is the register_desk of the agent's desk; the caller
// holds a.mu.
func (a *agent) deskRegistration(resume *proto.HostResume) proto.RegisterDesk {
	return proto.RegisterDesk{
		T: proto.HostRegisterDesk, Proto: proto.ProtoVersion,
		Host:    proto.HostInfo{Name: a.opts.HostName, Version: version.Version, Instance: a.instance},
		LocalID: proto.DeskLocalID(a.desk.ID), Run: *a.desk, Resume: resume,
	}
}

// ErrNoDesks says the switchyard keeps no desks: an older one, which
// closes a register_desk.
var ErrNoDesks = errors.New("the switchyard keeps no desks")

// Desk is a run's desk at the switchyard. Its viewers' lanes live under
// its agent's laneGroup, and its end (end) waits for their workers before
// it tells the switchyard, lets the viewers go and is done.
type Desk struct {
	a      *agent
	cancel context.CancelFunc
	done   chan struct{}
	stop   chan struct{}
	once   sync.Once
	// told is set once the switchyard has been told the desk ended (or
	// that word could not wait longer: flushControl's bound).
	told atomic.Bool
}

// newDesk is the desk of the agent a, whose connection's context cancel
// ends; its end runs once started (end), after its stop.
func (a *agent) newDesk(cancel context.CancelFunc) *Desk {
	k := &Desk{a: a, cancel: cancel, done: make(chan struct{}), stop: make(chan struct{})}
	// A stop from the switchyard ends the desk here too.
	a.unpublish = func() { k.once.Do(func() { close(k.stop) }) }
	return k
}

// end is the desk's end, once it is stopped: its lanes first (nothing
// begun from now on, those under way waited for), then the switchyard
// told, and the viewers let go.
func (k *Desk) end(runCtx context.Context) {
	defer close(k.done)
	<-k.stop
	a := k.a
	a.lanes.end()
	// Told it ended, the switchyard forgets the desk at once and lets its
	// viewers go, rather than hold it for a host that will not return.
	a.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: k.CurrentID(), Status: string(session.StatusStopped)})
	a.flushControl(runCtx)
	k.told.Store(true)
	k.cancel()
	a.closeAllPeers()
}

// OpenDesk registers the desk of the run with the switchyard (ctx bounds
// the registration) and serves its viewers until Stop, or the switchyard
// ends it. It returns once registered.
func (u *Uplink) OpenDesk(ctx context.Context, run proto.DeskRun) (*Desk, error) {
	if run.ID == "" {
		return nil, errors.New("uplink: a desk needs a run")
	}
	if err := plainRefused(u.ServerURL, u.Insecure); err != nil {
		return nil, err
	}
	opts := u.options(session.Info{Name: run.Name})
	log := opts.Log
	wsURL, err := controlURL(u.ServerURL)
	if err != nil {
		return nil, err
	}
	d := run
	a := &agent{opts: opts, wsURL: wsURL, peers: map[string]*peer{}, log: log, instance: u.instanceID(), desk: &d}
	conn, _, err := a.dialAndRegister(ctx)
	var closed websocket.CloseError
	if errors.As(err, &closed) && closed.Reason == "register expected" {
		// An older switchyard: its first message must be a register.
		return nil, ErrNoDesks
	}
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	k := a.newDesk(cancel)
	go a.controlLoop(runCtx, conn)
	go k.end(runCtx)
	return k, nil
}

// CurrentID is the switchyard's id for the desk.
func (k *Desk) CurrentID() string { return k.a.sessionID() }

// Decisions says the switchyard's last registered said it carries
// decisions: only then is a decision link minted over the desk.
func (k *Desk) Decisions() bool { return k.a.decisions.Load() }

// RunLink mints, at the switchyard, a link to the run over the desk's
// connection (the group names the desk: proto.RunGroup.Desk).
func (k *Desk) RunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error) {
	return k.a.requestRunLink(ctx, role, ttl, label, run)
}

// RunLinkUpdate tells the switchyard the run's members now, over the desk.
func (k *Desk) RunLinkUpdate(ctx context.Context, run proto.RunGroup) error {
	return k.a.requestRunUpdate(ctx, run)
}

// Revoke revokes a link of the run at the switchyard, over the desk.
func (k *Desk) Revoke(ctx context.Context, linkID string) error {
	return k.a.requestRevoke(ctx, linkID)
}

// HeldLinks is what the switchyard last said it holds of the run's links
// for the desk (Published.HeldLinks).
func (k *Desk) HeldLinks() ([]string, bool) { return k.a.heldLinks() }

// Done is closed once the desk has ended, Stop or the switchyard's stop.
func (k *Desk) Done() <-chan struct{} { return k.done }

// Stop ends the desk (the switchyard is told, its viewers let go), and
// waits for its end until ctx is done: past that the end goes on by
// itself, and the error says what of it was left (lane operations still
// under way, the switchyard not yet told).
func (k *Desk) Stop(ctx context.Context) error {
	k.once.Do(func() { close(k.stop) })
	select {
	case <-k.done:
		return nil
	case <-ctx.Done():
	}
	select {
	case <-k.done:
		return nil
	default:
	}
	switch n := k.a.lanes.pending(); {
	case n > 0:
		return fmt.Errorf("the desk's end goes on by itself: %d lane operations under way, the switchyard not yet told", n)
	case !k.told.Load():
		return errors.New("the desk's end goes on by itself: the switchyard not yet told")
	default:
		return errors.New("the desk's end goes on by itself: its viewers being let go")
	}
}

// flushControl waits, at most endDrain, for what the control connection
// holds to be written: the desk's last word before it closes.
func (a *agent) flushControl(ctx context.Context) {
	deadline := time.Now().Add(endDrain)
	for time.Now().Before(deadline) && ctx.Err() == nil && a.unsent() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
