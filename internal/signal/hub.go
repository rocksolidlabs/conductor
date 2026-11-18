package signal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/share"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// Hub indexes hosted sessions by ID.
type Hub struct {
	mu       sync.Mutex
	registry *session.Registry
	sessions map[string]*HostedSession
	log      *slog.Logger
	// MaxViewers caps viewers per hosted session.
	MaxViewers int
	// OnChange is called after a hosted session's Info changes.
	OnChange func(session.Info)
	// OnActivity is called with a hosted session's ID and each activity entry
	// its host reports, after the entry has been checked and cleaned, and,
	// for an attention entry, the attention state the host says it records
	// ("" when the host sent none it may send). It runs on the goroutine that
	// reads the host's connection, so it must not block; calls for different
	// hosts overlap, so it must be safe for concurrent use. Set it, like
	// OnChange, before the first host registers.
	OnActivity func(sessionID string, e session.ActivityEntry, state session.AttentionState)
	// OnLateActivity, when set, takes in OnActivity's place an entry a host
	// sends late (proto.HostActivityMsg.Late: recorded before the connection
	// that carries it began, or sent again) and the notice of entries it
	// left out: kept and shown, offered to no sink as a live event.
	OnLateActivity func(sessionID string, e session.ActivityEntry, state session.AttentionState)
	// OnChat is called with a hosted session's ID and each chat message its
	// host reports, after the message has been checked and cleaned. The same
	// contract as OnActivity: it must not block, and it must be safe for
	// concurrent use. Set it before the first host registers.
	OnChat func(sessionID string, m session.ChatMessage)
	// OnAdded is called with each hosted session a register made (not one
	// it attached again), once the registry holds it and before its first
	// change is told, on the registering connection's goroutine with no lock
	// of the hub's held; OnSessionInfo with each session_info its host
	// sends, checked (report.go). Set them before the first host registers.
	OnAdded       func(*HostedSession)
	OnSessionInfo func(*HostedSession, proto.HostSessionInfoMsg)
	// Tokens, when set, is the set of live agent tokens a hosted session's
	// token is entered in (setAgentToken): from its registration, or an
	// attach that carries a token, until the session ends, leaves the
	// registry or this table, or its registration fails; nil keeps none.
	// Desks carry no token. Set it before the first host registers.
	Tokens session.TokenSet
	// registering holds the ids of the registrations with an instance
	// under way, from the look-up that found no session under the id to
	// the insert into sessions or the failure: a second one meanwhile is
	// refused, so one id is never made twice. Guarded by mu.
	registering map[string]struct{}
	// decisions says this switchyard carries a decision link's lane
	// (SetDecisions).
	decisions atomic.Bool
}

// SetDecisions says whether this switchyard carries a decision link's
// lane: its registered says so (proto.Registered.Decisions), and only then
// does a publisher mint a decision link here. It relays the lane's frames
// and keeps desks (RegisterDesk) whatever it says; it is said where the
// switchyard's join page has the Decide tab it serves
// (docs/plans/decision-inbox.md, PR-7), so no build advertises what its
// page lacks: the serve command says it (api.Server.SetSwitchyardDecisions).
func (hub *Hub) SetDecisions(on bool) { hub.decisions.Store(on) }

// Decisions is what SetDecisions last said.
func (hub *Hub) Decisions() bool { return hub.decisions.Load() }

// NewHub creates a hub that registers hosted sessions in registry.
func NewHub(registry *session.Registry, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{registry: registry, sessions: map[string]*HostedSession{}, log: log, MaxViewers: 32}
}

// Errors returned by Register.
var (
	ErrBadRegister = errors.New("signal: invalid registration")
	ErrBadResume   = errors.New("signal: cannot resume session")
)

