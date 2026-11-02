package brakeman

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Config is Brakeman's configuration, one document, brakeman/config.json,
// written only through the admin's route (docs/plans/brakeman.md, The
// configuration). It has no yolo field: a turn always runs in its
// adapter's read-only mode and never with the yolo recipe.
type Config struct {
	Enabled bool   `json:"enabled"`
	AgentID string `json:"agentId"` // a catalog agent; "" for none
	// Account is the folder the agent's login lives in, set in its
	// session as its adapter's ConfigDirEnv (CLAUDE_CONFIG_DIR,
	// CODEX_HOME): a reference, never read by Conductor beyond a stat.
	// "" is the agent's default login on this machine.
	Account string `json:"account,omitempty"`
	Model   string `json:"model,omitempty"` // "" is the agent's default
	Cwd     string `json:"cwd"`             // the folder its turns run in
	// AllowConductorTools lets the agent call Conductor's MCP tools
	// without asking, where its adapter can say so (Claude Code).
	AllowConductorTools bool        `json:"allowConductorTools"`
	Visibility          Visibility  `json:"visibility"`
	Scope               Scope       `json:"scope"`
	Permissions         Permissions `json:"permissions"`
	Wake                Wake        `json:"wake"`
	Budget              Budget      `json:"budget"`
	Rev                 int64       `json:"rev"`
	UpdatedAt           time.Time   `json:"updatedAt"`
}

// Visibility is what Brakeman may see, a switch a class.
type Visibility struct {
	Runs      bool `json:"runs"`      // runs, tasks, attempts, check receipts, messages, blockers
	Logs      bool `json:"logs"`      // check logs' text; without it a receipt carries its exit, subject and SHA-256
	Dispatch  bool `json:"dispatch"`  // projects, tickets, comments, history
	Sessions  bool `json:"sessions"`  // the sessions list: names, states, questions, quiet time
	Touched   bool `json:"touched"`   // the touched index
	Terminals bool `json:"terminals"` // a session's screen text; off by default
}

// The visibility classes, as a read log and a footprint name them.
const (
	ClassRuns      = "runs"
	ClassLogs      = "logs"
	ClassDispatch  = "dispatch"
	ClassSessions  = "sessions"
	ClassTouched   = "touched"
	ClassTerminals = "terminals"
)

// Classes is every visibility class, in the order Visibility has them.
var Classes = []string{ClassRuns, ClassLogs, ClassDispatch, ClassSessions, ClassTouched, ClassTerminals}

// Has reports whether the class is visible; an unknown class never is.
func (v Visibility) Has(class string) bool {
	switch class {
	case ClassRuns:
		return v.Runs
	case ClassLogs:
		return v.Logs
	case ClassDispatch:
		return v.Dispatch
	case ClassSessions:
		return v.Sessions
	case ClassTouched:
		return v.Touched
	case ClassTerminals:
		return v.Terminals
	}
	return false
}

// Scope narrows what Brakeman may see. A selector left empty is all; one
// given narrows, at most MaxSelector entries each. Admits combines them.
type Scope struct {
	Projects []string `json:"projects,omitempty"` // Dispatch project keys
	Repos    []string `json:"repos,omitempty"`    // absolute repository roots
	Crews    []string `json:"crews,omitempty"`    // crew ids
	Runs     []string `json:"runs,omitempty"`     // run ids
	Sessions []string `json:"sessions,omitempty"` // session ids
}

// Permissions is what Brakeman may do. Observe is true whenever Enabled
// is. Draft (draft_ticket/1, draft_crew/1, handoff/1) and Propose
// (proposal/1, decision/1) are the person's to turn off; both off is
// observe only, and a brief with an item they do not allow is refused.
// Act names the operations BK2 grants; it must be empty in this build.
type Permissions struct {
	Observe bool     `json:"observe"`
	Draft   bool     `json:"draft"`
	Propose bool     `json:"propose"`
	Act     []string `json:"act,omitempty"`
}

