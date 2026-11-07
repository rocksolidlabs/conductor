// Package catalog defines the launchable agent commands. Commands are argv
// arrays; nothing in the catalog is ever passed through a shell.
package catalog

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// Agent describes one launchable command.
type Agent struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Command     []string          `json:"command"`
	AllowArgs   bool              `json:"allowArgs"`
	Env         map[string]string `json:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	// Site is the agent's website, which the Agents page links to beside an
	// agent that is not installed on the server: an https URL, or empty.
	Site string `json:"site,omitempty"`
	// EnvPassthrough names server environment variables this agent's
	// sessions may inherit, in addition to the server-wide envPassthrough.
	EnvPassthrough []string `json:"envPassthrough,omitempty"`
	// Adapter names the hook adapter for this agent; empty means none.
	Adapter string `json:"adapter,omitempty"`
	// Signal says how the agent reports that it needs input; nil means the
	// bell default (see EffectiveSignal).
	Signal *Signal `json:"signal,omitempty"`
	// Yolo is the agent's yolo recipe: what a launch with yolo on adds to
	// skip the agent's permission prompts. nil in a saved override takes the
	// replaced agent's; an empty recipe ({}) has none, so yolo does nothing
	// for the agent.
	Yolo *Yolo `json:"yolo,omitempty"`
	// TrustPrompt matches the agent's workspace-trust question in the text of
	// its screen (escape sequences read as spaces): while it shows, a crew
	// run types no prompt and the session needs a person's answer. RE2, at
	// most 200 bytes, never matching an empty text; empty in a saved
	// override takes the replaced agent's.
	TrustPrompt string `json:"trustPrompt,omitempty"`
	// PromptShape matches the agent's own prompt at rest, by its shape, on
	// its whole screen (rows joined by line breaks, a row autowrap went on
	// from read with the next as one): a Brakeman turn's line, and its
	// Enter, are typed only while the screen matches, never into anything
	// else (#714). As TrustPrompt; empty, the line is kept from a screen
	// that shows a dialog's numbered choice alone, and empty in a saved
	// override takes the replaced agent's.
	PromptShape string `json:"promptShape,omitempty"`
	// TrustAnswers are the answers to that question as the choices a page
	// offers for it (the session page's reply bar, the sidebar's row, the
	// chat): each a label and the keys that pick it, the trusting answer
	// first. The keys matter: Claude Code highlights "No, exit" first, so a
	// plain Enter there ends the session. Without answers the question has
	// no choices and only the terminal answers it. At most 6; a label of at
	// most 60 characters, keys of 1 to 48 bytes. nil in a saved override
	// takes the replaced agent's.
	TrustAnswers []Answer `json:"trustAnswers,omitempty"`
	// Questions are the agent's other startup questions, held like the
	// trust question (round 13, G2c): while one shows, a crew run types no
	// prompt, typed text is refused and its answers are the choices. Codex
	// asks "Hooks need review" whenever its hooks are new or changed. Each a
	// prompt (as TrustPrompt), its words, its answers (as TrustAnswers) and
	// whether it is asked at any time (Anytime: Codex's command approval);
	// at most 8. nil in a saved override takes the replaced agent's.
	Questions []Question `json:"questions,omitempty"`
	// Session is the agent's session recipe: how Conductor names the agent's
	// own session and resumes it. nil in a saved override takes the replaced
	// agent's; an empty recipe ({}) has none, so Resume relaunches plainly.
	Session *SessionRecipe `json:"session,omitempty"`
	// Probe, when false, turns the identity probe off for this agent (the
	// server runs its adapter's version check otherwise). nil in a saved
	// override takes the replaced agent's.
	Probe *bool `json:"probe,omitempty"`
	// Browser is the browser the agent's links open in (#436): what it
	// opens with conductor open, the MCP tool open_url or its own opener
	// ($BROWSER, set to Conductor's when the agent has none of its own).
	// nil is the computer's default browser; nil in a saved override takes
	// the replaced agent's, and {} is the default over it.
	Browser *Browser `json:"browser,omitempty"`
}

// Probed reports whether the identity probe runs for the agent: unless Probe
// says false.
func (a Agent) Probed() bool { return a.Probe == nil || *a.Probe }

// Yolo is an agent's yolo recipe: Args go after the agent's command and the
// launch's own arguments, Env over the agent's own environment, through the
// same filtered environment (never as Conductor's own variables).
type Yolo struct {
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// Empty reports whether y adds nothing to a launch: nil, or no arguments and
// no environment.
func (y *Yolo) Empty() bool { return y == nil || (len(y.Args) == 0 && len(y.Env) == 0) }

// clone returns a copy of y that shares nothing with it.
func (y *Yolo) clone() *Yolo {
	if y == nil {
		return nil
	}
	return &Yolo{Args: slices.Clone(y.Args), Env: maps.Clone(y.Env)}
}

// SessionRecipe is how Conductor names an agent's own session (its
// conversation) and resumes it. "{id}" is a whole argument, never part of
// one: it stands for a fresh id in StartArgs and for the stored id in
// ResumeArgs, which go right after the agent's command (Codex resumes with a
// subcommand).
type SessionRecipe struct {
	// StartArgs choose the id at launch: "{id}" becomes a fresh one (NewID).
	// Empty: the agent chooses, and reports it (IDFrom).
	StartArgs []string `json:"startArgs,omitempty"`
	// NewID is how a fresh id is made: "uuid" (the default) or "name"
	// ("cdr-" and a uuid).
	NewID string `json:"newId,omitempty"`
	// IDFrom is "hook" when the agent's hook reports carry its id
	// (internal/notify reads it), "" when only StartArgs set it.
	IDFrom string `json:"idFrom,omitempty"`
	// IDPolicy says which of the ids the agent reports is its session's:
	// "latest" (the default), or "lowest" (Codex, whose title thread
	// reports too).
	IDPolicy string `json:"idPolicy,omitempty"`
	// ResumeArgs resume the stored id: "{id}" is it.
	ResumeArgs []string `json:"resumeArgs,omitempty"`
	// IDPattern is the shape of the agent's ids: anchored RE2 (^…$), at most
	// 200 bytes. An id that does not match, or does not begin with a letter
	// or a digit, is never stored nor passed.
	IDPattern string `json:"idPattern,omitempty"`
	// ResumeNeedsCwd says the agent resumes only in the directory the session
	// ran in: when that is gone, Resume is refused rather than run elsewhere.
	ResumeNeedsCwd bool `json:"resumeNeedsCwd,omitempty"`
}

// Empty reports whether r is no recipe: nil, or nothing to resume with.
func (r *SessionRecipe) Empty() bool { return r == nil || len(r.ResumeArgs) == 0 }

// IDArg is the placeholder of a recipe's id.
const IDArg = "{id}"

// Expand returns args with each IDArg element replaced by id.
func Expand(args []string, id string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == IDArg {
			a = id
		}
		out[i] = a
	}
	return out
}

func (r *SessionRecipe) clone() *SessionRecipe {
	if r == nil {
		return nil
	}
	c := *r
	c.StartArgs, c.ResumeArgs = slices.Clone(r.StartArgs), slices.Clone(r.ResumeArgs)
	return &c
}

// Signal kinds (Signal.Kind).
const (
	SignalHook    = "hook"    // the agent's hooks report through its adapter
	SignalBell    = "bell"    // a terminal bell or an OSC notification; the default
	SignalPattern = "pattern" // Pattern matches the last line of the screen
	SignalNone    = "none"    // never flagged
)

// Signal describes how an agent reports that it needs attention.
type Signal struct {
	Kind    string `json:"kind"`              // hook | bell | pattern | none
	Pattern string `json:"pattern,omitempty"` // RE2 on the last screen line, kind=pattern only
	// ToolEvents asks hook adapters to report tool use as events (chatty; off by default).
	ToolEvents bool `json:"toolEvents,omitempty"`
	// WorkingTitle, with any kind, matches the window title (OSC 0 or 2)
	// the agent writes while it works: RE2, at most 200 bytes, never
	// matching an empty title (CompileWorkingTitle). A title that matches
	// shows the session working (source "title"), as a working report
	// would, but over no question on its screen, no held turn and not the
	// done of the turn just ended; a wait raised while the title says so
	// gives way a second later (session.Options.WorkingTitle). Codex's
	// built-in matches its run state, "Working", "Thinking" or "Waiting",
	// after its spinner's frame: the title Conductor's launch asks it for
	// (tui.terminal_title, internal/agents). A saved agent that leaves it
	// out keeps the one of the agent it replaces (inherit).
	WorkingTitle string `json:"workingTitle,omitempty"`
}

// File is the JSON shape operators write.
type File struct {
	DisableDefaults bool    `json:"disableDefaults"`
	Agents          []Agent `json:"agents"`
}

// Overlay is the UI-managed layer over the configured catalog: agents to add
// or replace by ID, and IDs to hide.
type Overlay struct {
	Agents []Agent  `json:"agents"`
	Hidden []string `json:"hidden,omitempty"`
}

// Source says where the catalog took an agent from.
type Source string

const (
	SourceBuiltIn Source = "built-in" // defaults.go
	SourceConfig  Source = "config"   // the catalog of the config file, or the catalog file
	SourceSaved   Source = "saved"    // the overlay the Agents page saves (catalog.json)
)

// Catalog is an ordered, validated set of agents keyed by ID.
type Catalog struct {
	agents  map[string]Agent
	sources map[string]Source
	order   []string
}

var idPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidID reports whether id has the shape every agent ID has, for packages
// that keep agent IDs of their own, as crews do.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// envNamePattern matches the environment variable names an agent may list in
// envPassthrough. Lower case is allowed: proxy variables such as http_proxy,
// https_proxy and no_proxy are lower case by convention.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// iconPattern is what an icon name looks like: an Iconify name in a class
// (i-lucide-sparkles), at most 64 bytes.
var iconPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9:-]{0,63}$`)

