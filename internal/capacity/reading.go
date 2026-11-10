package capacity

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// The states of a View. A Reading's state is one of ok, signed_out,
// key_paid, unqualified and error; reading and never_read are the cache's;
// the rest are answered without a read.
const (
	StateOK           = "ok"
	StateReading      = "reading"
	StateUnsupported  = "unsupported"
	StateNotInstalled = "not_installed"
	StateSignedOut    = "signed_out"
	StateKeyPaid      = "key_paid"
	StateUnqualified  = "unqualified"
	StateRemote       = "remote"
	StateEnvSet       = "env_set"
	StateUnlisted     = "unlisted"
	StateNotOwner     = "not_owner"
	StateError        = "error"
	StateNeverRead    = "never_read"
)

// stateWords is each state's words when nothing more precise is known.
var stateWords = map[string]string{
	StateOK:           "the provider's figures at the moment of the read",
	StateReading:      "reading the agent's usage now",
	StateUnsupported:  "Conductor knows no way to read this agent's usage",
	StateNotInstalled: "the agent's program is not installed on this server",
	StateSignedOut:    "the agent is signed out on this account",
	StateKeyPaid:      "this login is paid by key: the provider reports no usage windows for it",
	StateUnqualified:  "this version of the agent has not been checked for reading its usage",
	StateRemote:       "read where it runs: not on this server",
	StateEnvSet:       "this launch's environment sets the agent's login: its usage is not read",
	StateUnlisted:     "a folder not in Accounts: add it to read its usage",
	StateNotOwner:     "another server over this data directory reads this agent's usage",
	StateError:        "the read failed",
	StateNeverRead:    "not read since the server started",
}

// StateWords answers a state's words, "" for no state of a View.
func StateWords(state string) string { return stateWords[state] }

// The freshness of a View.
const (
	FreshNow         = "fresh"        // read less than FreshFor ago
	FreshStale       = "stale"        // read FreshFor ago or more
	FreshResetPassed = "reset_passed" // a window's reset is behind the clock
	FreshNone        = "none"         // nothing read
)

// The sources a View's figures name.
const (
	SourceProvider  = "provider"  // what the agent's own tool reported
	SourceConductor = "conductor" // what this server counted itself
)

// The bounds a reading is held to as it is kept, which keep a View small.
// Free text for people (words, a plan's, a window's and a program's name)
// is cut to them, saying so; any other value past its bound makes the
// reading error, never a cut-down value (Reading.kept).
const (
	MaxWords   = 300 // bytes
	MaxPlan    = 40  // bytes
	MaxName    = 80  // bytes of a window's name
	MaxShort   = 40  // bytes of a kind, a limit id, a reached type, a currency
	MaxFigure  = 32  // bytes of a credit's figure
	MaxWindows = 16
	MaxCredits = 4
)

// Key is what a reading is of: a catalog agent id and an account id
// ("default" for the agent's own login).
type Key struct {
	Agent   string
	Account string
}

// Reading is the report of one read for one key: what the agent's tool
// said, the agent's version that read found, the version its reader was
// qualified on, and when. It is kept in memory only.
type Reading struct {
	Key       Key
	State     string // ok | signed_out | key_paid | unqualified | error
	Words     string // why, for every state but ok
	Plan      string // the subscription as the tool says it
	Windows   []Window
	Credits   []Credit
	Limits    Limits
	Program   string // the program read, for people ("Claude Code")
	Version   string // the agent's version this read found; "" when none
	Qualified string // the reader's qualified version
	// ReadAt is the cache's: its clock's time when the read returned. A
	// time the reader puts here is replaced, so no reader's clock decides
	// freshness.
	ReadAt time.Time
	// Env is the hex SHA-256 of the variables the read was made with,
	// never their values; "" when the reader gave none.
	Env string
}

