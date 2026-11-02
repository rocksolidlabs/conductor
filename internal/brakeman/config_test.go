package brakeman

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// longPath is an absolute, clean path of n bytes.
func longPath(n int) string {
	var b strings.Builder
	for left := n; left > 0; left = n - b.Len() {
		if left == 1 {
			b.WriteString("d")
			break
		}
		b.WriteString("/" + strings.Repeat("d", min(200, left-1)))
	}
	return b.String()
}

// TestConfigBoundsEachField holds every bounded field at its bound and one
// past: the first saved, the second refused with the field's name.
func TestConfigBoundsEachField(t *testing.T) {
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("P%03d", i)
		}
		return out
	}
	crews := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("crew-%d", i)
		}
		return out
	}
	runs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("team-%08x", i)
		}
		return out
	}
	sessions := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%016x", i)
		}
		return out
	}
	repos := func(n, each int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = longPath(each-4) + fmt.Sprintf("/%03d", i)
		}
		return out
	}
	cases := []struct {
		field    string
		at, past func(*Config)
	}{
		{"model", func(c *Config) { c.Model = "m" + strings.Repeat("x", 99) }, func(c *Config) { c.Model = "m" + strings.Repeat("x", 100) }},
		{"account", func(c *Config) { c.Account = longPath(MaxPath) }, func(c *Config) { c.Account = longPath(MaxPath + 1) }},
		{"cwd", func(c *Config) { c.Cwd = longPath(MaxPath) }, func(c *Config) { c.Cwd = longPath(MaxPath + 1) }},
		{"scope.projects", func(c *Config) { c.Scope.Projects = keys(MaxSelector) }, func(c *Config) { c.Scope.Projects = keys(MaxSelector + 1) }},
		{"scope.crews", func(c *Config) { c.Scope.Crews = crews(MaxSelector) }, func(c *Config) { c.Scope.Crews = crews(MaxSelector + 1) }},
		{"scope.runs", func(c *Config) { c.Scope.Runs = runs(MaxSelector) }, func(c *Config) { c.Scope.Runs = runs(MaxSelector + 1) }},
		{"scope.sessions", func(c *Config) { c.Scope.Sessions = sessions(MaxSelector) }, func(c *Config) { c.Scope.Sessions = sessions(MaxSelector + 1) }},
		{"scope.repos", func(c *Config) { c.Scope.Repos = repos(MaxSelector, 100) }, func(c *Config) { c.Scope.Repos = repos(MaxSelector+1, 100) }},
		// The scope's bytes: fifty repositories of 160 bytes fit, of 170 do not.
		{"scope", func(c *Config) { c.Scope.Repos = repos(MaxSelector, 155) }, func(c *Config) { c.Scope.Repos = repos(MaxSelector, 170) }},
		{"budget.turnMinutes", func(c *Config) { c.Budget.TurnMinutes = MaxTurnMinutes }, func(c *Config) { c.Budget.TurnMinutes = MaxTurnMinutes + 1 }},
		{"budget.turnMinutes", func(c *Config) { c.Budget.TurnMinutes = MinTurnMinutes }, func(c *Config) { c.Budget.TurnMinutes = MinTurnMinutes - 1 }},
		{"budget.readsPerTurn", func(c *Config) { c.Budget.ReadsPerTurn = MaxReadsPerTurn }, func(c *Config) { c.Budget.ReadsPerTurn = MaxReadsPerTurn + 1 }},
		{"budget.readsPerTurn", func(c *Config) { c.Budget.ReadsPerTurn = MinReadsPerTurn }, func(c *Config) { c.Budget.ReadsPerTurn = MinReadsPerTurn - 1 }},
		{"budget.turnsPerDay", func(c *Config) { c.Budget.TurnsPerDay = MaxTurnsPerDay }, func(c *Config) { c.Budget.TurnsPerDay = MaxTurnsPerDay + 1 }},
		{"budget.turnsPerDay", func(c *Config) { c.Budget.TurnsPerDay = MinTurnsPerDay }, func(c *Config) { c.Budget.TurnsPerDay = MinTurnsPerDay - 1 }},
	}
	ff := goodFacts()
	for _, c := range cases {
		at, past := onConfig(), onConfig()
		c.at(&at)
		c.past(&past)
		if err := Validate(at, ff); err != nil {
			t.Errorf("%s at its bound: %v", c.field, err)
		}
		inv := wantInvalid(t, Validate(past, ff), c.field)
		if inv.Code != CodeInvalidConfig {
			t.Errorf("%s: code %s", c.field, inv.Code)
		}
	}
	// The rest of the fields' shapes.
	for field, bad := range map[string]func(*Config){
		"agentId":               func(c *Config) { c.AgentID = "Claude" },
		"cwd":                   func(c *Config) { c.Cwd = "relative/dir" },
		"account":               func(c *Config) { c.Account = "/a/../b" },
		"scope.projects[0]":     func(c *Config) { c.Scope.Projects = []string{"cnd"} },
		"scope.repos[0]":        func(c *Config) { c.Scope.Repos = []string{"repo"} },
		"scope.crews[1]":        func(c *Config) { c.Scope.Crews = []string{"team", "team"} },
		"scope.runs[0]":         func(c *Config) { c.Scope.Runs = []string{"team"} },
		"scope.sessions[0]":     func(c *Config) { c.Scope.Sessions = []string{"x"} },
		"wake.mode":             func(c *Config) { c.Wake.Mode = "background" },
		"permissions.observe":   func(c *Config) { c.Permissions.Observe = false },
		"model":                 func(c *Config) { c.Model = "-m" },
		"allowConductorTools":   func(c *Config) { c.AgentID, c.AllowConductorTools = "codex", true },
		"agentId (on, without)": func(c *Config) { c.AgentID = "" },
	} {
		c := onConfig()
		bad(&c)
		wantInvalid(t, Validate(c, ff), strings.Fields(field)[0])
	}
	// What only the server can check is asked of Facts, with its words.
	c := onConfig()
	c.Account = "/home/me/.claude-work"
	f := goodFacts()
	f.accountErr = errors.New("not a directory you own")
	if inv := wantInvalid(t, Validate(c, f), "account"); !strings.Contains(inv.Why, "not a directory you own") {
		t.Errorf("account: %s", inv)
	}
	f = goodFacts()
	f.cwdErr = errors.New("no such folder")
	if inv := wantInvalid(t, Validate(onConfig(), f), "cwd"); !strings.Contains(inv.Why, "no such folder") {
		t.Errorf("cwd: %s", inv)
	}
	// Off, a configuration may name nothing.
	off := DefaultConfig("")
	if err := Validate(off, goodFacts()); err != nil {
		t.Errorf("the defaults with no folder: %v", err)
	}
}