// Size limits enforced by validate, so that a saved agent stays small enough
// for catalog.json and the Agents page.
const (
	maxName           = 60   // characters (runes)
	maxDescription    = 200  // characters (runes)
	maxCommandArgs    = 32   // elements in command
	maxCommandArg     = 4096 // bytes in one command element
	maxEnvKeys        = 32   // entries in env
	maxEnvPassthrough = 32   // entries in envPassthrough
	maxSignalPattern  = 200  // bytes in a signal pattern or a trust prompt
	maxSite           = 200  // bytes in site
	maxYoloArgs       = 16   // elements in yolo.args
	maxYoloEnv        = 16   // entries in yolo.env
	maxYoloValue      = 4096 // bytes in a yolo.env value
)

const (
	maxCwd      = 4096     // bytes in cwd
	maxEnvKey   = 128      // bytes in an env key or an envPassthrough name
	maxEnvValue = 16 << 10 // bytes in an env value
)

// ReadFile parses a catalog file: one JSON value, no unknown field, nothing
// after it (store.DecodeStrict).
func ReadFile(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read catalog: %w", err)
	}
	var f File
	if err := store.DecodeStrict(b, &f); err != nil {
		return File{}, fmt.Errorf("parse catalog %s: %w", path, err)
	}
	return f, nil
}

