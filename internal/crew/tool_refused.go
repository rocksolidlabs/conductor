package crew

import (
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// refusedKey is a session and one of Conductor's tools whose call the
// session's agent refused.
type refusedKey struct{ session, tool string }

// refusedCall takes a member's tool_denied entry: its agent refused a
// call, which a terminal's Claude Code tells through its PermissionDenied
// hook. The run's log says it once for the session and the tool (#1143)
// when the tool is exactly one of those the member's launch allows
// Conductor's server without asking (agents.ConductorToolOf: at most
// len(ProtocolUnaskedTools) lines and keys a session, the tool's name in
// the line one Conductor wrote), the launch carries that allowance with
// Conductor's own server (agents.AllowsConductorTools: a launch without
// the server, or a Brakeman turn, carries none, and a server keyed
// conductor there is not Conductor's), and the member runs in its
// terminal: a chat's own stream records its refusals too (a deny rule's
// among them), and a chat's floor and its permission questions are
// Conductor's own, so nothing there needs this line. A member whose
// task_result was refused would otherwise leave the run waiting with only
// "reports done without a result". Any other refusal says nothing and
// keeps nothing. The session's way and command are read as the entry
// arrives, on the recording goroutine, before the change the turn's end
// may bring. It takes e.mu.
func (e *Engine) refusedCall(sessionID string, sm sessionMember, tool string) {
	name, ok := agents.ConductorToolOf(tool)
	if !ok {
		return
	}
	l, ok := e.lookup(sessionID)
	if !ok || l.Way() != session.WayTerminal || !agents.AllowsConductorTools(l.Info().Command) {
		return
	}
	r := sm.r
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runs[r.id] != r { // forgotten meanwhile
		return
	}
	key := refusedKey{sessionID, name}
	if r.refusedSaid[key] {
		return
	}
	if r.refusedSaid == nil {
		r.refusedSaid = map[refusedKey]bool{}
	}
	r.refusedSaid[key] = true
	where := ""
	if sm.a != nil {
		where = "task " + sm.a.t.def.ID + ": "
	}
	r.note(session.ActivityStatus, "%s%s's call to Conductor's %s was refused by its agent's own permission check or a rule in its settings, though Conductor's launch allows it; look in its terminal (in Claude Code, /permissions)",
		where, sm.m.def.Name, name)
}