// TestAModelNeedsAnAdapterThatTakesOne: a model goes only to an agent
// whose adapter has a model flag, and is one argument of that flag.
func TestAModelNeedsAnAdapterThatTakesOne(t *testing.T) {
	f := goodFacts()
	f.agents["pi"] = AgentFacts{MCP: true, ReadOnly: true}
	for _, m := range []string{"opus", "claude-opus-5-5", "gpt-6-astra", "openai/gpt-6@2026-10", "a:b.c_d-e"} {
		c := onConfig()
		c.Model = m
		if err := Validate(c, f); err != nil {
			t.Errorf("model %s: %v", m, err)
		}
	}
	for _, m := range []string{"--dangerously-skip-permissions", "-m", "a b", "opus\n", ".x", "x;rm"} {
		c := onConfig()
		c.Model = m
		wantInvalid(t, Validate(c, f), "model")
	}
	c := onConfig()
	c.AgentID, c.Model = "pi", "anything"
	if inv := wantInvalid(t, Validate(c, f), "model"); !strings.Contains(inv.Why, "takes no model") {
		t.Errorf("pi with a model: %s", inv)
	}
	c.Model = ""
	if err := Validate(c, f); err != nil {
		t.Errorf("pi with its default model: %v", err)
	}
	c = DefaultConfig("/srv/work")
	c.Model = "opus"
	wantInvalid(t, Validate(c, f), "model")
}

// TestAnAgentWithoutAReadOnlyModeIsRefused: Brakeman's agent is in the
// catalog, not hidden, registers Conductor's MCP server and has a
// read-only mode.
func TestAnAgentWithoutAReadOnlyModeIsRefused(t *testing.T) {
	f := goodFacts()
	f.agents["goose"] = AgentFacts{MCP: true}
	f.agents["amp"] = AgentFacts{ReadOnly: true}
	f.agents["shy"] = AgentFacts{Hidden: true, MCP: true, ReadOnly: true}
	for id, words := range map[string]string{
		"goose":   "no read-only mode",
		"amp":     "MCP server",
		"shy":     "hidden",
		"nowhere": "no agent",
	} {
		c := onConfig()
		c.AgentID = id
		if inv := wantInvalid(t, Validate(c, f), "agentId"); !strings.Contains(inv.Why, words) {
			t.Errorf("%s: %s", id, inv)
		}
		// Off, the agent is held to the same: it is what turns would run.
		c.Enabled = false
		wantInvalid(t, Validate(c, f), "agentId")
	}
}

