package crew

import (
	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// refusedKey is a session and one of Conductor's tools whose call the
// session's agent refused.
type refusedKey struct{ session, tool string }

// refusedCall takes a member's tool_denied entry: its agent's own
// permission check refused a call (Claude Code's PermissionDenied hook,
// which runs when auto mode's classifier denies one). A refusal of one of
// Conductor's own tools is said in the run's log, once for the session and
// the tool, with the rule that allows the call and where it is set: a
// member whose task_result was refused otherwise leaves the run waiting
// with only "reports done without a result" (#1143). Any other tool's
// refusal is the agent's own business and says nothing. It takes e.mu.
func (e *Engine) refusedCall(sessionID string, sm sessionMember, tool string) {
	name, ok := agents.ConductorToolOf(tool)
	if !ok {
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
	r.note(session.ActivityStatus, "%s%s's call to Conductor's %s was refused by its agent's own permission check; allow %s in its permissions (in Claude Code, /permissions) for it to call again",
		where, sm.m.def.Name, name, tool)
}