// Register creates a hosted session (or resumes one) and attaches conn.
func (hub *Hub) Register(reg proto.Register, conn *HostConn) (*HostedSession, bool, error) {
	if reg.Proto != proto.ProtoVersion {
		return nil, false, ErrBadRegister
	}
	if len(reg.Session.Command) == 0 || strings.TrimSpace(reg.Session.Command[0]) == "" {
		return nil, false, ErrBadRegister
	}
	if len(reg.Session.Name) > 120 || len(reg.Host.Name) > 120 || len(reg.Session.Cwd) > proto.MaxCwd {
		return nil, false, ErrBadRegister
	}
	if len(reg.Host.User) > proto.MaxHostUser || len(reg.Session.Branch) > session.MaxBranchLen {
		return nil, false, ErrBadRegister
	}
	if (reg.Host.Instance == "") != (reg.Session.LocalID == "") || len(reg.Host.Instance) > proto.MaxHostInstance || len(reg.Session.LocalID) > proto.MaxLocalID {
		return nil, false, ErrBadRegister
	}
	if reg.Session.HostReport.Check() != nil {
		return nil, false, ErrBadRegister
	}
	if reg.Resume != nil {
		hub.mu.Lock()
		hs, ok := hub.sessions[reg.Resume.SessionID]
		hub.mu.Unlock()
		if !ok || hs.desk != "" || !share.Equal(reg.Resume.Secret, hs.secret) {
			return nil, false, ErrBadResume
		}
		// A session that left the registry or the table is not resumed.
		if attached, _ := hs.attachLive(reg, conn); !attached {
			return nil, false, ErrBadResume
		}
		return hs, true, nil
	}
	id := session.NewID()
	owner := ""
	if reg.Host.Instance != "" {
		// The same instance and local id give the same id after a restart of
		// this server; a session still held under it (its host away) is
		// taken back, one with a live connection refused.
		id = HostedID(reg.Host.Instance, reg.Session.LocalID)
		owner = ownerOf(reg.Host.Instance)
		for looks := 0; ; looks++ {
			hub.mu.Lock()
			hs, ok := hub.sessions[id]
			if ok && hs.desk == "" && hs.forgetIfLeft() {
				// It left the registry (retired) and the hub had not yet
				// forgotten it: forgotten here, as Expire would, and the
				// id registered anew.
				delete(hub.sessions, id)
				ok = false
			}
			if !ok {
				// A first registration of this id: another under way is
				// refused, nothing made, and this one is marked until the
				// session is in the table or the registration failed.
				if _, under := hub.registering[id]; under {
					hub.mu.Unlock()
					return nil, false, ErrBadRegister
				}
				if hub.registering == nil {
					hub.registering = map[string]struct{}{}
				}
				hub.registering[id] = struct{}{}
			}
			hub.mu.Unlock()
			if !ok {
				break
			}
			if hs.desk != "" || hs.owner != owner {
				return nil, false, ErrBadRegister
			}
			attached, left := hs.attachLive(reg, conn)
			if attached {
				return hs, true, nil
			}
			if !left || looks > 0 {
				return nil, false, ErrBadRegister
			}
			// It left the table or the registry between the look-up and
			// the attach (an expiry, a removal): look again, and register
			// anew if nothing holds the id now.
		}
	}
	secret, _ := share.NewToken()
	name := strings.TrimSpace(reg.Session.Name)
	if name == "" {
		name = reg.Session.Command[0] + " @ " + reg.Host.Name
	}
	hs := &HostedSession{
		hub:        hub,
		owner:      owner,
		secret:     secret,
		log:        hub.log,
		conn:       conn,
		relayOnly:  reg.Session.RelayOnly,
		viewers:    map[string]*Viewer{},
		maxViewers: hub.MaxViewers,
		report:     reg.Session.HostReport,
		added:      make(chan struct{}),
		info: session.Info{
			ID:        id,
			Name:      name,
			Kind:      session.KindHosted,
			AgentID:   reg.Session.AgentID,
			Command:   reg.Session.Command,
			Cwd:       reg.Session.Cwd,
			Status:    session.StatusRunning,
			Cols:      reg.Session.Cols,
			Rows:      reg.Session.Rows,
			HostName:  reg.Host.Name,
			HostUser:  reg.Host.User,
			Branch:    reg.Session.Branch,
			CreatedAt: time.Now().UTC(),
		},
	}
	hs.setAgentToken(reg.Session.AgentToken)
	hs.setAnswerAs(reg.Host.AnswerAs)
	if err := hub.registry.Add(hs); err != nil {
		// Never shared: its entry goes with the registration.
		hs.endToken()
		hub.mu.Lock()
		delete(hub.registering, id)
		hub.mu.Unlock()
		return nil, false, err
	}
	hub.mu.Lock()
	hub.sessions[hs.info.ID] = hs
	delete(hub.registering, id)
	hub.mu.Unlock()
	if hub.OnAdded != nil {
		hub.OnAdded(hs)
	}
	close(hs.added)
	hs.notifyChange()
	return hs, false, nil
}

// attach puts conn on a session whose host is away (a resume, or the same
// instance registering again) and refreshes what the host says of it; false
// when the session has a live connection, or has left the registry or the
// hub's table (attachLive).
func (hs *HostedSession) attach(reg proto.Register, conn *HostConn) bool {
	attached, _ := hs.attachLive(reg, conn)
	return attached
}