// Default returns the built-in catalog.
func Default() Catalog {
	c := Catalog{agents: map[string]Agent{}}
	for _, a := range defaults() {
		c.add(a, SourceBuiltIn)
	}
	return c
}

// Load merges f over the defaults (unless disabled) and validates every entry.
func Load(f File) (Catalog, error) {
	c := Catalog{agents: map[string]Agent{}}
	if !f.DisableDefaults {
		for _, a := range defaults() {
			c.add(a, SourceBuiltIn)
		}
	}
	var errs []error
	for i, a := range f.Agents {
		if err := validate(a); err != nil {
			errs = append(errs, fmt.Errorf("agents[%d]: %w", i, err))
			continue
		}
		c.add(a, SourceConfig)
	}
	if len(errs) > 0 {
		return Catalog{}, errors.Join(errs...)
	}
	if len(c.order) == 0 {
		return Catalog{}, errors.New("catalog has no agents")
	}
	return c, nil
}

func (c *Catalog) add(a Agent, src Source) {
	if c.agents == nil {
		c.agents = map[string]Agent{}
	}
	if c.sources == nil {
		c.sources = map[string]Source{}
	}
	if _, exists := c.agents[a.ID]; !exists {
		c.order = append(c.order, a.ID)
	}
	c.agents[a.ID] = a
	c.sources[a.ID] = src
}