// Window is one usage limit as the provider reported it. A window the
// provider did not report is absent, never a zero one.
type Window struct {
	Source   string     `json:"source"`             // always "provider"
	Kind     string     `json:"kind"`               // the provider's word
	Name     string     `json:"name"`               // for people: "5-hour", "Weekly, all models"
	Minutes  int        `json:"minutes,omitempty"`  // the window's length when the provider says it
	Percent  int        `json:"percent"`            // 0 to 100, the provider's share used
	ResetsAt *time.Time `json:"resetsAt,omitempty"` // nil when the provider gave none
	Severity string     `json:"severity,omitempty"` // normal | warning | critical; "" when not reported
	Active   *bool      `json:"active,omitempty"`
	Limit    string     `json:"limit,omitempty"` // the provider's limit id
}

// MarshalJSON writes the window with its source, whatever it was set to.
func (w Window) MarshalJSON() ([]byte, error) {
	type plain Window
	w.Source = SourceProvider
	return json.Marshal(plain(w))
}

// Credit is one of the provider's credit figures, as it wrote them.
type Credit struct {
	Source    string  `json:"source"` // always "provider"
	Kind      string  `json:"kind"`   // extra_usage | credits
	Enabled   *bool   `json:"enabled,omitempty"`
	Used      *string `json:"used,omitempty"`
	Limit     *string `json:"limit,omitempty"`
	Balance   *string `json:"balance,omitempty"`
	Unlimited bool    `json:"unlimited,omitempty"`
	Currency  string  `json:"currency,omitempty"`
}

// MarshalJSON writes the credit with its source.
func (c Credit) MarshalJSON() ([]byte, error) {
	type plain Credit
	c.Source = SourceProvider
	return json.Marshal(plain(c))
}

// Limits are the provider's own refusal flags, apart from any window;
// each absent when not reported, never false.
type Limits struct {
	Source          string `json:"source"`            // always "provider"
	Reached         string `json:"reached,omitempty"` // the reached type as the provider says it
	SpendControl    *bool  `json:"spendControl,omitempty"`
	OrdinaryAllowed *bool  `json:"ordinaryAllowed,omitempty"`
}

// MarshalJSON writes the limits with their source.
func (l Limits) MarshalJSON() ([]byte, error) {
	type plain Limits
	l.Source = SourceProvider
	return json.Marshal(plain(l))
}

// Counts are this server's own counts for a key, an estimate kept apart
// from every figure of the provider's: no code adds, compares or merges
// the two.
type Counts struct {
	Source    string     `json:"source"`   // always "conductor"
	Estimate  bool       `json:"estimate"` // always true
	Running   int        `json:"running"`  // sessions running bound to the key
	Started5h int        `json:"started5h"`
	Started7d int        `json:"started7d"`
	Since     *time.Time `json:"since,omitempty"` // since when this server counts
}

// MarshalJSON writes the counts as Conductor's estimate.
func (c Counts) MarshalJSON() ([]byte, error) {
	type plain Counts
	c.Source, c.Estimate = SourceConductor, true
	return json.Marshal(plain(c))
}

// ViewAgent is the agent a View is of.
type ViewAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Adapter string `json:"adapter,omitempty"`
	Version string `json:"version,omitempty"`
}

// ViewModel is the model a launch would ask for.
type ViewModel struct {
	Chosen string `json:"chosen"` // "" for the agent's default
	Words  string `json:"words,omitempty"`
}

// ViewAccount is the account a View is of.
type ViewAccount struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Default bool   `json:"default,omitempty"`
	Retired bool   `json:"retired,omitempty"`
	Plan    string `json:"plan,omitempty"` // as the tool reported it
}

// The kinds of a ViewHost.
const (
	HostNative       = "native"
	HostUndetermined = "undetermined"
	HostRemote       = "remote"
)

// ViewHost is where a launch would run.
type ViewHost struct {
	Kind  string `json:"kind"`           // native | undetermined | remote
	Name  string `json:"name,omitempty"` // the server's name, for native
	Words string `json:"words,omitempty"`
}

// ReadBy is what made a reading: the program, the version it found, and
// the version its reader was qualified on.
type ReadBy struct {
	Program   string `json:"program"`
	Version   string `json:"version"`
	Qualified string `json:"qualified"`
}