// Wake is when Brakeman runs: only when invoked, until BK3's background
// role.
type Wake struct {
	Mode string `json:"mode"`
}

// WakeInvoked is the one wake mode of this build.
const WakeInvoked = "invoked"

// Budget bounds Brakeman's turns.
type Budget struct {
	TurnMinutes  int `json:"turnMinutes"`  // 1 to 60; 15
	ReadsPerTurn int `json:"readsPerTurn"` // 10 to 2,000; 300
	TurnsPerDay  int `json:"turnsPerDay"`  // 1 to 500; 40, counted by the server's local day
}

// The configuration's bounds and defaults.
const (
	MaxSelector     = 50   // entries in one scope selector
	MaxPath         = 4096 // the account, the folder, a repository root
	MaxScopeBytes   = 8 << 10
	MinTurnMinutes  = 1
	MaxTurnMinutes  = 60
	MinReadsPerTurn = 10
	MaxReadsPerTurn = 2000
	MinTurnsPerDay  = 1
	MaxTurnsPerDay  = 500

	DefaultTurnMinutes  = 15
	DefaultReadsPerTurn = 300
	DefaultTurnsPerDay  = 40
)

// modelPattern is a model's shape: an argument of the adapter's own flag,
// never free arguments.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$`)

// agentIDPattern is a catalog agent's id (catalog's idPattern).
var agentIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// crewIDPattern is a crew's id (crew.ValidID).
var crewIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// projectKeyPattern is a Dispatch project's key (dispatch.KeyPattern).
var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}$`)

// DefaultConfig is the configuration before anyone saved one: off, no
// agent, the server's default folder, every visibility on but terminals,
// all of scope, observe, draft and propose, the default budget, invoked.
func DefaultConfig(cwd string) Config {
	return Config{
		Cwd:         cwd,
		Visibility:  Visibility{Runs: true, Logs: true, Dispatch: true, Sessions: true, Touched: true},
		Permissions: Permissions{Observe: true, Draft: true, Propose: true},
		Wake:        Wake{Mode: WakeInvoked},
		Budget:      Budget{TurnMinutes: DefaultTurnMinutes, ReadsPerTurn: DefaultReadsPerTurn, TurnsPerDay: DefaultTurnsPerDay},
	}
}

// Facts answers what internal/brakeman cannot know itself: the catalog's
// agents and their adapters, the folders on this machine, and a crew's
// body as the crew store checks it. internal/api implements it.
type Facts interface {
	// Agent answers what the catalog and the agent's adapter say of the
	// agent id; ok false when the catalog has no such agent.
	Agent(id string) (a AgentFacts, ok bool)
	// CheckAccount checks an account folder: a directory the server's
	// user owns. The path is absolute and bounded already.
	CheckAccount(dir string) error
	// CheckCwd checks the folder turns run in as a session's is checked
	// (resolveCwd). The path is absolute and bounded already.
	CheckCwd(dir string) error
	// CheckCrew checks a draft crew's body as POST /api/crews does (the
	// crew's strict decoding and its Validate), so this package needs no
	// internal/crew.
	CheckCrew(body json.RawMessage) error
}

// AgentFacts is what Facts says of an agent.
type AgentFacts struct {
	Hidden   bool // the catalog hides it
	MCP      bool // its adapter registers Conductor's MCP server
	ReadOnly bool // its adapter has a read-only mode (Adapter.ReadOnlyArgs)
	// ReadOnlyGap is what that mode does not hold, in words (the
	// adapter's ReadOnlyGap); "" when it holds. An agent whose mode does
	// not hold is refused as Brakeman's.
	ReadOnlyGap string
	Model       bool // its adapter takes a model (Adapter.ModelArgs)
	// AllowTools: its adapter can let one MCP server's tools run without
	// asking (Adapter.AllowTools).
	AllowTools bool
}

// Invalid is a value refused for one field: its name and why, with the
// code a route answers (CodeInvalidConfig, CodeInvalidBrief,
// CodeInvalidRequest).
type Invalid struct {
	Code  string
	Field string
	Why   string
}

func (e *Invalid) Error() string { return e.Field + ": " + e.Why }

// The codes of an Invalid.
const (
	CodeInvalidConfig  = "invalid_config"
	CodeInvalidBrief   = "invalid_brief"
	CodeInvalidRequest = "invalid_request"
)

func invalidConfig(field, why string, a ...any) error {
	return &Invalid{Code: CodeInvalidConfig, Field: field, Why: fmt.Sprintf(why, a...)}
}

// Validate holds every field of c to its bound and asks f what the
// package cannot know: the agent exists, is not hidden, has an MCP
// registration and a read-only mode, and takes a model only when its
// adapter does; the account and the folder are what the server allows. It
// leaves Rev and UpdatedAt to the save. The error is an *Invalid.
func Validate(c Config, f Facts) error {
	if c.AgentID != "" {
		if !agentIDPattern.MatchString(c.AgentID) {
			return invalidConfig("agentId", "must match %s", agentIDPattern)
		}
		a, ok := f.Agent(c.AgentID)
		switch {
		case !ok:
			return invalidConfig("agentId", "no agent %s in the catalog", c.AgentID)
		case a.Hidden:
			return invalidConfig("agentId", "the agent %s is hidden", c.AgentID)
		case !a.MCP:
			return invalidConfig("agentId", "the agent %s does not register Conductor's MCP server, so it could not read its request", c.AgentID)
		case !a.ReadOnly:
			return invalidConfig("agentId", "the agent %s has no read-only mode Conductor knows, and a turn never runs with yolo", c.AgentID)
		case a.ReadOnlyGap != "":
			// The one place a mode that does not hold refuses the agent:
			// a warning instead would drop this case and show the gap.
			return invalidConfig("agentId", "the agent %s cannot be Brakeman's: its read-only mode does not hold: %s", c.AgentID, a.ReadOnlyGap)
		case c.Model != "" && !a.Model:
			return invalidConfig("model", "the agent %s takes no model from Conductor: leave it empty for its default", c.AgentID)
		case c.AllowConductorTools && !a.AllowTools:
			return invalidConfig("allowConductorTools", "the agent %s cannot be told to allow Conductor's tools: it asks before each", c.AgentID)
		}
	} else {
		switch {
		case c.Enabled:
			return invalidConfig("agentId", "Brakeman on needs an agent")
		case c.Model != "":
			return invalidConfig("model", "a model needs an agent")
		}
	}
	if c.Model != "" && !modelPattern.MatchString(c.Model) {
		return invalidConfig("model", "must match %s", modelPattern)
	}
	if c.Account != "" {
		if err := absPath("account", c.Account); err != nil {
			return err
		}
		if err := f.CheckAccount(c.Account); err != nil {
			return invalidConfig("account", "%v", err)
		}
	}
	if c.Cwd == "" {
		if c.Enabled {
			return invalidConfig("cwd", "Brakeman on needs the folder its turns run in")
		}
	} else {
		if err := absPath("cwd", c.Cwd); err != nil {
			return err
		}
		if err := f.CheckCwd(c.Cwd); err != nil {
			return invalidConfig("cwd", "%v", err)
		}
	}
	if err := c.Scope.validate(); err != nil {
		return err
	}
	p := c.Permissions
	if c.Enabled && !p.Observe {
		return invalidConfig("permissions.observe", "is true while Brakeman is on")
	}
	if len(p.Act) > 0 {
		return invalidConfig("permissions.act", "no action can be granted in this build: it must be empty")
	}
	if c.Wake.Mode != WakeInvoked {
		return invalidConfig("wake.mode", "must be %q in this build", WakeInvoked)
	}
	b := c.Budget
	switch {
	case b.TurnMinutes < MinTurnMinutes || b.TurnMinutes > MaxTurnMinutes:
		return invalidConfig("budget.turnMinutes", "must be between %d and %d", MinTurnMinutes, MaxTurnMinutes)
	case b.ReadsPerTurn < MinReadsPerTurn || b.ReadsPerTurn > MaxReadsPerTurn:
		return invalidConfig("budget.readsPerTurn", "must be between %d and %d", MinReadsPerTurn, MaxReadsPerTurn)
	case b.TurnsPerDay < MinTurnsPerDay || b.TurnsPerDay > MaxTurnsPerDay:
		return invalidConfig("budget.turnsPerDay", "must be between %d and %d", MinTurnsPerDay, MaxTurnsPerDay)
	}
	return nil
}

// absPath checks a folder's path as the package can: absolute, clean, at
// most MaxPath bytes, UTF-8 with no control character.
func absPath(field, p string) error {
	switch {
	case len(p) > MaxPath:
		return invalidConfig(field, "at most %d bytes", MaxPath)
	case !utf8.ValidString(p) || strings.ContainsFunc(p, isControl):
		return invalidConfig(field, "UTF-8 with no control character")
	case !filepath.IsAbs(p):
		return invalidConfig(field, "must be an absolute path")
	case filepath.Clean(p) != p:
		return invalidConfig(field, "must be a clean path (%s)", filepath.Clean(p))
	}
	return nil
}

// validate holds each selector to its bound and its entries to their
// shape, and the whole to MaxScopeBytes as JSON, so a request's snapshot
// of it stays small.
func (s Scope) validate() error {
	check := func(field string, list []string, ok func(string) error) error {
		if len(list) > MaxSelector {
			return invalidConfig(field, "at most %d entries", MaxSelector)
		}
		for i, v := range list {
			if err := ok(v); err != nil {
				return invalidConfig(fmt.Sprintf("%s[%d]", field, i), "%v", err)
			}
			if slices.Contains(list[:i], v) {
				return invalidConfig(fmt.Sprintf("%s[%d]", field, i), "%s is named twice", v)
			}
		}
		return nil
	}
	pattern := func(p *regexp.Regexp) func(string) error {
		return func(v string) error {
			if !p.MatchString(v) {
				return fmt.Errorf("must match %s", p)
			}
			return nil
		}
	}
	repo := func(v string) error {
		if err := absPath("repo", v); err != nil {
			return fmt.Errorf("%s", err.(*Invalid).Why)
		}
		return nil
	}
	for _, sel := range []struct {
		field string
		list  []string
		ok    func(string) error
	}{
		{"scope.projects", s.Projects, pattern(projectKeyPattern)},
		{"scope.repos", s.Repos, repo},
		{"scope.crews", s.Crews, pattern(crewIDPattern)},
		{"scope.runs", s.Runs, pattern(runIDPattern)},
		{"scope.sessions", s.Sessions, pattern(hexIDPattern)},
	} {
		if err := check(sel.field, sel.list, sel.ok); err != nil {
			return err
		}
	}
	if b, _ := json.Marshal(s); len(b) > MaxScopeBytes {
		return invalidConfig("scope", "at most %d bytes as JSON", MaxScopeBytes)
	}
	return nil
}

// The kinds of a ScopeItem.
const (
	ItemRun     = "run"     // a run, and what is under it: tasks, attempts, checks, commands, events, messages, commits
	ItemSession = "session" // a session, and a touched-index entry by its session
	ItemTicket  = "ticket"  // a ticket, a comment, a Dispatch receipt
)

// ScopeItem is what Admits is asked about: an item's kind and what the
// selectors read of it.
type ScopeItem struct {
	Kind string
	// Run is a run's id; for a session, the run it is a member of ("" for
	// a session in no run).
	Run string
	// Crew is a run's crew; for a member session, its run's.
	Crew string
	// Repo is the repository root of the run's or the session's folder,
	// "" for one in no repository.
	Repo string
	// Project is the project of the ticket a run is linked to (for a
	// member session, its run's; "" for none); a ticket's own project.
	Project string
	// Session is a session's id.
	Session string
	// Links are the runs a ticket is linked to.
	Links []RunLink
}

// RunLink is a run linked to a ticket.
type RunLink struct {
	Run  string
	Crew string
}

// in reports whether a selector takes v: an empty one takes everything.
func in(sel []string, v string) bool { return len(sel) == 0 || slices.Contains(sel, v) }

// Admits reports whether the item is in scope: it matches every non-empty
// selector that applies to its kind (docs/plans/brakeman.md, The
// configuration). A run by Crews, Runs, Repos and Projects (a run linked
// to no ticket is outside a non-empty Projects). A session by Sessions,
// Repos and, for a run's member, its run's selectors; a session in no run
// is outside a non-empty Crews, Runs or Projects unless Sessions names it.
// A ticket by Projects, and by Runs and Crews through a run linked to it
// (one linked to none is outside either when it is non-empty). Evidence
// is narrowed by the visibility apart from this.
func (s Scope) Admits(it ScopeItem) bool {
	switch it.Kind {
	case ItemRun:
		return s.admitsRun(it.Run, it.Crew, it.Repo, it.Project)
	case ItemSession:
		if !in(s.Repos, it.Repo) {
			return false
		}
		if it.Run != "" {
			return in(s.Sessions, it.Session) && in(s.Crews, it.Crew) && in(s.Runs, it.Run) && in(s.Projects, it.Project)
		}
		if len(s.Sessions) > 0 {
			return slices.Contains(s.Sessions, it.Session)
		}
		return len(s.Crews) == 0 && len(s.Runs) == 0 && len(s.Projects) == 0
	case ItemTicket:
		if !in(s.Projects, it.Project) {
			return false
		}
		if len(s.Runs) == 0 && len(s.Crews) == 0 {
			return true
		}
		for _, l := range it.Links {
			if in(s.Runs, l.Run) && in(s.Crews, l.Crew) {
				return true
			}
		}
		return false
	}
	return false
}

func (s Scope) admitsRun(run, crew, repo, project string) bool {
	return in(s.Crews, crew) && in(s.Runs, run) && in(s.Repos, repo) && in(s.Projects, project)
}

// Covers reports whether every item earlier admits, s admits too: what an
// earlier brief made under earlier may be given whole under s. It is
// decided clause by clause of Admits. Each conjunctive clause (Projects,
// Repos, Crews, Runs, and Sessions as it narrows a run's members) is
// covered when s's selector is empty, or earlier's is non-empty and a
// subset of it. The standalone-session clause is not conjunctive, since
// Sessions widens there as well as narrows: it is covered when earlier
// admitted no standalone session (its Sessions empty and one of Crews,
// Runs and Projects not), or earlier's Sessions is non-empty and a subset
// of s's, or s admits every standalone session (all four empty).
func (s Scope) Covers(earlier Scope) bool {
	conj := func(now, then []string) bool {
		if len(now) == 0 {
			return true
		}
		if len(then) == 0 {
			return false
		}
		for _, v := range then {
			if !slices.Contains(now, v) {
				return false
			}
		}
		return true
	}
	if !conj(s.Projects, earlier.Projects) || !conj(s.Repos, earlier.Repos) || !conj(s.Crews, earlier.Crews) || !conj(s.Runs, earlier.Runs) || !conj(s.Sessions, earlier.Sessions) {
		return false
	}
	noStandalone := len(earlier.Sessions) == 0 && (len(earlier.Crews) > 0 || len(earlier.Runs) > 0 || len(earlier.Projects) > 0)
	everyStandalone := len(s.Sessions) == 0 && len(s.Crews) == 0 && len(s.Runs) == 0 && len(s.Projects) == 0
	named := len(earlier.Sessions) > 0 && len(s.Sessions) > 0 && conj(s.Sessions, earlier.Sessions)
	return noStandalone || everyStandalone || named
}
