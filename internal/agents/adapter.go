// Package agents knows how each supported coding agent reports to Conductor:
// the hook files Conductor generates for it, the flags and environment that
// wire them in at launch, and how to install them into the agent's own
// configuration when an admin asks. The hooks run `conductor notify`, whose
// payload mappers live in internal/notify.
package agents

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// Adapter knows how one agent reports to Conductor.
type Adapter struct {
	ID, Name string
	// Assets written under hooksDir at startup: relative path → content. {{BIN}} is
	// replaced by the absolute conductor binary path.
	Assets map[string]string
	// Inject returns extra argv and env for a launch, given the hooks dir. Empty
	// when the agent has no launch-time route.
	Inject func(hooksDir string, sig catalog.Signal) (argv []string, env map[string]string)
	// YoloInject, when set, takes Inject's place for a launch with the
	// agent's yolo recipe applied, whatever the signal: Claude Code honours
	// only the last --settings, so the key that skips its bypass-permissions
	// warning rides in the hooks settings file, or in a file of its own when
	// the hooks are not wired.
	YoloInject func(hooksDir string, sig catalog.Signal) (argv []string, env map[string]string)
	// TrustArgs, when set, returns the arguments that trust dirs for one
	// launch without writing the agent's configuration (Codex's -c projects
	// override). A launch adds them only with the yolo recipe applied.
	TrustArgs func(dirs []string) []string
	// ConfirmsSubmit says the agent's hooks report working as it takes a
	// prompt (Claude Code's UserPromptSubmit): with the hook signal, a role
	// prompt not taken within session.ConfirmWait gets one more Enter.
	ConfirmsSubmit bool
	// Install merges Conductor's hooks into the agent's own config under home
	// and, for an agent that reads skills, copies the Conductor skill there.
	// Idempotent. Returns the files it touched, none when nothing changed;
	// what it leaves to the user comes back as an error wrapping ErrByHand.
	// nil when the agent has no file route.
	Install func(home, hooksDir string) ([]string, error)
	// SkillPath is where the agent reads the Conductor skill under home, the
	// SKILL.md itself, slash-separated, for an agent that reads skills: its
	// Install copies the skill there, and so does a launch (InstallSkill).
	// Empty for an agent that reads none (aider).
	SkillPath string
	// Status is a dry run of Install on home: installed is true when Install
	// would change nothing and leave nothing to do by hand, so an install
	// that is partial or names another binary reads as not installed; where
	// is the install's main file. nil when there is nothing Install can put
	// in place (aider, dsh).
	Status func(home string) (installed bool, where string)
	// Snippet is what a host user pastes when Install is not available to them.
	Snippet func(hooksDir string) string
	// Events documents what this adapter can report (for the Events page).
	Events []string // e.g. "needs_input", "done", "tool_use"
	// Experimental marks an adapter for an agent whose hook interface is still
	// changing (a developer preview); the Events page says so.
	Experimental bool
	// Probe says how the agent identifies itself (its version flag and
	// output); nil when Conductor does not know it yet.
	Probe *Probe
	// MCP returns the arguments that register Conductor's MCP server
	// (conductor mcp) with the agent for one launch, given the hooks dir;
	// nil for an agent Conductor cannot register it with at launch.
	MCP func(hooksDir string) []string
	// PlansDir, when set, is where the agent keeps the plans it writes
	// outside a project (#289), given the environment its process runs with
	// (getenv): Claude Code's `plans` under CLAUDE_CONFIG_DIR, else under
	// ~/.claude. "" when no home is known; a relative directory is taken
	// from the session's working directory (PlansDir). nil for an agent
	// that keeps none Conductor knows of.
	PlansDir func(getenv func(string) string) string
	// ConfigDirEnv names the environment variable that moves the agent's
	// own configuration directory, its directory under home, elsewhere
	// (Claude Code's CLAUDE_CONFIG_DIR for ~/.claude, Codex's CODEX_HOME for
	// ~/.codex): an agent launched with it set reads its skills there, so a
	// launch puts the Conductor skill there too (InstallSkillIn; a copy of
	// an agent under a configuration of its own, #291). Empty for an agent
	// without one.
	ConfigDirEnv string
	// ReadOnlyArgs, when set, returns the arguments that hold the agent to
	// reading for one launch, through its own read-only mode: its file
	// reads and Conductor's MCP tools left, nothing that writes or runs a
	// command (Claude Code's tool rules, Codex's read-only sandbox). The
	// adapter's definition says what its mode does not hold (Codex's: the
	// person's own MCP servers and exec-policy allow rules). A Brakeman
	// turn launches with them; an agent without them cannot be Brakeman's.
	// nil for an agent Conductor knows no read-only mode of.
	ReadOnlyArgs func() []string
	// ReadOnlyGap says, in words for Settings, what the agent's read-only
	// mode does not hold: "" when it holds (Claude Code's, with
	// --strict-mcp-config). A Brakeman turn needs a mode that holds, so an
	// agent with a gap is refused as Brakeman's (brakeman.Validate, through
	// the API's Facts); this one fact is where that refusal starts, so
	// turning it into a warning in Settings is a change of one line there.
	ReadOnlyGap string
	// ModelArgs, when set, returns the arguments that choose model for one
	// launch, through the agent's own flag, the model its value and never an
	// argument of its own; "" is the agent's default, and gives none. nil for
	// an agent whose model flag Conductor has not checked: no model can be
	// set for it.
	ModelArgs func(model string) []string
	// AllowTools, when set, returns the arguments that let the agent call
	// every tool of the MCP server named server without asking for each,
	// for one launch (Claude Code's allow rule, Codex's approval mode for
	// one server); nil for a name that is not a plain word. nil for an
	// agent that asks before each tool.
	AllowTools func(server string) []string
}

// mcpServerName is what an MCP server's name must be for AllowTools to
// write it into a rule: a plain word, so it cannot carry a rule, a TOML key
// or a flag of its own.
var mcpServerName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// InstallsSkill reports whether the agent reads skills: its Install, and a
// launch, bring the Conductor skill (SkillPath).
func (a Adapter) InstallsSkill() bool { return a.SkillPath != "" }

// ErrByHand wraps what Install leaves to the user: a file it would have to
// overwrite or cannot merge into, or an agent it does not edit at all. The
// message says what to do instead, and the files Install did change are still
// returned with it.
var ErrByHand = errors.New("install by hand")

func byHand(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrByHand, fmt.Sprintf(format, args...))
}

// stepError is what a step left to the user (ErrByHand) says, with the file
// it is about. Its message is the step's.
type stepError struct {
	rel string
	err error
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

// SnippetNeeded reports whether what Install left to the user (err) calls for
// the adapter's snippet. It does unless every step left is the Conductor
// skill's, whose message says what to do (conductor skill prints it); the
// snippet is for the agent's hooks.
func SnippetNeeded(err error) bool {
	return errors.Is(err, ErrByHand) && !onlySkillSteps(err)
}

func onlySkillSteps(err error) bool {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			if !onlySkillSteps(e) {
				return false
			}
		}
		return true
	}
	var se *stepError
	return errors.As(err, &se) && (se.rel == claudeSkill || se.rel == codexSkill || se.rel == agentsSkill)
}