// attachLive is attach saying why it refused: left, the session left the
// registry (retired) or the hub's table (forgotten), which no host is put
// on again; otherwise it has a live connection.
func (hs *HostedSession) attachLive(reg proto.Register, conn *HostConn) (attached, left bool) {
	hs.mu.Lock()
	if hs.retired || hs.forgotten {
		hs.mu.Unlock()
		return false, true
	}
	if hs.conn != nil {
		hs.mu.Unlock()
		return false, false
	}
	hs.conn = conn
	if !hs.info.Status.Ended() {
		hs.info.Status = session.StatusRunning
	}
	hs.info.Cols, hs.info.Rows = reg.Session.Cols, reg.Session.Rows
	hs.info.Branch = reg.Session.Branch
	hs.info.HostUser = reg.Host.User
	hs.relayOnly = reg.Session.RelayOnly
	hs.setAnswerAs(reg.Host.AnswerAs)
	if reg.Session.AgentToken != "" {
		hs.setAgentToken(reg.Session.AgentToken)
	}
	hs.mu.Unlock()
	hs.notifyChange()
	return true, false
}

// HostedID is the id of a session registered with an instance and a local
// id: the same on every registration, so a link minted for it names it again
// after this server restarts, and not to be guessed without the instance.
func HostedID(instance, localID string) string {
	sum := sha256.Sum256([]byte("conductor hosted session\x00" + instance + "\x00" + localID))
	return hex.EncodeToString(sum[:session.IDLen/2])
}

// ownerOf is what a session keeps of its host's instance: a hash, never the secret.
func ownerOf(instance string) string {
	sum := sha256.Sum256([]byte("conductor host instance\x00" + instance))
	return hex.EncodeToString(sum[:16])
}

// Get returns the hosted session with the given ID.
func (hub *Hub) Get(id string) (*HostedSession, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hs, ok := hub.sessions[id]
	return hs, ok
}

// Expire removes sessions whose host has been gone longer than the grace
// period, and forgets ended sessions that the registry already dropped.
// A session is judged, marked forgotten (no host is attached to it again)
// and its token entry removed in one hold of its lock, inside the hold of
// the hub's that takes it out of the table: a host that attached before
// the judgement keeps it, one that comes after finds it gone. The
// registry loses only the session expired: one registered under its id
// since is left alone.
func (hub *Hub) Expire(now time.Time) {
	hub.mu.Lock()
	var desks, gone []*HostedSession
	for id, hs := range hub.sessions {
		// A desk is in no registry: only its host gone past the grace
		// ends it. A session the registry no longer holds as itself is
		// forgotten whatever its host does.
		registered := true
		if hs.desk == "" {
			d, ok := hub.registry.Get(id)
			registered = ok && d == session.Driver(hs)
		}
		hs.mu.Lock()
		expire := !registered || hs.conn == nil && !hs.disconnectedAt.IsZero() && now.Sub(hs.disconnectedAt) >= DisconnectGrace
		if expire {
			hs.forgotten = true
			hs.endToken()
		}
		hs.mu.Unlock()
		if !expire {
			continue
		}
		delete(hub.sessions, id)
		if hs.desk != "" {
			desks = append(desks, hs)
		} else {
			gone = append(gone, hs)
		}
	}
	hub.mu.Unlock()
	for _, d := range desks {
		hub.log.Info("desk expired", "desk", d.info.ID)
		d.retireDesk()
	}
	for _, hs := range gone {
		id := hs.info.ID
		d, ok := hub.registry.Get(id)
		if !ok {
			continue
		}
		if d == session.Driver(hs) {
			hub.log.Info("hosted session expired", "session", id)
		}
		if !hub.registry.RemoveIf(id, hs) {
			// Registered again under its id since it left the table: the
			// new one stays; this one leaves as a removal retires it.
			hs.Retire()
		}
	}
}

// CloseAll disconnects every host and viewer (server shutdown).
func (hub *Hub) CloseAll() {
	hub.mu.Lock()
	list := make([]*HostedSession, 0, len(hub.sessions))
	for _, hs := range hub.sessions {
		list = append(list, hs)
	}
	hub.mu.Unlock()
	for _, hs := range list {
		hs.CloseViewers(errors.New("server shutting down"))
		hs.mu.Lock()
		conn := hs.conn
		hs.mu.Unlock()
		if conn != nil {
			conn.Close()
		}
	}
}