// validate checks one agent against every rule. Load and Upsert both run it,
// and ApplyOverlay goes through Upsert.
func validate(a Agent) error {
	if !idPattern.MatchString(a.ID) {
		return fmt.Errorf("id %q must match %s", a.ID, idPattern)
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("agent %s: name must not be empty", a.ID)
	}
	if utf8.RuneCountInString(a.Name) > maxName {
		return fmt.Errorf("agent %s: name must be at most %d characters", a.ID, maxName)
	}
	if utf8.RuneCountInString(a.Description) > maxDescription {
		return fmt.Errorf("agent %s: description must be at most %d characters", a.ID, maxDescription)
	}
	if len(a.Command) == 0 || strings.TrimSpace(a.Command[0]) == "" {
		return fmt.Errorf("agent %s: command must have at least one element", a.ID)
	}
	if len(a.Command) > maxCommandArgs {
		return fmt.Errorf("agent %s: command has too many elements (at most %d)", a.ID, maxCommandArgs)
	}
	for i, arg := range a.Command {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("agent %s: command contains NUL", a.ID)
		}
		if len(arg) > maxCommandArg {
			return fmt.Errorf("agent %s: command[%d] is longer than %d bytes", a.ID, i, maxCommandArg)
		}
	}
	if len(a.Cwd) > maxCwd || strings.ContainsRune(a.Cwd, 0) {
		return fmt.Errorf("agent %s: cwd must be at most %d bytes, without NUL", a.ID, maxCwd)
	}
	if a.Icon != "" && !iconPattern.MatchString(a.Icon) {
		return fmt.Errorf("agent %s: icon must match %s", a.ID, iconPattern)
	}
	if a.Site != "" {
		if err := validateSite(a.Site); err != nil {
			return fmt.Errorf("agent %s: site: %w", a.ID, err)
		}
	}
	// The adapter's shape; the registry of adapters is checked where it is
	// known (agents.CheckAdapter), for the config and for saved agents alike.
	if a.Adapter != "" && !idPattern.MatchString(a.Adapter) {
		return fmt.Errorf("agent %s: adapter must match %s", a.ID, idPattern)
	}
	if len(a.Env) > maxEnvKeys {
		return fmt.Errorf("agent %s: too many env entries (at most %d)", a.ID, maxEnvKeys)
	}
	for k, v := range a.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("agent %s: invalid env entry %q", a.ID, cut(k))
		}
		if len(k) > maxEnvKey {
			return fmt.Errorf("agent %s: env key %q… is longer than %d bytes", a.ID, cut(k), maxEnvKey)
		}
		if len(v) > maxEnvValue {
			return fmt.Errorf("agent %s: env %s: the value is %d bytes, more than %d", a.ID, k, len(v), maxEnvValue)
		}
	}
	if a.Signal != nil {
		if err := validateSignal(*a.Signal); err != nil {
			return fmt.Errorf("agent %s: signal: %w", a.ID, err)
		}
	}
	if a.Yolo != nil {
		if err := validateYolo(*a.Yolo); err != nil {
			return fmt.Errorf("agent %s: yolo: %w", a.ID, err)
		}
	}
	if a.TrustPrompt != "" {
		if _, err := CompilePattern(a.TrustPrompt); err != nil {
			return fmt.Errorf("agent %s: trustPrompt: %w", a.ID, err)
		}
	}
	if a.PromptShape != "" {
		if _, err := CompilePattern(a.PromptShape); err != nil {
			return fmt.Errorf("agent %s: promptShape: %w", a.ID, err)
		}
	}
	if err := validateTrustAnswers(a.TrustAnswers); err != nil {
		return fmt.Errorf("agent %s: trustAnswers: %w", a.ID, err)
	}
	if len(a.Questions) > MaxQuestions {
		return fmt.Errorf("agent %s: questions: at most %d", a.ID, MaxQuestions)
	}
	for i, q := range a.Questions {
		if _, err := CompilePattern(q.Prompt); err != nil {
			return fmt.Errorf("agent %s: questions[%d].prompt: %w", a.ID, i, err)
		}
		if q.Words != "" {
			if _, err := CompilePattern(q.Words); err != nil {
				return fmt.Errorf("agent %s: questions[%d].words: %w", a.ID, i, err)
			}
		}
		if err := validateTrustAnswers(q.Answers); err != nil {
			return fmt.Errorf("agent %s: questions[%d].answers: %w", a.ID, i, err)
		}
		if q.AnswersFromScreen && (!q.Anytime || len(q.Answers) > 0) {
			return fmt.Errorf("agent %s: questions[%d].answersFromScreen: only for a question asked at any time (anytime), with no answers of its own", a.ID, i)
		}
		if q.ModeAnswers != "" {
			if _, err := CompilePattern(q.ModeAnswers); err != nil {
				return fmt.Errorf("agent %s: questions[%d].modeAnswers: %w", a.ID, i, err)
			}
		}
		if q.TypedAnswers != "" {
			if _, err := CompilePattern(q.TypedAnswers); err != nil {
				return fmt.Errorf("agent %s: questions[%d].typedAnswers: %w", a.ID, i, err)
			}
		}
	}
	if a.Session != nil {
		if err := validateSession(*a.Session); err != nil {
			return fmt.Errorf("agent %s: session: %w", a.ID, err)
		}
	}
	if a.Browser != nil {
		if err := validateBrowser(*a.Browser); err != nil {
			return fmt.Errorf("agent %s: browser: %w", a.ID, err)
		}
	}
	if len(a.EnvPassthrough) > maxEnvPassthrough {
		return fmt.Errorf("agent %s: too many envPassthrough entries (at most %d)", a.ID, maxEnvPassthrough)
	}
	for _, name := range a.EnvPassthrough {
		if len(name) > maxEnvKey {
			return fmt.Errorf("agent %s: envPassthrough name %q… is longer than %d bytes", a.ID, cut(name), maxEnvKey)
		}
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("agent %s: invalid envPassthrough entry %q", a.ID, name)
		}
	}
	return nil
}

