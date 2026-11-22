package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/machine"
)

// serverMachine is what this server keeps to group the hosts of its own
// machine with its own row (docs/plans/hosts.md, PR-1): its key of this
// start, 32 random bytes made as the server is made and held in memory
// only, never logged, which it sends a host in the upgrade of GET /ws/host
// it accepts (proto.ServerKeyHeader); and its own row's key, made from its
// own machine's id by the one function the hub keys a host of this machine
// by.
type serverMachine struct {
	key [machine.ServerKeyLen]byte
	mu  sync.Mutex
	// own is machine.Group(machine.Key(id, key)) of this machine's id, ""
	// until the composition root says it (SetOwnMachine). Guarded by mu.
	own string
}

// newServerMachine makes this start's key.
func newServerMachine() *serverMachine {
	m := &serverMachine{}
	_, _ = rand.Read(m.key[:]) // never fails (crypto/rand)
	return m
}

// serverKeyHeader is the value of proto.ServerKeyHeader on an upgrade of
// GET /ws/host this server accepts from a host: "" for a server made
// without New (tests that build a Server by hand).
func (s *Server) serverKeyHeader() string {
	if s.machine == nil {
		return ""
	}
	return hex.EncodeToString(s.machine.key[:])
}

// SetOwnMachine gives the server this machine's id, read once at the start
// in the composition root (cli.runServe, never in a route): its own row's
// key is made from it and this start's key as a host of this machine makes
// the key it sends, then kept as the hub keeps that host's (machine.Group),
// so the two are one string. The id itself is not kept. An id not of its
// form keeps none.
func (s *Server) SetOwnMachine(id string) {
	if s.machine == nil {
		return
	}
	own := ""
	if sent := machine.Key(id, s.machine.key[:]); sent != "" {
		own = machine.Group(sent)
	}
	s.machine.mu.Lock()
	s.machine.own = own
	s.machine.mu.Unlock()
}

// OwnHostKey is the key this server's own row is grouped by among its
// hosts' machines (session.Info.HostKey's form): "" when it was given no
// machine id.
func (s *Server) OwnHostKey() string {
	if s.machine == nil {
		return ""
	}
	s.machine.mu.Lock()
	defer s.machine.mu.Unlock()
	return s.machine.own
}
