package session

import (
	"regexp"
	"strings"
)

// An agent's own session (its conversation) as Conductor knows it, for
// Resume: an id Conductor chose at launch, one the agent reported through its
// hooks, or the one a resumed session took over.

// Where an agent session's id came from (AgentSession.Source).
const (
	AgentSessionSet     = "set"     // Conductor chose it at launch
	AgentSessionHook    = "hook"    // the agent reported it
	AgentSessionResumed = "resumed" // this session resumed it
)

// AgentSession is the agent's own session in a Conductor session.
type AgentSession struct {
	ID string `json:"id"`
	// Resumable is set once the agent has reported a turn (or the session
	// resumed one): an agent never prompted keeps nothing to resume (Claude
	// Code writes no transcript until its first request).
	Resumable bool   `json:"resumable"`
	Source    string `json:"source"`
}

// MaxAgentSessionID bounds an agent session id, in bytes.
const MaxAgentSessionID = 128

// agentSessionShape is what every agent session id looks like, whatever its
// agent's own pattern says: a letter or digit first, so that an id can never
// read as a flag, then letters, digits and . _ : -.
var agentSessionShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidAgentSessionID reports whether id has the shape of an agent session
// id and matches the agent's pattern (catalog.SessionRecipe.IDPattern).
func ValidAgentSessionID(id string, pattern *regexp.Regexp) bool {
	return len(id) <= MaxAgentSessionID && agentSessionShape.MatchString(id) && pattern != nil && pattern.MatchString(id)
}

// Agent session id policies (catalog.SessionRecipe.IDPolicy).
const (
	PolicyLatest = "latest" // the newest id reported is the session's
	PolicyLowest = "lowest" // the smallest: Codex's main thread, not its title thread
)

// SetCrew tags the session with the run it was adopted into (an agent that
// formed a crew around its own session, docs/protocol.md): the listings
// group it under the run from then on.
func (s *Local) SetCrew(ref CrewRef) {
	s.mu.Lock()
	r := ref
	s.info.Crew = &r
	s.mu.Unlock()
	s.notifyChange()
}

// SetAgentSession records the agent session a launch chose or resumed.
func (s *Local) SetAgentSession(as AgentSession) {
	s.mu.Lock()
	s.info.AgentSession = &as
	s.mu.Unlock()
	s.notifyChange()
}

// ReportAgentSession takes the id an agent's report carries, already checked
// (ValidAgentSessionID): the session's when it has none, or by policy (the
// latest one; the lowest one); turn marks the session resumable. A change
// notifies OnChange.
func (s *Local) ReportAgentSession(id, policy string, turn bool) {
	s.mu.Lock()
	cur := s.info.AgentSession
	next := AgentSession{ID: id, Source: AgentSessionHook}
	switch {
	case cur == nil:
	case cur.ID == id:
		next = *cur
	case policy == PolicyLowest && strings.Compare(id, cur.ID) > 0:
		next = *cur
	}
	next.Resumable = next.Resumable || (turn && next.ID == id)
	changed := cur == nil || *cur != next
	if changed {
		s.info.AgentSession = &next
	}
	s.mu.Unlock()
	if changed {
		s.notifyChange()
	}
}

// Launched is what a server session was launched with, kept for Resume: the
// agent, the name, the working directory, the arguments and variables of the
// launch, and its yolo choice.
type Launched struct {
	AgentID, Name, Cwd string
	Args               []string
	Env                map[string]string
	Yolo               bool
	// Renamed says Name is one a person gave the session (Rename), or the
	// session it resumes: a crew member's resume starts under it, where it
	// starts under the member's own name otherwise (api.Server.Launch).
	Renamed bool
}

// Launched returns what the session was launched with (Options.Launched),
// under the name a person renamed it to, once they have (Rename): what a
// resume of it starts under.
func (s *Local) Launched() Launched {
	l := s.opts.Launched
	s.mu.Lock()
	if s.renamed {
		l.Name, l.Renamed = s.info.Name, true
	}
	s.mu.Unlock()
	l.Args = append([]string(nil), l.Args...)
	env := make(map[string]string, len(l.Env))
	for k, v := range l.Env {
		env[k] = v
	}
	l.Env = env
	return l
}