// validateYolo bounds a yolo recipe like the command and the environment it
// adds to: at most 16 arguments of at most 4096 bytes without NUL, at most 16
// variables, each named like an envPassthrough entry, never CONDUCTOR_*,
// whose values have at most 4096 bytes without NUL. Its errors carry no agent
// id; validate adds it.
func validateYolo(y Yolo) error {
	if len(y.Args) > maxYoloArgs {
		return fmt.Errorf("too many args (at most %d)", maxYoloArgs)
	}
	for i, arg := range y.Args {
		if arg == "" || strings.ContainsRune(arg, 0) || len(arg) > maxCommandArg {
			return fmt.Errorf("args[%d] must be 1 to %d bytes without NUL", i, maxCommandArg)
		}
	}
	if len(y.Env) > maxYoloEnv {
		return fmt.Errorf("too many env entries (at most %d)", maxYoloEnv)
	}
	for k, v := range y.Env {
		if len(k) > maxEnvKey || !envNamePattern.MatchString(k) || strings.HasPrefix(k, "CONDUCTOR_") {
			return fmt.Errorf("invalid env name %q", cut(k))
		}
		if strings.ContainsRune(v, 0) || len(v) > maxYoloValue {
			return fmt.Errorf("env %s: the value must be at most %d bytes without NUL", k, maxYoloValue)
		}
	}
	return nil
}

// validateSession holds a session recipe to the bounds of a command: at most
// 16 arguments of at most 4096 bytes without NUL each way, "{id}" a whole
// argument, once in ResumeArgs and once in StartArgs when it has any; a known
// NewID, IDFrom and IDPolicy; a way to know the id (StartArgs or IDFrom); and
// an anchored IDPattern that matches neither an empty id nor one that begins
// with a dash. The empty recipe, which disables an inherited one, passes.
func validateSession(r SessionRecipe) error {
	if reflect.ValueOf(r).IsZero() {
		return nil
	}
	count := func(name string, args []string, need bool) error {
		if len(args) > maxYoloArgs {
			return fmt.Errorf("too many %s (at most %d)", name, maxYoloArgs)
		}
		n := 0
		for i, a := range args {
			if a == "" || strings.ContainsRune(a, 0) || len(a) > maxCommandArg {
				return fmt.Errorf("%s[%d] must be 1 to %d bytes without NUL", name, i, maxCommandArg)
			}
			if a == IDArg {
				n++
			} else if strings.Contains(a, IDArg) {
				return fmt.Errorf("%s[%d]: %s must be a whole argument", name, i, IDArg)
			}
		}
		if (need || len(args) > 0) && n != 1 {
			return fmt.Errorf("%s must hold %s once", name, IDArg)
		}
		return nil
	}
	if err := count("resumeArgs", r.ResumeArgs, true); err != nil {
		return err
	}
	if err := count("startArgs", r.StartArgs, false); err != nil {
		return err
	}
	switch {
	case r.NewID != "" && r.NewID != "uuid" && r.NewID != "name":
		return fmt.Errorf("newId must be uuid or name")
	case r.IDFrom != "" && r.IDFrom != "hook":
		return fmt.Errorf("idFrom must be hook or empty")
	case r.IDPolicy != "" && r.IDPolicy != "latest" && r.IDPolicy != "lowest":
		return fmt.Errorf("idPolicy must be latest or lowest")
	case len(r.StartArgs) == 0 && r.IDFrom == "":
		return fmt.Errorf("startArgs or idFrom must say how the id is known")
	}
	if len(r.IDPattern) > maxSignalPattern || !strings.HasPrefix(r.IDPattern, "^") || !strings.HasSuffix(r.IDPattern, "$") {
		return fmt.Errorf("idPattern must be anchored (^…$), at most %d bytes", maxSignalPattern)
	}
	re, err := regexp.Compile(r.IDPattern)
	if err != nil {
		return fmt.Errorf("idPattern: %w", err)
	}
	if re.MatchString("") || re.MatchString("-x") || re.MatchString("--x") {
		return fmt.Errorf("idPattern must not match an empty id or one that begins with a dash")
	}
	return nil
}