// View is what a page and the CLI show of a key. The cache fills what a
// reading holds (Cache.View); the API fills the agent's name, the model,
// the account's label, the host and the counts, and answers the states no
// read gives.
type View struct {
	Agent    ViewAgent   `json:"agent"`
	Model    ViewModel   `json:"model"`
	Account  ViewAccount `json:"account"`
	Host     ViewHost    `json:"host"`
	State    string      `json:"state"`
	Words    string      `json:"words,omitempty"`
	Windows  []Window    `json:"windows"`
	Credits  []Credit    `json:"credits"`
	Limits   Limits      `json:"limits"`
	Counts   Counts      `json:"counts"`
	ReadAt   *time.Time  `json:"readAt,omitempty"`
	ReadBy   *ReadBy     `json:"readBy,omitempty"`
	Fresh    string      `json:"fresh"`
	NextRead *time.Time  `json:"nextReadAfter,omitempty"`
	// Env is the reading's Env, kept out of the JSON (MarkEnv).
	Env string `json:"-"`
}

// EnvWords are the words of a view whose reading was made with another
// environment than a read would be now.
const EnvWords = "the agent's environment changed since this reading"

// MarkEnv makes the view stale, with EnvWords, when it shows a reading
// made with an environment other than env, the hex SHA-256 of the
// variables a read of its key would be made with now.
func (v *View) MarkEnv(env string) {
	if v.ReadAt != nil && v.Env != env {
		v.Fresh, v.Words = FreshStale, EnvWords
	}
}

// cutMark ends free text that was cut, so the cut is said.
const cutMark = "…"

// cut holds free text meant for a person (words, a plan's or a window's
// name, a program's name) to at most n bytes: cut on a character's
// boundary and ended with cutMark when it was longer. Nothing else a
// reader answers is ever cut (bounded).
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	m := n - len(cutMark)
	for m > 0 && !utf8.RuneStart(s[m]) {
		m--
	}
	return s[:m] + cutMark
}

// own is a copy of what p points to, so no pointer of a kept reading is
// the reader's or a caller's.
func own[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (w Window) clone() Window {
	w.ResetsAt, w.Active = own(w.ResetsAt), own(w.Active)
	return w
}

func (c Credit) clone() Credit {
	c.Enabled, c.Used, c.Limit, c.Balance = own(c.Enabled), own(c.Used), own(c.Limit), own(c.Balance)
	return c
}

func (l Limits) clone() Limits {
	l.SpendControl, l.OrdinaryAllowed = own(l.SpendControl), own(l.OrdinaryAllowed)
	return l
}

func cloneWindows(ws []Window) []Window {
	out := make([]Window, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.clone())
	}
	return out
}

func cloneCredits(cs []Credit) []Credit {
	out := make([]Credit, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.clone())
	}
	return out
}

// outOfBounds names the first value of an ok reading past its bound, ""
// when every one fits. A value that does not fit is never cut: the
// reading is unknown instead (kept).
func (r Reading) outOfBounds() string {
	long := func(what string, s string, n int) string {
		if len(s) > n {
			return fmt.Sprintf("%s is longer than %d bytes", what, n)
		}
		return ""
	}
	longp := func(what string, p *string, n int) string {
		if p == nil {
			return ""
		}
		return long(what, *p, n)
	}
	first := func(whys ...string) string {
		for _, w := range whys {
			if w != "" {
				return w
			}
		}
		return ""
	}
	switch {
	case len(r.Windows) > MaxWindows:
		return fmt.Sprintf("%d windows, more than %d", len(r.Windows), MaxWindows)
	case len(r.Credits) > MaxCredits:
		return fmt.Sprintf("%d credit figures, more than %d", len(r.Credits), MaxCredits)
	}
	if why := first(long("the version", r.Version, MaxVersion), long("the qualified version", r.Qualified, MaxVersion),
		long("the reached limit", r.Limits.Reached, MaxShort)); why != "" {
		return why
	}
	for i, w := range r.Windows {
		switch {
		case w.Percent < 0 || w.Percent > 100:
			return fmt.Sprintf("window %d's percent is %d, not 0 to 100", i+1, w.Percent)
		case w.Minutes < 0:
			return fmt.Sprintf("window %d's length is %d minutes", i+1, w.Minutes)
		}
		if why := first(long(fmt.Sprintf("window %d's kind", i+1), w.Kind, MaxShort), long(fmt.Sprintf("window %d's limit", i+1), w.Limit, MaxShort),
			long(fmt.Sprintf("window %d's severity", i+1), w.Severity, MaxShort)); why != "" {
			return why
		}
	}
	for i, c := range r.Credits {
		if why := first(long(fmt.Sprintf("credit %d's kind", i+1), c.Kind, MaxShort), long(fmt.Sprintf("credit %d's currency", i+1), c.Currency, MaxShort),
			longp(fmt.Sprintf("credit %d's used figure", i+1), c.Used, MaxFigure), longp(fmt.Sprintf("credit %d's limit", i+1), c.Limit, MaxFigure),
			longp(fmt.Sprintf("credit %d's balance", i+1), c.Balance, MaxFigure)); why != "" {
			return why
		}
	}
	return ""
}

