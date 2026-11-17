package hostagent

import (
	"errors"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The agent's own session id, as the attention reports its hooks make to
// the host's socket name it (localreport.go), is the server's to keep, by
// the catalog's recipe for the agent, as its attention route keeps one
// (docs/protocol.md, The host's socket). The host carries it on
// agent_session, to a server whose registered said AgentSessions alone; a
// host names its socket to its agent only when its first server said so,
// and stops listening on it when a later one does not (dropReports), so
// that the agent's reports go to the server's route, which keeps the id,
// whatever server the host has.

// maxAgentSessionLog bounds the reports a host keeps to tell again.
const maxAgentSessionLog = 32

// agentSessionReport is one report's id and whether it was of a turn.
type agentSessionReport struct {
	id   string
	turn bool
}

// noteAgentSession keeps a report's id, in the order the reports came, and
// has it sent. Reports of the same id one after another are kept as one,
// its turn if either was of a turn: to the record's recipe they are the
// same (session.NextAgentSession). The log is told again from its start as
// each connection starts (replayAgentSessions): a report the record took
// already, taken again in its order, changes nothing (the latest id is
// still the latest, the lowest still the lowest, and a turn still marks
// the id resumable), so a report a lost connection may or may not have
// carried is never lost. Past maxAgentSessionLog reports the oldest goes;
// it was told unless the connection was down for all of them.
func (a *agent) noteAgentSession(id string, turn bool) {
	a.mu.Lock()
	if n := len(a.asLog); n > 0 && a.asLog[n-1].id == id {
		if !a.asLog[n-1].turn && turn {
			a.asLog[n-1].turn = true
			a.asNext = min(a.asNext, n-1)
		}
	} else {
		a.asLog = append(a.asLog, agentSessionReport{id: id, turn: turn})
		if len(a.asLog) > maxAgentSessionLog {
			a.asLog = a.asLog[1:]
			a.asNext = max(a.asNext-1, 0)
		}
	}
	a.mu.Unlock()
	a.flushAgentSessions()
}

// replayAgentSessions tells the log again from its start, as a connection
// starts.
func (a *agent) replayAgentSessions() {
	a.mu.Lock()
	a.asNext = 0
	a.mu.Unlock()
	a.flushAgentSessions()
}

// flushAgentSessions sends what of the log is not yet sent on the current
// connection, in order, one sender at a time. A send that fails leaves the
// rest for the next report or the next connection; nothing goes to a
// server that does not take it.
func (a *agent) flushAgentSessions() {
	a.mu.Lock()
	if a.asFlushing {
		a.mu.Unlock()
		return
	}
	a.asFlushing = true
	a.mu.Unlock()
	go func() {
		for {
			a.mu.Lock()
			if a.asNext >= len(a.asLog) || !a.agentSessions.Load() || (a.out == nil && a.sendHook == nil) {
				a.asFlushing = false
				a.mu.Unlock()
				return
			}
			r, sid := a.asLog[a.asNext], a.sessID
			a.mu.Unlock()
			err := a.send(proto.HostAgentSessionMsg{T: proto.HostAgentSession, SessionID: sid, AgentSession: r.id, Turn: r.turn})
			a.mu.Lock()
			if err != nil {
				a.asFlushing = false
				a.mu.Unlock()
				if !errors.Is(err, errConnDown) {
					a.log.Debug("an agent session id waits for the next connection", "err", err)
				}
				return
			}
			// The log may have changed meanwhile: the report sent is at or
			// before asNext still, unless the oldest went.
			if a.asNext < len(a.asLog) && a.asLog[a.asNext] == r {
				a.asNext++
			}
			a.mu.Unlock()
		}
	}()
}

// dropReports stops listening on the host's socket, for a server that
// does not take agent_session: the agent's sender finds it gone and
// reports to the server's route, as under a host of an earlier release.
func (a *agent) dropReports() {
	a.mu.Lock()
	l := a.reports
	a.reports = nil
	a.mu.Unlock()
	if l != nil {
		a.log.Info("the server keeps no agent session a host carries: the agent's reports go to the server")
		l.Close()
	}
}

// dropReportsAtEnd closes the host's socket as the host ends.
func (a *agent) dropReportsAtEnd() {
	a.mu.Lock()
	l := a.reports
	a.reports = nil
	a.mu.Unlock()
	l.Close()
}