// TestAReadOnlyModeThatDoesNotHoldIsAWarning: an agent whose read-only
// mode does not hold (Codex 0.163.0's) is taken, and Warnings says what
// it leaves in the adapter's own words; one whose mode holds has none.
func TestAReadOnlyModeThatDoesNotHoldIsAWarning(t *testing.T) {
	f := goodFacts()
	f.agents["leaky"] = AgentFacts{MCP: true, ReadOnly: true, ReadOnlyGap: "your own MCP servers run outside the sandbox"}
	c := onConfig()
	c.AgentID = "leaky"
	if err := Validate(c, f); err != nil {
		t.Fatalf("a mode that does not hold was refused: %v", err)
	}
	w := Warnings(c, f)
	if len(w) != 1 || !strings.Contains(w[0], "does not hold: your own MCP servers run outside the sandbox") {
		t.Fatalf("%q", w)
	}
	if w := Warnings(onConfig(), f); w != nil {
		t.Fatalf("a mode that holds warns %q", w)
	}
	c.AgentID = ""
	if w := Warnings(c, f); w != nil {
		t.Fatalf("no agent warns %q", w)
	}
}

// TestAConfigSaveNamesItsRevision: a save names the rev it read, else
// stale_revision; each save bumps it and stamps its time.
func TestAConfigSaveNamesItsRevision(t *testing.T) {
	fx := newFixture(t)
	c, err := fx.s.Config()
	if err != nil || c.Rev != 1 {
		t.Fatalf("first read: rev %d %v", c.Rev, err)
	}
	c.Enabled, c.AgentID = true, "claude"
	fx.clock.add(1e9)
	saved, err := fx.s.SaveConfig(c, goodFacts())
	if err != nil || saved.Rev != 2 || !saved.UpdatedAt.Equal(fx.clock.now()) {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err := fx.s.SaveConfig(c, goodFacts()); !errors.Is(err, ErrStale) {
		t.Fatalf("a save of rev 1 over rev 2: %v", err)
	}
	saved.Budget.TurnsPerDay = 5
	again, err := fx.s.SaveConfig(saved, goodFacts())
	if err != nil || again.Rev != 3 {
		t.Fatalf("a save of rev 2: %+v %v", again, err)
	}
	fx.reopen()
	if c, err := fx.s.Config(); err != nil || c.Rev != 3 || c.Budget.TurnsPerDay != 5 {
		t.Fatalf("after a restart: %+v %v", c, err)
	}
	// An invalid save writes nothing.
	again.Budget.TurnsPerDay = 0
	if _, err := fx.s.SaveConfig(again, goodFacts()); err == nil {
		t.Fatal("an invalid configuration was saved")
	}
	if c, _ := fx.s.Config(); c.Rev != 3 {
		t.Fatalf("an invalid save moved the rev to %d", c.Rev)
	}
}

// TestTheDefaultsAreOffAndTerminalsHidden: the first read writes the
// defaults: off, no agent, the server's folder, every visibility on but
// terminals, all of scope, observe, draft and propose, the budget, invoked.
func TestTheDefaultsAreOffAndTerminalsHidden(t *testing.T) {
	fx := newFixture(t)
	c, err := fx.s.Config()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Cwd:         "/srv/work",
		Visibility:  Visibility{Runs: true, Logs: true, Dispatch: true, Sessions: true, Touched: true},
		Permissions: Permissions{Observe: true, Draft: true, Propose: true},
		Wake:        Wake{Mode: WakeInvoked},
		Budget:      Budget{TurnMinutes: 15, ReadsPerTurn: 300, TurnsPerDay: 40},
		Rev:         1,
		UpdatedAt:   fx.clock.now().UTC(),
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("defaults:\n got %+v\nwant %+v", c, want)
	}
	if _, err := os.Stat(filepath.Join(fx.data.Dir(), Dir, ConfigFile)); err != nil {
		t.Fatalf("the defaults were not written: %v", err)
	}
	for _, class := range Classes {
		if c.Visibility.Has(class) == (class == ClassTerminals) {
			t.Errorf("class %s visible %v", class, c.Visibility.Has(class))
		}
	}
}