// kept is the reading the cache keeps of what the reader answered for k,
// the read having returned at at (the cache's clock):
//
//   - its time is at, whatever the reader dated it;
//   - a state no reading has is error;
//   - an ok reading with a value past its bound (a figure, a version, a
//     percent, a count of windows or credits) is error, with words naming
//     the bound: never a cut or clamped figure;
//   - a reading that is not ok holds its state, its words, its time and
//     its environment's hash, and nothing of the reader's plan, windows,
//     credits, limits or versions;
//   - free text for people is cut, saying so (cut);
//   - every slice and pointer is its own, never the reader's.
func (r Reading) kept(k Key, at time.Time) Reading {
	failed := func(words string) Reading {
		return Reading{Key: k, State: StateError, Words: cut(words, MaxWords), ReadAt: at, Env: r.Env}
	}
	switch r.State {
	case StateOK, StateSignedOut, StateKeyPaid, StateUnqualified, StateError:
	default:
		return failed("the reader answered no state a reading has")
	}
	if r.State != StateOK {
		words := r.Words
		if words == "" {
			words = StateWords(r.State)
		}
		return Reading{Key: k, State: r.State, Words: cut(words, MaxWords), ReadAt: at, Env: r.Env}
	}
	if why := r.outOfBounds(); why != "" {
		return failed("the read's answer is unknown: " + why)
	}
	out := Reading{Key: k, State: StateOK, Words: cut(r.Words, MaxWords), Plan: cut(r.Plan, MaxPlan),
		Windows: cloneWindows(r.Windows), Credits: cloneCredits(r.Credits), Limits: r.Limits.clone(),
		Program: cut(r.Program, MaxShort), Version: r.Version, Qualified: r.Qualified, ReadAt: at, Env: r.Env}
	for i := range out.Windows {
		out.Windows[i].Name = cut(out.Windows[i].Name, MaxName)
	}
	return out
}

// view is the View of a kept reading at now: what the reading holds, in
// copies of its own, and how fresh it is. A reading that is not ok shows
// its state and its words alone. A reading dated ahead of the clock (the
// clock set back) is never fresh.
func (r Reading) view(now time.Time) View {
	at := r.ReadAt
	v := View{
		Agent:   ViewAgent{ID: r.Key.Agent, Version: r.Version},
		Account: ViewAccount{ID: r.Key.Account, Plan: r.Plan},
		State:   r.State,
		Words:   r.Words,
		Windows: cloneWindows(r.Windows),
		Credits: cloneCredits(r.Credits),
		Limits:  r.Limits.clone(),
		ReadAt:  &at,
		Fresh:   FreshNow,
		Env:     r.Env,
	}
	if r.State == StateOK {
		v.ReadBy = &ReadBy{Program: r.Program, Version: r.Version, Qualified: r.Qualified}
	}
	if age := now.Sub(r.ReadAt); age >= FreshFor || age < 0 {
		v.Fresh = FreshStale
	}
	for _, w := range r.Windows {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			v.Fresh = FreshResetPassed
		}
	}
	return v
}

// emptyView is the View of a key nothing has been read for.
func emptyView(k Key) View {
	return View{
		Agent:   ViewAgent{ID: k.Agent},
		Account: ViewAccount{ID: k.Account},
		State:   StateNeverRead,
		Words:   StateWords(StateNeverRead),
		Windows: []Window{},
		Credits: []Credit{},
		Fresh:   FreshNone,
	}
}