// cut keeps the first 40 bytes of s for an error message, so an error never
// sends a long value back whole.
func cut(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

// validateSite accepts an https URL with a host name, a port in range if any,
// no user info and no white space (a no-break space included), at most maxSite
// bytes, so that the Agents page can link to it as it is and a browser parses
// it as the server does.
func validateSite(site string) error {
	if len(site) > maxSite {
		return fmt.Errorf("must be at most %d bytes", maxSite)
	}
	u, err := url.Parse(site)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsFunc(site, unicode.IsSpace) || !validPort(u.Port()) {
		return errors.New("must be an https:// URL with a host")
	}
	return nil
}

// validPort reports whether port, as url.URL.Port gives it, is absent or a
// TCP port from 1 to 65535.
func validPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// validateSignal checks a signal's kind and pattern. Its errors carry no agent
// id; validate adds it.
func validateSignal(s Signal) error {
	switch s.Kind {
	case SignalHook, SignalBell, SignalNone:
		if s.Pattern != "" {
			return errors.New("pattern only applies to kind=pattern")
		}
	case SignalPattern:
		if _, err := CompilePattern(s.Pattern); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown kind %q", s.Kind)
	}
	if s.WorkingTitle != "" {
		if _, err := CompileWorkingTitle(s.WorkingTitle); err != nil {
			return fmt.Errorf("workingTitle: %w", err)
		}
	}
	return nil
}

// CompilePattern compiles a screen pattern: the pattern of a signal of kind
// "pattern" and the value of `conductor host --signal-pattern`. It is RE2, so
// matching the last line of a terminal takes linear time whatever the pattern,
// it is at most 200 bytes, and it does not match an empty line: one that did
// would match a screen that shows nothing and mark every quiet session as
// waiting.
func CompilePattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > maxSignalPattern {
		return nil, fmt.Errorf("pattern required, at most %d bytes", maxSignalPattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if re.MatchString("") {
		return nil, errors.New("pattern must not match an empty line")
	}
	return re, nil
}

// CompileWorkingTitle compiles a signal's workingTitle: RE2, so matching a
// title the agent writes ten times a second takes linear time whatever the
// pattern, at most 200 bytes, and it does not match an empty title, which
// is what a title cut at the bound the session reads it to is taken as
// (session.Scanner), and what a program writes to clear its title.
func CompileWorkingTitle(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > maxSignalPattern {
		return nil, fmt.Errorf("pattern required, at most %d bytes", maxSignalPattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if re.MatchString("") {
		return nil, errors.New("pattern must not match an empty title")
	}
	return re, nil
}

// Upsert validates a, then replaces the agent with the same ID in place (so it
// keeps its position) or appends a copy of a as a new one, from the overlay
// (SourceSaved). a is not kept: changing it later changes nothing here.
func (c *Catalog) Upsert(a Agent) error {
	if err := validate(a); err != nil {
		return err
	}
	c.add(a.clone(), SourceSaved)
	return nil
}

// Hide removes the agent with the given ID and reports whether it was there.
func (c *Catalog) Hide(id string) bool {
	if _, ok := c.agents[id]; !ok {
		return false
	}
	delete(c.agents, id)
	delete(c.sources, id)
	c.order = slices.DeleteFunc(c.order, func(other string) bool { return other == id })
	return true
}

// ApplyOverlay upserts every agent in o over the agent it replaces, with what
// it leaves out taken from that one (inherit), then hides every ID in
// o.Hidden, so hiding wins over an agent with the same ID. Hiding an ID that
// is not in the catalog is not an error. If any agent is invalid the catalog
// is unchanged.
func (c *Catalog) ApplyOverlay(o Overlay) error {
	next := c.Clone()
	for i, a := range o.Agents {
		prev, had := next.Get(a.ID)
		if err := next.Upsert(inherit(a, prev, had)); err != nil {
			return fmt.Errorf("agents[%d]: %w", i, err)
		}
	}
	for _, id := range o.Hidden {
		next.Hide(id)
	}
	*c = next
	return nil
}

// inherit returns a, an agent saved over prev (had says there was one), with
// what it leaves to prev filled in: an adapter, a signal (or its working title), a site, a yolo recipe, a trust prompt, a prompt shape or a browser it omits,
// and every env value it holds as RedactedValue. The Agents page stores the
// mask for a key whose value the editor did not change, so a value changed in
// the config reaches the agent. A value equal to prev's counts as the mask: it
// is prev's value, and the server stores the mask for it at start and on save
// (internal/api), which is how an override saved in full by an earlier version
// comes to follow the config. A masked key that prev does not have, and every
// masked value when there is no prev, is dropped. a itself is not changed.
func inherit(a, prev Agent, had bool) Agent {
	a = a.clone()
	if had {
		if a.Adapter == "" {
			a.Adapter = prev.Adapter
		}
		if a.Signal == nil && prev.Signal != nil {
			s := *prev.Signal
			a.Signal = &s
		} else if a.Signal != nil && a.Signal.WorkingTitle == "" && prev.Signal != nil {
			// A signal saved without a working title (by an Agents page or
			// a version that had none) keeps the replaced agent's: the
			// form has no control for it.
			a.Signal.WorkingTitle = prev.Signal.WorkingTitle
		}
		if a.Site == "" {
			a.Site = prev.Site
		}
		if a.Yolo == nil {
			a.Yolo = prev.Yolo.clone()
		}
		if a.TrustPrompt == "" {
			a.TrustPrompt = prev.TrustPrompt
		}
		if a.PromptShape == "" {
			a.PromptShape = prev.PromptShape
		}
		if a.TrustAnswers == nil {
			a.TrustAnswers = append([]Answer(nil), prev.TrustAnswers...)
		}
		if a.Questions == nil {
			a.Questions = cloneQuestions(prev.Questions)
		}
		if a.Session == nil {
			a.Session = prev.Session.clone()
		}
		if a.Probe == nil && prev.Probe != nil {
			v := *prev.Probe
			a.Probe = &v
		}
		if a.Browser == nil {
			a.Browser = prev.Browser.clone()
		}
	}
	for k, v := range a.Env {
		pv, inPrev := prev.Env[k]
		inPrev = had && inPrev
		if v != RedactedValue && (!inPrev || v != pv) {
			continue // a value of its own
		}
		if inPrev {
			a.Env[k] = pv
		} else {
			delete(a.Env, k)
		}
	}
	return a
}

// Clone returns a deep copy: no map, slice or signal is shared with c. Upsert,
// Hide and ApplyOverlay change a catalog in place, so a catalog that other
// goroutines read should be cloned, changed and swapped in, not changed itself.
func (c Catalog) Clone() Catalog {
	out := Catalog{
		agents:  make(map[string]Agent, len(c.agents)),
		sources: maps.Clone(c.sources),
		order:   slices.Clone(c.order),
	}
	for id, a := range c.agents {
		out.agents[id] = a.clone()
	}
	return out
}

// Source says where the agent with the given ID comes from: built in, the
// config, or saved from the Agents page; "" when the catalog has no such agent.
func (c Catalog) Source(id string) Source { return c.sources[id] }

// Copy returns a copy of a that shares no slice, map or pointer with it, for
// a package that takes an agent apart (a copy of one, internal/api).
func (a Agent) Copy() Agent { return a.clone() }

// clone returns a copy of a that shares no slice, map or pointer with it.
func (a Agent) clone() Agent {
	a.Command = slices.Clone(a.Command)
	a.Env = maps.Clone(a.Env)
	a.EnvPassthrough = slices.Clone(a.EnvPassthrough)
	if a.Signal != nil {
		s := *a.Signal
		a.Signal = &s
	}
	a.Yolo = a.Yolo.clone()
	a.Session = a.Session.clone()
	a.Questions = cloneQuestions(a.Questions)
	if a.Probe != nil {
		v := *a.Probe
		a.Probe = &v
	}
	a.Browser = a.Browser.clone()
	return a
}

// Get returns the agent with the given ID.
func (c Catalog) Get(id string) (Agent, bool) {
	a, ok := c.agents[id]
	return a, ok
}

// List returns agents in definition order.
func (c Catalog) List() []Agent {
	out := make([]Agent, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.agents[id])
	}
	return out
}

// RedactedValue replaces every environment value in the output of Redacted.
// The API treats it as "unchanged" when an agent comes back on save with a value
// still set to it.
const RedactedValue = "***"

// Redacted returns a copy with environment values hidden, suitable for API
// output. Everything else, including Adapter, Signal and EnvPassthrough, is
// kept: none of it is secret.
func (a Agent) Redacted() Agent {
	if len(a.Env) == 0 {
		return a
	}
	cp := a
	cp.Env = make(map[string]string, len(a.Env))
	for k := range a.Env {
		cp.Env[k] = RedactedValue
	}
	return cp
}

// EffectiveSignal returns the agent's signal, or the bell default when it has
// none.
func (a Agent) EffectiveSignal() Signal {
	if a.Signal == nil {
		return Signal{Kind: SignalBell}
	}
	return *a.Signal
}

// Answer is one answer to the agent's trust question (Agent.TrustAnswers): the
// label a page shows and the keys that pick it in the agent's dialog.
type Answer struct {
	Label string `json:"label"`
	Input string `json:"input"`
}

// Question is one of the agent's other startup questions (Agent.Questions):
// the pattern of its words on the screen and its answers.
type Question struct {
	Prompt string `json:"prompt"`
	// Words, when set, picks the question's words out of the same text for
	// the message a page shows: what its last match's capturing groups
	// matched, joined by a space, or the whole of that match when it has no
	// group. Without it, or when it matches nothing, the words are what
	// Prompt matched. A pattern as Prompt is. Codex's update question is
	// found by its highlighted "Update now (runs …" and told by its heading,
	// "Update available 0.161.0 → 0.162.0": the highlighted line read as if
	// it had been the answer.
	Words string `json:"words,omitempty"`
	// Anytime marks a question the agent can ask at any time in the
	// session's life, not only as it starts: it is watched until the
	// session ends (a startup question only until the first submission),
	// held as a startup question is but with source "question", and raised
	// again each time it is drawn after an answer. Its prompt and words are
	// matched against the screen as the program drew it (session.Screen):
	// its rows, each without the spaces at its end, joined by line breaks,
	// without the blank rows at the bottom, its last 80 rows at most.
	// Codex's command approval is one.
	Anytime bool     `json:"anytime,omitempty"`
	Answers []Answer `json:"answers,omitempty"`
	// AnswersFromScreen has the question's answers read off the screen
	// each time it is asked, in place of Answers: the numbered list its
	// dialog draws at the bottom, each choice its label and its number (a
	// digit picks it at once), and, without Words, its words the rows
	// above the list up to the dialog's top rule (session.readList). A
	// dialog whose list cannot be read is not asked. Only with Anytime and
	// no Answers. Claude Code's dialogs are read so: their choices change
	// with the tool, the permission mode and the release.
	AnswersFromScreen bool `json:"answersFromScreen,omitempty"`
	// ModeAnswers, when set, matches the labels of the question's answers
	// that change the session's permission mode (Claude Code's "switch to
	// auto mode", "accept edits", "bypass permissions"): a page gives such
	// a choice no number key and never offers it first. A pattern as
	// Prompt is.
	ModeAnswers string `json:"modeAnswers,omitempty"`
	// TypedAnswers, when set, matches the answers read off the screen
	// that take typed words, each with the rows that follow it (Claude
	// Code's "Tell Claude what to change", whose hint reads "shift+tab to
	// approve with this feedback"): while the dialog's highlight stands
	// before one, its keys type into that field, so the question offers
	// no choices and is answered in the terminal. A pattern as Prompt is.
	TypedAnswers string `json:"typedAnswers,omitempty"`
}

// MaxQuestions bounds Agent.Questions.
const MaxQuestions = 8

func cloneQuestions(qs []Question) []Question {
	if qs == nil {
		return nil
	}
	out := make([]Question, len(qs))
	for i, q := range qs {
		q.Answers = append([]Answer(nil), q.Answers...)
		out[i] = q
	}
	return out
}

// Bounds of Agent.TrustAnswers: as many as a prompt's quick-reply choices,
// with the same label and key limits (session.MaxAttentionOptions and the rest).
const (
	MaxTrustAnswers     = 6
	MaxTrustAnswerLabel = 60 // runes
	MaxTrustAnswerInput = 48 // bytes
)

func validateTrustAnswers(answers []Answer) error {
	if len(answers) > MaxTrustAnswers {
		return fmt.Errorf("at most %d answers", MaxTrustAnswers)
	}
	for i, a := range answers {
		label := strings.TrimSpace(a.Label)
		if label == "" || len([]rune(label)) > MaxTrustAnswerLabel {
			return fmt.Errorf("answer %d: a label of 1 to %d characters", i+1, MaxTrustAnswerLabel)
		}
		if a.Input == "" || len(a.Input) > MaxTrustAnswerInput {
			return fmt.Errorf("answer %d: keys of 1 to %d bytes", i+1, MaxTrustAnswerInput)
		}
	}
	return nil
}
