package capacity

import (
	"encoding/json"
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
	ReadAt    time.Time
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
}

// clip cuts s to at most n bytes on a character's boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func clipp(p *string, n int) *string {
	if p == nil {
		return nil
	}
	s := clip(*p, n)
	return &s
}

// bounded is r held to the bounds a kept reading has, its slices its own.
func (r Reading) bounded() Reading {
	r.Words, r.Plan = clip(r.Words, MaxWords), clip(r.Plan, MaxPlan)
	r.Program, r.Version, r.Qualified = clip(r.Program, MaxShort), clip(r.Version, MaxVersion), clip(r.Qualified, MaxVersion)
	ws := make([]Window, 0, min(len(r.Windows), MaxWindows))
	for _, w := range r.Windows[:min(len(r.Windows), MaxWindows)] {
		w.Kind, w.Name, w.Limit, w.Severity = clip(w.Kind, MaxShort), clip(w.Name, MaxName), clip(w.Limit, MaxShort), clip(w.Severity, MaxShort)
		w.Percent = max(0, min(100, w.Percent))
		ws = append(ws, w)
	}
	cs := make([]Credit, 0, min(len(r.Credits), MaxCredits))
	for _, c := range r.Credits[:min(len(r.Credits), MaxCredits)] {
		c.Kind, c.Currency = clip(c.Kind, MaxShort), clip(c.Currency, MaxShort)
		c.Used, c.Limit, c.Balance = clipp(c.Used, MaxFigure), clipp(c.Limit, MaxFigure), clipp(c.Balance, MaxFigure)
		cs = append(cs, c)
	}
	r.Windows, r.Credits = ws, cs
	r.Limits.Reached = clip(r.Limits.Reached, MaxShort)
	if r.State != StateOK && r.Words == "" {
		r.Words = StateWords(r.State)
	}
	return r
}

// view is the View of a kept reading at now: what the reading holds and
// how fresh it is.
func (r Reading) view(now time.Time) View {
	at := r.ReadAt
	v := View{
		Agent:   ViewAgent{ID: r.Key.Agent, Version: r.Version},
		Account: ViewAccount{ID: r.Key.Account, Plan: r.Plan},
		State:   r.State,
		Words:   r.Words,
		Windows: r.Windows,
		Credits: r.Credits,
		Limits:  r.Limits,
		ReadAt:  &at,
		ReadBy:  &ReadBy{Program: r.Program, Version: r.Version, Qualified: r.Qualified},
		Fresh:   FreshNow,
	}
	if now.Sub(r.ReadAt) >= FreshFor {
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