// TestActMustBeEmptyInThisBuild: no operation can be granted before BK2.
func TestActMustBeEmptyInThisBuild(t *testing.T) {
	c := onConfig()
	c.Permissions.Act = []string{"run_retry"}
	if inv := wantInvalid(t, Validate(c, goodFacts()), "permissions.act"); !strings.Contains(inv.Why, "must be empty") {
		t.Errorf("act: %s", inv)
	}
	c.Enabled = false
	wantInvalid(t, Validate(c, goodFacts()), "permissions.act")
}

// TestScopeAdmits: each selector alone and together, a session in no run,
// a ticket linked to no run, a repository root.
func TestScopeAdmits(t *testing.T) {
	run := ScopeItem{Kind: ItemRun, Run: "team-00000001", Crew: "team", Repo: "/src/app", Project: "CND"}
	unlinked := ScopeItem{Kind: ItemRun, Run: "solo-00000002", Crew: "solo", Repo: "/src/app"}
	member := ScopeItem{Kind: ItemSession, Session: sessA, Run: run.Run, Crew: run.Crew, Repo: "/src/app", Project: "CND"}
	alone := ScopeItem{Kind: ItemSession, Session: "1111111111111111", Repo: "/src/other"}
	norepo := ScopeItem{Kind: ItemSession, Session: "2222222222222222"}
	ticket := ScopeItem{Kind: ItemTicket, Project: "CND", Links: []RunLink{{Run: run.Run, Crew: run.Crew}}}
	lone := ScopeItem{Kind: ItemTicket, Project: "CND"}
	other := ScopeItem{Kind: ItemTicket, Project: "OPS", Links: []RunLink{{Run: "x-00000003", Crew: "x"}, {Run: run.Run, Crew: run.Crew}}}
	items := []ScopeItem{run, unlinked, member, alone, norepo, ticket, lone, other}
	names := []string{"run", "unlinked run", "member", "standalone session", "session in no repository", "ticket", "unlinked ticket", "other project's ticket"}
	cases := []struct {
		name  string
		scope Scope
		want  []bool
	}{
		{"all of it", Scope{}, []bool{true, true, true, true, true, true, true, true}},
		{"projects", Scope{Projects: []string{"CND"}}, []bool{true, false, true, false, false, true, true, false}},
		{"repos", Scope{Repos: []string{"/src/app"}}, []bool{true, true, true, false, false, true, true, true}},
		{"crews", Scope{Crews: []string{"team"}}, []bool{true, false, true, false, false, true, false, true}},
		{"runs", Scope{Runs: []string{"team-00000001"}}, []bool{true, false, true, false, false, true, false, true}},
		{"sessions", Scope{Sessions: []string{sessA}}, []bool{true, true, true, false, false, true, true, true}},
		{"a standalone session named", Scope{Sessions: []string{"1111111111111111"}}, []bool{true, true, false, true, false, true, true, true}},
		{"runs and a session named", Scope{Runs: []string{"team-00000001"}, Sessions: []string{"1111111111111111"}}, []bool{true, false, false, true, false, true, false, true}},
		{"crews and projects", Scope{Crews: []string{"team", "x"}, Projects: []string{"OPS"}}, []bool{false, false, false, false, false, false, false, true}},
		{"a repository and a run", Scope{Repos: []string{"/src/other"}, Runs: []string{"team-00000001"}}, []bool{false, false, false, false, false, true, false, true}},
	}
	for _, c := range cases {
		for i, it := range items {
			if got := c.scope.Admits(it); got != c.want[i] {
				t.Errorf("%s: %s admitted %v", c.name, names[i], got)
			}
		}
	}
	if (Scope{}).Admits(ScopeItem{Kind: "brief"}) {
		t.Error("an item of no kind Admits knows was admitted")
	}
}

