package hostagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

// Uplink publishes sessions that already run, a server's local sessions, to
// a rendezvous Conductor through the host protocol: one control connection
// per session, as `conductor host` makes for the process it runs. The
// rendezvous lists the session as hosted, serves its viewers over WebRTC or
// its relay, and mints the share links. It is a switchyard, which someone
// else may run (Options.Switchyard): the caller chains the hooks
// (Published.OnChange, OnActivity, OnChat), and the rendezvous is told the
// session's status and attention state, never its chat or its activity.
type Uplink struct {
	ServerURL string
	Token     string
	HostName  string
	RelayOnly bool
	// Insecure allows a ServerURL that is plain http on another machine, and
	// a redirect to one (Options.Insecure; rendezvous.insecure). The caller
	// says so in its log once; Publish refuses such a server without it.
	Insecure bool
	// ICEServers override the rendezvous's.
	ICEServers []proto.ICEServer
	// ICE is how the peers gather (Options.ICE).
	ICE          ICE
	ReconnectMax time.Duration
	Log          *slog.Logger
	// Decisions serves the lanes of the decision links minted at the
	// switchyard (docs/plans/decision-inbox.md, PR-6): handed to each
	// publication's viewers and to each run's desk (OpenDesk). The server
	// sets its API's (internal/cli, serve.go); nil serves none.
	Decisions Decisions
	// LinkGone, when set, is told each link the switchyard says no longer
	// opens what one of its connections carries, with that connection's
	// session or desk's run (Options.LinkGone).
	LinkGone func(linkID, sessionID, runID string)

	// instance is this server's secret with the rendezvous: with each
	// session's own id it gives a published session the same id after the
	// rendezvous restarts (proto.HostInfo.Instance). Made on first use.
	instOnce sync.Once
	instance string

	// stopped counts, by session, the publications the rendezvous ended with
	// a stop of its own: the session's next publication registers under a
	// local id of its own (localID), so it is a new session there and the
	// ended one's links do not open it. Rare, so never pruned.
	mu      sync.Mutex
	stopped map[string]int
}

func (u *Uplink) instanceID() string {
	u.instOnce.Do(func() { u.instance, _ = share.NewToken() })
	return u.instance
}

// localID is the local id a publication of session id registers under: the
// session's own, or after the rendezvous ended n publications of it,
// "<id>.<n>".
func (u *Uplink) localID(id string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if n := u.stopped[id]; n > 0 {
		return fmt.Sprintf("%s.%d", id, n)
	}
	return id
}

// noteStopped counts a publication of session id the rendezvous ended.
func (u *Uplink) noteStopped(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped == nil {
		u.stopped = map[string]int{}
	}
	u.stopped[id]++
}

// options are the Options of a publication of the session info, or of a
// desk (info.Name the run's): to the switchyard, with the uplink's
// Decisions and LinkGone.
func (u *Uplink) options(info session.Info) Options {
	log := u.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	opts := Options{
		ServerURL: u.ServerURL, Token: u.Token, Name: info.Name, HostName: u.HostName, AgentID: info.AgentID,
		Argv: info.Command, Dir: info.Cwd, RelayOnly: u.RelayOnly, Insecure: u.Insecure, ICEServers: u.ICEServers, ICE: u.ICE, ReconnectMax: u.ReconnectMax, Log: log,
		Switchyard: true, Decisions: u.Decisions, LinkGone: u.LinkGone,
	}
	if opts.HostName == "" {
		opts.HostName, _ = os.Hostname()
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = 30 * time.Second
	}
	return opts
}

// Published is a session published to the rendezvous.
type Published struct {
	// SessionID is the rendezvous's id for the session; ShareBaseURL the
	// base its links take.
	SessionID    string
	ShareBaseURL string

	a      *agent
	cancel context.CancelFunc
	stop   func()
	done   chan struct{}
	once   sync.Once
	// there says the switchyard ended the publication (its stop).
	there atomic.Bool
}

// Done is closed once the publication has ended: Stop, the session's end
// (endLinger after it), or a stop the switchyard sent (StoppedThere).
func (p *Published) Done() <-chan struct{} { return p.done }

// StoppedThere says the switchyard ended the publication with a stop of its
// own. The session goes on here; publishing it again is a new publication.
func (p *Published) StoppedThere() bool { return p.there.Load() }

// CurrentSessionID is the rendezvous's id for the session now: SessionID
// unless the rendezvous restarted and the session was registered afresh.
func (p *Published) CurrentSessionID() string {
	p.a.mu.Lock()
	defer p.a.mu.Unlock()
	return p.a.sessID
}

// CurrentID is CurrentSessionID, as the api's PublishedSession names it.
func (p *Published) CurrentID() string { return p.CurrentSessionID() }

// RunLink mints, at the rendezvous, one link to the sessions of a run's
// members there, over this session's connection (which must be a member's).
func (p *Published) RunLink(ctx context.Context, role string, ttl time.Duration, label string, run proto.RunGroup) (proto.LinkCreated, error) {
	return p.a.requestRunLink(ctx, role, ttl, label, run)
}

// RunLinkUpdate tells the rendezvous a run's members now.
func (p *Published) RunLinkUpdate(ctx context.Context, run proto.RunGroup) error {
	return p.a.requestRunUpdate(ctx, run)
}

