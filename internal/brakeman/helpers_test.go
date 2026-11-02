package brakeman

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// facts is a Facts double: the agents by id, and the errors its checks
// answer.
type facts struct {
	agents     map[string]AgentFacts
	accountErr error
	cwdErr     error
	crewErr    error
	crews      []string // the bodies CheckCrew was given
}

func (f *facts) Agent(id string) (AgentFacts, bool) { a, ok := f.agents[id]; return a, ok }
func (f *facts) CheckAccount(string) error          { return f.accountErr }
func (f *facts) CheckCwd(string) error              { return f.cwdErr }
func (f *facts) CheckCrew(b json.RawMessage) error {
	f.crews = append(f.crews, string(b))
	return f.crewErr
}

// goodFacts knows claude (everything) and codex (no tools rule), as the
// adapters are.
func goodFacts() *facts {
	return &facts{agents: map[string]AgentFacts{
		"claude": {MCP: true, ReadOnly: true, Model: true, AllowTools: true},
		"codex":  {MCP: true, ReadOnly: true, Model: true},
	}}
}

// clock is a fake clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// zone is the local zone of the tests' days.
var zone = time.FixedZone("CST", -6*3600)

// fixture is a store in a data directory of its own, on a fake clock.
type fixture struct {
	t     *testing.T
	data  *store.Store
	clock *clock
	s     *Store
	log   *bytes.Buffer
	logMu *sync.Mutex
}

// syncBuffer is a bytes.Buffer safe for the log's writer.
type syncBuffer struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	data, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, data: data, clock: &clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, zone)}, log: &bytes.Buffer{}, logMu: &sync.Mutex{}}
	f.s = f.open()
	return f
}

// open opens another store on the fixture's data directory.
func (f *fixture) open() *Store {
	f.t.Helper()
	log := slog.New(slog.NewTextHandler(syncBuffer{f.logMu, f.log}, nil))
	s, err := Open(f.data, Options{Now: f.clock.now, Location: zone, DefaultCwd: "/srv/work", Log: log})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { s.Close() })
	return s
}

// reopen closes the fixture's store and opens it again, as a restart.
func (f *fixture) reopen() {
	f.t.Helper()
	f.s.Close()
	f.s = f.open()
}

func (f *fixture) logged() string {
	f.logMu.Lock()
	defer f.logMu.Unlock()
	return f.log.String()
}

// create keeps a briefing request under op id op.
func (f *fixture) create(op string) Request {
	f.t.Helper()
	r, created, err := f.s.Create(Request{OpID: op, Kind: KindBriefing, By: "Nate"})
	if err != nil || !created {
		f.t.Fatalf("create %s: %v %v", op, created, err)
	}
	return r
}

// onConfig is the configuration a turn starts under in the tests.
func onConfig() Config {
	c := DefaultConfig("/srv/work")
	c.Enabled, c.AgentID = true, "claude"
	return c
}

// start creates a request and starts it.
func (f *fixture) start(op string) Request {
	f.t.Helper()
	r := f.create(op)
	r, err := f.s.Start(r.ID, onConfig(), Stamp{AgentID: "claude", Adapter: "claude", Version: "2.1.304"})
	if err != nil {
		f.t.Fatalf("start %s: %v", r.ID, err)
	}
	return r
}

// close moves a request to state.
func (f *fixture) close(id, state string) Request {
	f.t.Helper()
	r, err := f.s.Update(id, func(r *Request) error { return r.To(state, "closed by the test", f.clock.now()) })
	if err != nil {
		f.t.Fatalf("close %s %s: %v", id, state, err)
	}
	return r
}

// ref parses s or fails the test.
func ref(t *testing.T, s string) Ref {
	t.Helper()
	r, err := ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	runA     = "team-0000000a"
	sessA    = "0123456789abcdef"
	questA   = "fedcba9876543210"
	commentA = "00112233445566aa"
)

// factItem is a fact citing the run.
func factItem(t *testing.T, text string) Item {
	return Item{Kind: ItemFact, Fact: &Fact{Text: text, Evidence: []Ref{ref(t, "run:"+runA)}}}
}

// goodBrief is a brief of one fact.
func goodBrief(t *testing.T) Brief {
	return Brief{Schema: BriefSchema, Title: "Overnight", Summary: "One run failed.", Items: []Item{factItem(t, "The run failed.")}}
}

// allowAll is every permission a brief may need.
var allowAll = Permissions{Observe: true, Draft: true, Propose: true}

// wantInvalid fails unless err is an *Invalid of field.
func wantInvalid(t *testing.T, err error, field string) *Invalid {
	t.Helper()
	var inv *Invalid
	if !errors.As(err, &inv) {
		t.Fatalf("want an invalid %s, got %v", field, err)
	}
	if inv.Field != field {
		t.Fatalf("want an invalid %s, got %s", field, inv)
	}
	return inv
}

// pad is a string of n bytes of c.
func pad(c string, n int) string { return strings.Repeat(c, n)[:n] }