// TestCoversAgreesWithAdmits: for every pair of scopes over a small
// universe (two values per selector, and each empty), Covers is true
// exactly when every item the earlier scope admits the later admits too,
// over runs, run members, standalone sessions and tickets linked and
// unlinked, whose values range over a third value too.
func TestCoversAgreesWithAdmits(t *testing.T) {
	vals := []string{"a", "b", "c"}
	optional := []string{"a", "b", "c", ""}
	var universe []ScopeItem
	for _, run := range vals {
		for _, crew := range vals {
			for _, repo := range optional {
				for _, project := range optional {
					universe = append(universe, ScopeItem{Kind: ItemRun, Run: run, Crew: crew, Repo: repo, Project: project})
					for _, sess := range vals {
						universe = append(universe, ScopeItem{Kind: ItemSession, Session: sess, Run: run, Crew: crew, Repo: repo, Project: project})
					}
				}
			}
		}
	}
	for _, sess := range vals {
		for _, repo := range optional {
			universe = append(universe, ScopeItem{Kind: ItemSession, Session: sess, Repo: repo})
		}
	}
	links := [][]RunLink{nil}
	for _, run := range vals {
		for _, crew := range vals {
			links = append(links, []RunLink{{Run: run, Crew: crew}})
		}
	}
	links = append(links, []RunLink{{Run: "a", Crew: "b"}, {Run: "b", Crew: "a"}})
	for _, project := range vals {
		for _, l := range links {
			universe = append(universe, ScopeItem{Kind: ItemTicket, Project: project, Links: l})
		}
	}
	subsets := [][]string{nil, {"a"}, {"b"}, {"a", "b"}}
	var scopes []Scope
	for _, p := range subsets {
		for _, r := range subsets {
			for _, c := range subsets {
				for _, u := range subsets {
					for _, s := range subsets {
						scopes = append(scopes, Scope{Projects: p, Repos: r, Crews: c, Runs: u, Sessions: s})
					}
				}
			}
		}
	}
	words := (len(universe) + 63) / 64
	admitted := make([][]uint64, len(scopes))
	for i, s := range scopes {
		admitted[i] = make([]uint64, words)
		for j, it := range universe {
			if s.Admits(it) {
				admitted[i][j/64] |= 1 << (j % 64)
			}
		}
	}
	mismatches := 0
	for i, earlier := range scopes {
		for j, now := range scopes {
			brute := true
			for w := range words {
				if admitted[i][w]&^admitted[j][w] != 0 {
					brute = false
					break
				}
			}
			if got := now.Covers(earlier); got != brute {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("Covers(%+v over %+v) = %v, the brute force says %v", now, earlier, got, brute)
				}
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d pairs disagree", mismatches, len(scopes)*len(scopes))
	}
	// The case the plan names: Runs=[r], Sessions=[s] narrowed to Runs=[r]
	// withholds what the standalone session s gave.
	if (Scope{Runs: []string{"r"}}).Covers(Scope{Runs: []string{"r"}, Sessions: []string{"s"}}) {
		t.Fatal("narrowing away a standalone session covered the earlier scope")
	}
}

// TestObserveOnlyRefusesDraftsAndProposals: with draft off, a draft
// ticket, a draft crew and a handoff are refused; with propose off, a
// proposal and a decision card; a fact always passes.
func TestObserveOnlyRefusesDraftsAndProposals(t *testing.T) {
	drafts := []Item{draftTicket(t, "CND", "Split the parser"), draftCrew(), handoff(t)}
	proposals := []Item{proposal(t), decision(t)}
	observe := Permissions{Observe: true}
	for _, it := range append(drafts, proposals...) {
		b := goodBrief(t)
		b.Items = append(b.Items, it)
		if err := CheckBrief(&b, allowAll, goodFacts()); err != nil {
			t.Fatalf("%s with every permission: %v", it.Kind, err)
		}
		b = goodBrief(t)
		b.Items = append(b.Items, it)
		wantInvalid(t, CheckBrief(&b, observe, goodFacts()), "items[1].kind")
	}
	for _, it := range drafts {
		b := goodBrief(t)
		b.Items = append(b.Items, it)
		wantInvalid(t, CheckBrief(&b, Permissions{Observe: true, Propose: true}, goodFacts()), "items[1].kind")
	}
	for _, it := range proposals {
		b := goodBrief(t)
		b.Items = append(b.Items, it)
		wantInvalid(t, CheckBrief(&b, Permissions{Observe: true, Draft: true}, goodFacts()), "items[1].kind")
	}
	b := goodBrief(t)
	if err := CheckBrief(&b, observe, goodFacts()); err != nil {
		t.Fatalf("a fact, observe only: %v", err)
	}
}