// HeldLinks is what the rendezvous last said it holds of the links minted
// for the session: the ids in its registered answer, plus those minted and
// less those revoked since; false when it never said (an older rendezvous).
func (p *Published) HeldLinks() ([]string, bool) { return p.a.heldLinks() }

// Revoke asks the rendezvous to revoke a link it minted for the session;
// a link it no longer knows answers an error that names not_found.
func (p *Published) Revoke(ctx context.Context, linkID string) error {
	return p.a.requestRevoke(ctx, linkID)
}

// Link asks the rendezvous for a share link to the session: its URL there,
// the same as an invite, and the link's id, role, label and expiry.
func (p *Published) Link(ctx context.Context, role string, ttl time.Duration, label string) (proto.LinkCreated, error) {
	return p.a.requestLink(ctx, role, ttl, label)
}

// Publish registers local with the rendezvous (ctx bounds the registration)
// and serves its viewers until the session ends or Stop is called. It
// returns once registered.
func (u *Uplink) Publish(ctx context.Context, local *session.Local) (*Published, error) {
	if local == nil {
		return nil, errors.New("uplink: no session")
	}
	if err := plainRefused(u.ServerURL, u.Insecure); err != nil {
		return nil, err
	}
	log := u.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	info := local.Info()
	opts := u.options(info)
	wsURL, err := controlURL(u.ServerURL)
	if err != nil {
		return nil, err
	}
	// The agent token registered with the rendezvous lets an agent report
	// attention there; agents of a published session report to their own
	// server, so this one is used by nobody, and is fresh for every publish.
	agentToken, _ := share.NewToken()
	a := &agent{opts: opts, wsURL: wsURL, dir: info.Cwd, cols: info.Cols, rows: info.Rows, peers: map[string]*peer{}, log: log, agentToken: agentToken, local: local, instance: u.instanceID(), localID: u.localID(info.ID)}
	a.activity = newActivityForwarder(a.sendActivity, a.sendChat, log)
	conn, registered, err := a.dialAndRegister(ctx)
	if err != nil {
		return nil, err
	}
	// The publication outlives the registration's context: Stop or the
	// session's end are what end it.
	runCtx, cancel := context.WithCancel(context.Background())
	p := &Published{SessionID: registered.SessionID, ShareBaseURL: registered.ShareBaseURL, a: a, cancel: cancel, done: make(chan struct{})}
	stopped := make(chan struct{})
	p.stop = func() { close(stopped) }
	a.unpublish = func() {
		if p.there.CompareAndSwap(false, true) {
			u.noteStopped(info.ID)
		}
		p.once.Do(p.stop)
	}
	go a.activity.run(runCtx)
	go a.controlLoop(runCtx, conn)
	go func() {
		defer close(p.done)
		select {
		case <-local.Ended():
			a.settle(runCtx)
			// The rendezvous may write the viewers of the session's links
			// what it relayed them last for a while yet (its closeWait):
			// the publication lasts as long, so that a revoke of one of
			// those links from here reaches it meanwhile, unless Stop
			// comes first.
			a.closeAllPeers()
			select {
			case <-time.After(endLinger):
			case <-stopped:
			case <-runCtx.Done():
			}
		case <-stopped:
			// Stop, or the switchyard's own stop: the session goes on here;
			// for the rendezvous it is over. Tell it so, rather than leave it
			// waiting for a host that will not return, and let its viewers go.
			a.send(proto.HostStatusMsg{T: proto.HostStatus, SessionID: p.CurrentSessionID(), Status: string(session.StatusStopped)})
			a.flushViewers(runCtx)
		}
		cancel()
		a.closeAllPeers()
		// The lanes' workers under way end before the publication is
		// done: none outlasts it (the server's stop waits on Done).
		a.lanes.end()
	}()
	return p, nil
}

// OnChange forwards a change of the local session (its attention) to the
// rendezvous; the caller chains it from the session's OnChange hook. It
// returns at once (noteChange).
func (p *Published) OnChange(info session.Info) { p.a.noteChange(info) }

// OnChat is the local session's OnChat hook. The rendezvous is sent no
// chat: the people on the session have it over their own connection.
func (p *Published) OnChat(id string, m session.ChatMessage) { p.a.onLocalChat(id, m) }

// OnActivity is the local session's OnActivity hook; the caller chains it.
// It never waits, and the rendezvous is sent no entry: the publication only
// notes when the session's final status entry is recorded.
func (p *Published) OnActivity(id string, e session.ActivityEntry, state session.AttentionState) {
	p.a.onLocalActivity(id, e, state)
}

// Decisions says the rendezvous's last registered said it carries
// decisions (proto.Registered.Decisions): only then is a decision link of
// the session minted there.
func (p *Published) Decisions() bool { return p.a.decisions.Load() }

// ID is the rendezvous's id for the session; Base the base its links take.
func (p *Published) ID() string   { return p.SessionID }
func (p *Published) Base() string { return p.ShareBaseURL }

// Stop ends the publication: the rendezvous sees the session leave. The
// local session itself is left alone.
func (p *Published) Stop() {
	p.once.Do(p.stop)
	<-p.done
}
