package signal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
	// OnChat is called with a hosted session's ID and each chat message its
	// host reports, after the message has been checked and cleaned. The same
	// contract as OnActivity: it must not block, and it must be safe for
	// concurrent use. Set it before the first host registers.
	OnChat func(sessionID string, m session.ChatMessage)
}

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
	if reg.Resume != nil {
		hub.mu.Lock()
		hs, ok := hub.sessions[reg.Resume.SessionID]
		hub.mu.Unlock()
		if !ok || !share.Equal(reg.Resume.Secret, hs.secret) {
			return nil, false, ErrBadResume
		}
		if !hs.attach(reg, conn) {
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
		hub.mu.Lock()
		hs, ok := hub.sessions[id]
		hub.mu.Unlock()
		if ok {
			if hs.owner != owner || !hs.attach(reg, conn) {
				return nil, false, ErrBadRegister
			}
			return hs, true, nil
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
		return nil, false, err
	}
	hub.mu.Lock()
	hub.sessions[hs.info.ID] = hs
	hub.mu.Unlock()
	hs.notifyChange()
	return hs, false, nil
}

// attach puts conn on a session whose host is away (a resume, or the same
// instance registering again) and refreshes what the host says of it; false
// when the session has a live connection.
func (hs *HostedSession) attach(reg proto.Register, conn *HostConn) bool {
	hs.mu.Lock()
	if hs.conn != nil {
		hs.mu.Unlock()
		return false
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
	return true
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
func (hub *Hub) Expire(now time.Time) {
	hub.mu.Lock()
	var expired []string
	for id, hs := range hub.sessions {
		hs.mu.Lock()
		gone := hs.conn == nil && !hs.disconnectedAt.IsZero() && now.Sub(hs.disconnectedAt) >= DisconnectGrace
		hs.mu.Unlock()
		if _, inRegistry := hub.registry.Get(id); gone || !inRegistry {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(hub.sessions, id)
	}
	hub.mu.Unlock()
	for _, id := range expired {
		if _, ok := hub.registry.Get(id); ok {
			hub.log.Info("hosted session expired", "session", id)
			hub.registry.Remove(id)
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
