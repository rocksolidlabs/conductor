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
// group it under the run from then on. It reports whether it did: a
// session whose crew reference carries a comparison's lane (CrewRef.Lane,
// given at its launch) keeps that reference, and SetCrew sets nothing.
func (s *Local) SetCrew(ref CrewRef) bool {
	s.mu.Lock()
	if cur := s.info.Crew; cur != nil && cur.Lane != nil {
		s.mu.Unlock()
		return false
	}
	var could []*Subscription
	s.hub.Each(func(sub *Subscription) {
		if s.operatorChatLocked(sub) {
			could = append(could, sub)
		}
	})
	r := ref
	s.info.Crew = &r
	if p := s.way.Load(); p != nil {
		// A chat whose standing changed: every connection is told whether
		// its typed line may still go for the agent to expand (a crew
		// member's may not: operatorChatLocked), so a page never holds a
		// word the session no longer honours; and one that was sent the
		// agent's commands and may be no more is sent an empty list, so
		// its page keeps none it is no longer kept up to date with.
		s.standingChangedLocked()
		s.commandsWithdrawnLocked(p, could)
	}
	s.mu.Unlock()
	s.notifyChange()
	return true
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
	prev := s.info.AgentSession
	next, changed := NextAgentSession(prev, id, policy, turn)
	if changed {
		s.info.AgentSession = &next
	}
	// A chat whose agent took up another conversation (its /clear: the
	// agent's own hook or stream names a new id) says so in the transcript,
	// where the person reads what the agent still knows: what is above it no
	// longer is. Only an id the policy took in place of the one held.
	var out applied
	cleared := false
	if p := s.way.Load(); p != nil && changed && prev != nil && prev.ID != "" && next.ID != prev.ID {
		s.noticeLocked(p, noticeCleared, ClearedWords, &out)
		cleared = true
	}
	s.mu.Unlock()
	if cleared {
		s.sendApplied(out)
	}
	if changed {
		s.notifyChange()
		// A noted move waits for a conversation it can resume (move.go).
		s.tryNoted()
	}
}

// NextAgentSession is the agent session a report of id makes of cur (nil
// for none), by policy: id when there is none, else the latest one or the
// lowest one; turn marks it resumable. changed says it differs from cur.
// A server session's own (ReportAgentSession) and a hosted session's
// record's, which the server keeps apart from the hosted Info.
func NextAgentSession(cur *AgentSession, id, policy string, turn bool) (next AgentSession, changed bool) {
	next = AgentSession{ID: id, Source: AgentSessionHook}
	switch {
	case cur == nil:
	case cur.ID == id:
		next = *cur
	case policy == PolicyLowest && strings.Compare(id, cur.ID) > 0:
		next = *cur
	}
	next.Resumable = next.Resumable || (turn && next.ID == id)
	return next, cur == nil || *cur != next
}

// Launched is what a server session was launched with, kept for Resume: the
// agent, the name, the working directory, the arguments and variables of the
// launch, its yolo choice, the login it is bound to and its model.
type Launched struct {
	AgentID, Name, Cwd string
	Args               []string
	Env                map[string]string
	Yolo               bool
	// Renamed says Name is one a person gave the session (Rename), or the
	// session it resumes: a crew member's resume starts under it, where it
	// starts under the member's own name otherwise (api.Server.Launch).
	Renamed bool
	// Account is the login the launch was bound to, and Model the model it
	// was given ("" for the agent's default): a resume starts under both.
	// Adapter is the adapter the process was started as, whatever its
	// catalog entry says since.
	Account BoundAccount
	Model   string
	Adapter string
	// Holds are the ids of the registered accounts its launch's
	// reservation took, which it holds until its process has ended, and
	// Counted the one account it is counted on, decided with them at the
	// reservation: kept in memory alone, never recorded or answered.
	Holds   []string
	Counted string
}

// Where a bound login comes from (BoundAccount.Source).
const (
	SourceOwn       = "own"        // the tool's own login: nothing sets its folder or a login variable
	SourceAccount   = "account"    // a registered account: its folder
	SourcePinned    = "pinned"     // the agent's entry (or the server's passthrough) sets the login
	SourceUnlisted  = "unlisted"   // Brakeman's folder, held by no registered account
	SourceLaunchEnv = "launch_env" // the launch's own additions set the login
)

// BoundAccount is the login a launch was bound to: the account's id
// ("default" for the agent's own, "unlisted" for a folder no account
// holds), its label, the folder its adapter's folder variable is set to
// ("" for none), where the login comes from (Source), and Login, a
// fingerprint of every variable that says where the process's login comes
// from, as its environment held them. Folder and Login are kept for the
// launches made again from it and go in no reply.
type BoundAccount struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	Folder string `json:"folder,omitempty"`
	Source string `json:"source"`
	Login  string `json:"login"`
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
