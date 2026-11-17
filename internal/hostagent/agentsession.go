package hostagent

import (
	"context"
	"errors"
	"time"

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

// maxAgentSessionLog bounds the reports a host keeps to tell.
const maxAgentSessionLog = 32

// agentSessionReport is one report's id and whether it was of a turn; seq
// orders the log's entries, an entry changed since it was sent (its turn
// set) taking a new one.
type agentSessionReport struct {
	id   string
	turn bool
	seq  uint64
}

// noteAgentSession keeps a report's id, in the order the reports came, and
// has it sent. Reports of the same id one after another are kept as one,
// its turn if either was of a turn: to the record's recipe they are the
// same (session.NextAgentSession). An entry is sent once on a connection,
// and is known taken once the server has answered a ping the host sent
// after it on that connection (the server reads in order and takes the
// entry before it reads the ping): such an entry is never sent again. As a
// connection starts, the entries not known taken are sent again, in order
// (replayAgentSessions): one the record took already, taken again in its
// order before the newer ones, leaves the record where the newest puts
// it. Past maxAgentSessionLog entries the oldest goes.
func (a *agent) noteAgentSession(id string, turn bool) {
	a.mu.Lock()
	if a.asDropped {
		// The socket was dropped: the agent's reports go to the server's
		// route, which keeps their ids; one a report still under way on
		// the socket names is kept by no one, never sent after them.
		a.mu.Unlock()
		return
	}
	a.asSeq++
	if n := len(a.asLog); n > 0 && a.asLog[n-1].id == id {
		if !a.asLog[n-1].turn && turn {
			a.asLog[n-1].turn, a.asLog[n-1].seq = true, a.asSeq
		}
	} else {
		a.asLog = append(a.asLog, agentSessionReport{id: id, turn: turn, seq: a.asSeq})
		if len(a.asLog) > maxAgentSessionLog {
			a.asLog = a.asLog[1:]
		}
	}
	a.mu.Unlock()
	a.flushAgentSessions()
}

// replayAgentSessions has a connection that starts sent the entries not
// known taken, from the first of them.
func (a *agent) replayAgentSessions() {
	a.mu.Lock()
	a.asSent = a.asTaken
	a.mu.Unlock()
	a.flushAgentSessions()
}

// flushAgentSessions sends, in order, the entries not yet sent on the
// current connection, one sender at a time, then pings the server and,
// answered on the same connection, holds them taken. A send that fails
// while the connection stays the same is tried again; one whose
// connection went leaves the rest for the next connection's start; nothing
// goes to a server that does not take it.
func (a *agent) flushAgentSessions() {
	a.mu.Lock()
	if a.asFlushing {
		a.mu.Unlock()
		return
	}
	a.asFlushing = true
	a.mu.Unlock()
	go func() {
		stop := func() {
			a.asFlushing = false
			a.mu.Unlock()
		}
		for {
			a.mu.Lock()
			w := a.out
			if !a.agentSessions.Load() || (w == nil && a.sendHook == nil) {
				stop()
				return
			}
			var r *agentSessionReport
			for i := range a.asLog {
				if a.asLog[i].seq > a.asSent {
					r = &a.asLog[i]
					break
				}
			}
			if r == nil {
				if a.asSent <= a.asTaken {
					stop()
					return
				}
				sent := a.asSent
				a.mu.Unlock()
				taken := a.confirmAgentSessions(w)
				a.mu.Lock()
				if !taken || a.out != w {
					// Not known taken: sent again as the next connection
					// starts, or after the next report.
					stop()
					return
				}
				a.asTaken = max(a.asTaken, sent)
				a.mu.Unlock()
				continue
			}
			m := proto.HostAgentSessionMsg{T: proto.HostAgentSession, SessionID: a.sessID, AgentSession: r.id, Turn: r.turn}
			seq := r.seq
			a.mu.Unlock()
			err := a.sendAgentSession(m)
			a.mu.Lock()
			switch {
			case err == nil:
				a.asSent = max(a.asSent, seq)
				a.mu.Unlock()
			case a.out == w && !errors.Is(err, errConnDown):
				// No turn in time on a connection that stays: again.
				a.mu.Unlock()
				a.log.Debug("an agent session id is sent again", "err", err)
				time.Sleep(10 * time.Millisecond)
			default:
				stop()
				return
			}
		}
	}()
}

// sendAgentSession hands m to the current connection, waiting for its turn
// as long as the connection lasts (tell); tests replace it (asSend).
func (a *agent) sendAgentSession(m proto.HostAgentSessionMsg) error {
	if a.asSend != nil {
		return a.asSend(m)
	}
	return a.tell(m, nil)
}

// confirmAgentSessions says whether the server answered a ping sent on w
// after what was sent on it: what it read before the ping it has taken.
// Tests replace it (asConfirm).
func (a *agent) confirmAgentSessions(w *controlWriter) bool {
	if a.asConfirm != nil {
		return a.asConfirm()
	}
	if w == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(w.ctx, a.heardWithin())
	defer cancel()
	return w.c.Ping(ctx) == nil
}

// dropReports stops listening on the host's socket, for a server that
// does not take agent_session: the agent's sender finds it gone and
// reports to the server's route, as under a host of an earlier release.
//
// In the one step that takes the socket, the host forgets the log and
// takes no id after it (asDropped): nothing logged before, and nothing a
// report still under way on the socket names, is sent after the agent's
// reports went to the server's route, which may record a newer id.
func (a *agent) dropReports() {
	a.mu.Lock()
	l := a.reports
	if l != nil {
		a.reports = nil
		a.asDropped = true
		a.asLog = nil
		a.asSent, a.asTaken = a.asSeq, a.asSeq
	}
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
