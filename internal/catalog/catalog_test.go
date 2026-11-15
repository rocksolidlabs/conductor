package catalog

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// builtIns is the IDs of the built-in agents in order, comma separated.
const builtIns = "claude,codex,agy,copilot,cursor,opencode,pi,omp,aider,goose,amp,dsh,shell"

func TestDefaultContainsBuiltIns(t *testing.T) {
	if got := idsOf(Default()); got != builtIns {
		t.Fatalf("built-ins %s, want %s", got, builtIns)
	}
}

func TestLoadMergesByID(t *testing.T) {
	c, err := Load(File{Agents: []Agent{
		{ID: "claude", Name: "Claude (pinned)", Command: []string{"/opt/claude", "--model", "opus"}},
		{ID: "my-tool", Name: "My tool", Command: []string{"mytool"}, Env: map[string]string{"TOKEN": "x"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Get("claude")
	if a.Name != "Claude (pinned)" || a.Command[0] != "/opt/claude" {
		t.Fatalf("override not applied: %+v", a)
	}
	if len(c.List()) != 14 {
		t.Fatalf("expected 14 agents, got %d", len(c.List()))
	}
	if r, _ := c.Get("my-tool"); r.Redacted().Env["TOKEN"] != "***" {
		t.Fatal("env not redacted")
	}
}

func TestLoadDisableDefaults(t *testing.T) {
	c, err := Load(File{DisableDefaults: true, Agents: []Agent{{ID: "x", Name: "X", Command: []string{"x"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(c.List()))
	}
	if _, err := Load(File{DisableDefaults: true}); err == nil {
		t.Fatal("expected error for empty catalog")
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := []Agent{
		{ID: "Bad ID", Name: "x", Command: []string{"x"}},
		{ID: "ok", Name: "", Command: []string{"x"}},
		{ID: "ok", Name: "x", Command: nil},
		{ID: "ok", Name: "x", Command: []string{"x\x00y"}},
		{ID: "ok", Name: "x", Command: []string{"x"}, Env: map[string]string{"A=B": "c"}},
		{ID: "ok", Name: "x", Command: []string{"x"}, Signal: &Signal{Kind: "nope"}},
		{ID: "ok", Name: "x", Command: []string{"x"}, EnvPassthrough: []string{"not a name"}},
		{ID: "ok", Name: strings.Repeat("a", 61), Command: []string{"x"}},
	}
	for i, a := range cases {
		if _, err := Load(File{Agents: []Agent{a}}); err == nil {
			t.Fatalf("case %d: expected error", i)
		} else if !strings.Contains(err.Error(), "agents[0]") {
			t.Fatalf("case %d: unexpected error %v", i, err)
		}
	}
}

func TestSignalValidation(t *testing.T) {
	base := Agent{ID: "x", Name: "X", Command: []string{"x"}}
	ok := base
	ok.Signal = &Signal{Kind: "pattern", Pattern: `^> $`}
	if err := validate(ok); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Signal{{Kind: "nope"}, {Kind: "pattern"}, {Kind: "pattern", Pattern: "("}, {Kind: "pattern", Pattern: strings.Repeat("a", 201)}, {Kind: "bell", Pattern: "x"}} {
		a := base
		a.Signal = &s
		if err := validate(a); err == nil {
			t.Fatalf("signal %+v accepted", s)
		}
	}
	if got := base.EffectiveSignal(); got.Kind != "bell" {
		t.Fatalf("default signal %+v", got)
	}
}

// CompilePattern is the one rule for a screen pattern, for the catalog and
// for `conductor host --signal-pattern` alike.
func TestCompilePattern(t *testing.T) {
	re, err := CompilePattern(`^> $`)
	if err != nil || !re.MatchString("> ") || re.MatchString(">") {
		t.Fatalf("CompilePattern(^> $) = %v, %v", re, err)
	}
	if _, err := CompilePattern(strings.Repeat("a", 200)); err != nil {
		t.Fatalf("a pattern of 200 bytes was rejected: %v", err)
	}
	for _, p := range []string{"", "(", `(?P<n>`, `a**`, strings.Repeat("a", 201)} {
		if re, err := CompilePattern(p); err == nil || re != nil {
			t.Fatalf("CompilePattern(%.20q) = %v, %v; want an error", p, re, err)
		}
	}
}

// A pattern that matches an empty line would match a screen that shows
// nothing, and mark every quiet session as waiting.
func TestCompilePatternRejectsAPatternThatMatchesAnEmptyLine(t *testing.T) {
	for _, p := range []string{`^$`, `.*`, `x*`, `(?:)`, `\s*$`, `^\s*$`, `a?`, `$`, `(?i)`, `^|x`} {
		re, err := CompilePattern(p)
		if err == nil || re != nil || !strings.Contains(err.Error(), "empty line") {
			t.Fatalf("CompilePattern(%q) = %v, %v; want an error about an empty line", p, re, err)
		}
		a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Signal: &Signal{Kind: "pattern", Pattern: p}}
		if err := validate(a); err == nil {
			t.Fatalf("an agent whose signal pattern is %q was accepted", p)
		}
	}
	// A pattern that needs some text stays valid, whatever else it says.
	for _, p := range []string{`^> $`, `\? $`, `\(Y\)es/\(N\)o\s*$`, `x+`, `\S`, `\b|x`} {
		if _, err := CompilePattern(p); err != nil {
			t.Fatalf("CompilePattern(%q): %v", p, err)
		}
	}
}

func TestOverlayUpsertsAndHides(t *testing.T) {
	c := Default()
	if err := c.ApplyOverlay(Overlay{
		Agents: []Agent{{ID: "claude", Name: "Claude (opus)", Command: []string{"claude", "--model", "opus"}, AllowArgs: true}, {ID: "zed", Name: "Zed", Command: []string{"zed"}}},
		Hidden: []string{"shell"},
	}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("claude"); a.Name != "Claude (opus)" || a.Command[2] != "opus" {
		t.Fatalf("upsert did not replace: %+v", a)
	}
	if _, ok := c.Get("shell"); ok {
		t.Fatal("hidden agent still visible")
	}
	if _, ok := c.Get("zed"); !ok {
		t.Fatal("new agent missing")
	}
	ids := []string{}
	for _, a := range c.List() {
		ids = append(ids, a.ID)
	}
	if ids[0] != "claude" || ids[len(ids)-1] != "zed" {
		t.Fatalf("order: %v", ids)
	}
	if err := c.Upsert(Agent{ID: "Bad ID", Name: "x", Command: []string{"x"}}); err == nil {
		t.Fatal("invalid id accepted")
	}
}

// idsOf lists a catalog's agent IDs in order, comma separated.
func idsOf(c Catalog) string {
	ids := []string{}
	for _, a := range c.List() {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ",")
}

// A signal's workingTitle, with any kind, is RE2 of at most 200 bytes that
// does not match an empty title: a title cut at the session's capture, or
// one a program writes to clear its own, is empty.
func TestWorkingTitleValidation(t *testing.T) {
	for _, kind := range []string{SignalHook, SignalBell, SignalNone} {
		a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Signal: &Signal{Kind: kind, WorkingTitle: CodexWorkingTitle}}
		if err := validate(a); err != nil {
			t.Fatalf("kind %s with Codex's working title: %v", kind, err)
		}
	}
	a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Signal: &Signal{Kind: SignalPattern, Pattern: `^> $`, WorkingTitle: `^Busy$`}}
	if err := validate(a); err != nil {
		t.Fatalf("a pattern signal with a working title: %v", err)
	}
	if _, err := CompileWorkingTitle(strings.Repeat("a", 200)); err != nil {
		t.Fatalf("a working title of 200 bytes was rejected: %v", err)
	}
	for _, p := range []string{"(", strings.Repeat("a", 201), `.*`, `^$`, `x*`, `(?:)`, `^\s*$`, `^|Working`} {
		a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Signal: &Signal{Kind: SignalHook, WorkingTitle: p}}
		err := validate(a)
		if err == nil || !strings.Contains(err.Error(), "workingTitle") {
			t.Fatalf("working title %.20q: %v", p, err)
		}
	}
	if _, err := CompileWorkingTitle(`^$`); err == nil || !strings.Contains(err.Error(), "empty title") {
		t.Fatalf("a working title matching an empty title: %v", err)
	}
}

// Codex's working title matches its title through a turn, with the items
// Conductor's launch asks for (a frame of its spinner and the run state;
// the word alone with animations off), and none of its others: Starting
// while its MCP servers start, Ready at rest, Action Required and its blink,
// nor another program's.
func TestCodexWorkingTitleMatchesItsWorkingForms(t *testing.T) {
	re, err := CompileWorkingTitle(CodexWorkingTitle)
	if err != nil {
		t.Fatal(err)
	}
	var working []string
	for _, frame := range []string{"⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠋"} {
		for _, state := range []string{"Working", "Thinking", "Waiting"} {
			working = append(working, frame+" "+state)
		}
	}
	working = append(working, "Working", "Thinking", "Waiting")
	for _, title := range working {
		if !re.MatchString(title) {
			t.Errorf("%q does not match", title)
		}
	}
	for _, title := range []string{"", "Ready", "⠸ Starting", "Starting", "[ ! ] Action Required", "[ . ] Action Required",
		"[ ! ] Action Required | Working", "⠋ conductor", "codex | Working", "Working on it", "vim - Working", "working"} {
		if re.MatchString(title) {
			t.Errorf("%q matches", title)
		}
	}
}

// A saved agent whose signal leaves the working title out (the Agents page
// has no control for it; a version before it saved none) keeps the one of
// the agent it replaces; one that names its own keeps it.
func TestOverlayInheritsTheWorkingTitle(t *testing.T) {
	c := Default()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "codex", Name: "Codex", Command: []string{"codex", "--model", "o3"}, Signal: &Signal{Kind: SignalHook}}}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("codex"); a.Signal == nil || a.Signal.Kind != SignalHook || a.Signal.WorkingTitle != CodexWorkingTitle {
		t.Fatalf("saved codex signal %+v", a.Signal)
	}
	c = Default()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "codex", Name: "Codex", Command: []string{"codex"}, Signal: &Signal{Kind: SignalBell, WorkingTitle: `^Busy$`}}}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("codex"); a.Signal.Kind != SignalBell || a.Signal.WorkingTitle != `^Busy$` {
		t.Fatalf("saved codex signal %+v", a.Signal)
	}
	if b, _ := Default().Get("codex"); b.Signal.WorkingTitle != CodexWorkingTitle {
		t.Fatalf("the built-in changed: %+v", b.Signal)
	}
}

func TestSignalKinds(t *testing.T) {
	base := Agent{ID: "x", Name: "X", Command: []string{"x"}}
	valid := []Signal{
		{Kind: "hook"},
		{Kind: "bell"},
		{Kind: "none"},
		{Kind: "hook", ToolEvents: true},
		{Kind: "none", ToolEvents: true},
		{Kind: "pattern", Pattern: `^> $`, ToolEvents: true},
		{Kind: "pattern", Pattern: strings.Repeat("a", 200)},
	}
	for _, s := range valid {
		a := base
		a.Signal = &s
		if err := validate(a); err != nil {
			t.Fatalf("signal %+v rejected: %v", s, err)
		}
	}
	for _, s := range []Signal{{}, {Kind: "Hook"}, {Kind: "hook", Pattern: "x"}, {Kind: "none", Pattern: "x"}} {
		a := base
		a.Signal = &s
		if err := validate(a); err == nil {
			t.Fatalf("signal %+v accepted", s)
		}
	}
}

func TestEffectiveSignal(t *testing.T) {
	var a Agent
	if got := a.EffectiveSignal(); got != (Signal{Kind: "bell"}) {
		t.Fatalf("default signal %+v", got)
	}
	a.Signal = &Signal{Kind: "hook", ToolEvents: true}
	if got := a.EffectiveSignal(); got != *a.Signal {
		t.Fatalf("explicit signal %+v", got)
	}
}

func TestEnvPassthroughValidation(t *testing.T) {
	base := Agent{ID: "x", Name: "X", Command: []string{"x"}}
	ok := base
	// Proxy variables are lower case by convention, so both cases must pass.
	ok.EnvPassthrough = []string{"HTTP_PROXY", "_PRIVATE", "OPENAI_API_KEY", "A1", "http_proxy", "https_proxy", "no_proxy", "Mixed_Case9"}
	if err := validate(ok); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "1BAD", "1bad", "BAD-NAME", "bad-name", "A=B", "A B", "A\n", "A\x00", "$HOME"} {
		a := base
		a.EnvPassthrough = []string{"OK", name}
		if err := validate(a); err == nil {
			t.Fatalf("env name %q accepted", name)
		}
	}
	full := base
	for i := 1; i <= 32; i++ {
		full.EnvPassthrough = append(full.EnvPassthrough, strings.Repeat("V", i))
	}
	if err := validate(full); err != nil {
		t.Fatalf("32 entries rejected: %v", err)
	}
	full.EnvPassthrough = append(full.EnvPassthrough, "ONE_MORE")
	if err := validate(full); err == nil {
		t.Fatal("33 entries accepted")
	}
}

func TestSizeLimits(t *testing.T) {
	// Name and description are measured in characters, so multibyte text counts
	// once per rune. Command elements are measured in bytes, so the same text
	// counts in full.
	const (
		twoByte  = "\u00e9"     // one character, 2 bytes
		fourByte = "\U0001F600" // one character, 4 bytes
	)
	env := func(n int) map[string]string {
		m := make(map[string]string, n)
		for i := 1; i <= n; i++ {
			m[strings.Repeat("K", i)] = "v"
		}
		return m
	}
	cases := []struct {
		name   string
		change func(*Agent)
		field  string // the field the error must name; empty when the agent is valid
	}{
		{"name 60 characters", func(a *Agent) { a.Name = strings.Repeat("a", 60) }, ""},
		{"name 61 characters", func(a *Agent) { a.Name = strings.Repeat("a", 61) }, "name"},
		{"name 60 two-byte characters", func(a *Agent) { a.Name = strings.Repeat(twoByte, 60) }, ""},
		{"name 61 two-byte characters", func(a *Agent) { a.Name = strings.Repeat(twoByte, 61) }, "name"},
		{"name 60 four-byte characters", func(a *Agent) { a.Name = strings.Repeat(fourByte, 60) }, ""},
		{"name 61 four-byte characters", func(a *Agent) { a.Name = strings.Repeat(fourByte, 61) }, "name"},
		{"description 200 characters", func(a *Agent) { a.Description = strings.Repeat("a", 200) }, ""},
		{"description 201 characters", func(a *Agent) { a.Description = strings.Repeat("a", 201) }, "description"},
		{"description 200 two-byte characters", func(a *Agent) { a.Description = strings.Repeat(twoByte, 200) }, ""},
		{"description 201 two-byte characters", func(a *Agent) { a.Description = strings.Repeat(twoByte, 201) }, "description"},
		{"command 32 elements", func(a *Agent) { a.Command = slices.Repeat([]string{"x"}, 32) }, ""},
		{"command 33 elements", func(a *Agent) { a.Command = slices.Repeat([]string{"x"}, 33) }, "command"},
		{"command element 4096 bytes", func(a *Agent) { a.Command = []string{strings.Repeat("a", 4096)} }, ""},
		{"command element 4097 bytes", func(a *Agent) { a.Command = []string{strings.Repeat("a", 4097)} }, "command"},
		{"later command element 4097 bytes", func(a *Agent) { a.Command = []string{"x", strings.Repeat("a", 4097)} }, "command"},
		{"command element 2048 two-byte characters", func(a *Agent) { a.Command = []string{strings.Repeat(twoByte, 2048)} }, ""},
		{"command element 2049 two-byte characters", func(a *Agent) { a.Command = []string{strings.Repeat(twoByte, 2049)} }, "command"},
		{"env 32 keys", func(a *Agent) { a.Env = env(32) }, ""},
		{"env 33 keys", func(a *Agent) { a.Env = env(33) }, "env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := Agent{ID: "x", Name: "X", Command: []string{"x"}}
			c.change(&a)
			err := validate(a)
			switch {
			case c.field == "" && err != nil:
				t.Fatalf("valid agent rejected: %v", err)
			case c.field != "" && err == nil:
				t.Fatal("agent over the limit accepted")
			case c.field != "" && !strings.Contains(err.Error(), c.field):
				t.Fatalf("error does not name %q: %v", c.field, err)
			}
		})
	}
}

func TestUpsertKeepsPositionAndHideRemoves(t *testing.T) {
	c := Default()
	if err := c.Upsert(Agent{ID: "codex", Name: "Codex (pinned)", Command: []string{"codex", "--yolo"}}); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(c); got != builtIns {
		t.Fatalf("replacing an agent moved it: %s", got)
	}
	if err := c.Upsert(Agent{ID: "zed", Name: "Zed", Command: []string{"zed"}}); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(c); got != builtIns+",zed" {
		t.Fatalf("new agent not appended: %s", got)
	}
	if !c.Hide("agy") {
		t.Fatal("hiding an existing agent reported false")
	}
	withoutAgy := strings.Replace(builtIns, "agy,", "", 1)
	if got := idsOf(c); got != withoutAgy+",zed" {
		t.Fatalf("hide left the wrong order: %s", got)
	}
	if c.Hide("agy") || c.Hide("never-there") {
		t.Fatal("hiding an unknown agent reported true")
	}
	// A hidden agent can come back; it is a new entry, so it goes last.
	if err := c.Upsert(Agent{ID: "agy", Name: "Antigravity", Command: []string{"agy"}}); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(c); got != withoutAgy+",zed,agy" {
		t.Fatalf("re-added agent misplaced: %s", got)
	}
	// A rejected upsert changes nothing.
	if err := c.Upsert(Agent{ID: "claude", Name: "", Command: []string{"claude"}}); err == nil {
		t.Fatal("empty name accepted")
	}
	if a, _ := c.Get("claude"); a.Name != "Claude Code" {
		t.Fatalf("rejected upsert modified the entry: %+v", a)
	}
}

func TestApplyOverlayIsAllOrNothing(t *testing.T) {
	c := Default()
	err := c.ApplyOverlay(Overlay{
		Agents: []Agent{
			{ID: "fine", Name: "Fine", Command: []string{"fine"}},
			{ID: "Bad ID", Name: "Bad", Command: []string{"bad"}},
		},
		Hidden: []string{"shell"},
	})
	if err == nil || !strings.Contains(err.Error(), "agents[1]") {
		t.Fatalf("expected an error naming agents[1], got %v", err)
	}
	if got := idsOf(c); got != builtIns {
		t.Fatalf("failed overlay changed the catalog: %s", got)
	}
	if _, ok := c.Get("fine"); ok {
		t.Fatal("failed overlay left its first agent behind")
	}
}

func TestApplyOverlayHidesLast(t *testing.T) {
	c := Default()
	// Unknown ids are not an error, and an empty overlay is a no-op.
	if err := c.ApplyOverlay(Overlay{Hidden: []string{"nope"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyOverlay(Overlay{}); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(c); got != builtIns {
		t.Fatalf("no-op overlay changed the catalog: %s", got)
	}
	// Agents apply first, so hiding wins over an agent with the same id.
	if err := c.ApplyOverlay(Overlay{
		Agents: []Agent{{ID: "zed", Name: "Zed", Command: []string{"zed"}}},
		Hidden: []string{"zed", "shell"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(c); got != strings.TrimSuffix(builtIns, ",shell") {
		t.Fatalf("hidden did not win: %s", got)
	}
}

func TestCloneIsIndependent(t *testing.T) {
	orig, err := Load(File{Agents: []Agent{{
		ID: "tool", Name: "Tool", Command: []string{"tool", "--flag"},
		Env:            map[string]string{"TOKEN": "x"},
		EnvPassthrough: []string{"TOOL_HOME"},
		Signal:         &Signal{Kind: "pattern", Pattern: `^> $`},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	clone := orig.Clone()
	if !reflect.DeepEqual(orig.List(), clone.List()) {
		t.Fatalf("clone differs from the original:\n%+v\n%+v", clone.List(), orig.List())
	}

	// Structural edits to the clone do not reach the original.
	clone.Hide("claude")
	if err := clone.Upsert(Agent{ID: "extra", Name: "Extra", Command: []string{"extra"}}); err != nil {
		t.Fatal(err)
	}
	// Neither do edits made in place to an agent's slices, map or signal.
	a, _ := clone.Get("tool")
	a.Command[0] = "changed"
	a.Env["TOKEN"] = "changed"
	a.EnvPassthrough[0] = "CHANGED"
	a.Signal.Pattern = "changed"

	if got := idsOf(orig); got != builtIns+",tool" {
		t.Fatalf("original order changed: %s", got)
	}
	b, _ := orig.Get("tool")
	if b.Command[0] != "tool" || b.Env["TOKEN"] != "x" || b.EnvPassthrough[0] != "TOOL_HOME" || b.Signal.Pattern != `^> $` {
		t.Fatalf("original agent changed through the clone: %+v signal %+v", b, b.Signal)
	}
}

func TestZeroCatalogIsUsable(t *testing.T) {
	var c Catalog
	if c.Hide("x") || len(c.List()) != 0 {
		t.Fatal("zero catalog is not empty")
	}
	clone := c.Clone()
	if err := clone.Upsert(Agent{ID: "x", Name: "X", Command: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Upsert(Agent{ID: "y", Name: "Y", Command: []string{"y"}}); err != nil {
		t.Fatal(err)
	}
	if idsOf(c) != "y" || idsOf(clone) != "x" {
		t.Fatalf("zero catalog and its clone share state: %q %q", idsOf(c), idsOf(clone))
	}
}

func TestRedactedKeepsRoutingFields(t *testing.T) {
	a := Agent{
		ID: "x", Name: "X", Command: []string{"x"},
		Env:            map[string]string{"TOKEN": "secret"},
		EnvPassthrough: []string{"HOME_TOKEN"},
		Adapter:        "claude",
		Signal:         &Signal{Kind: "pattern", Pattern: `^> $`, ToolEvents: true},
	}
	r := a.Redacted()
	if r.Env["TOKEN"] != "***" {
		t.Fatalf("env not redacted: %v", r.Env)
	}
	if a.Env["TOKEN"] != "secret" {
		t.Fatal("redaction changed the original")
	}
	if r.Adapter != "claude" || !slices.Equal(r.EnvPassthrough, a.EnvPassthrough) || r.Signal == nil || *r.Signal != *a.Signal {
		t.Fatalf("redaction dropped non-secret fields: %+v", r)
	}
}

func TestAgentJSONShape(t *testing.T) {
	overlay := Overlay{
		Agents: []Agent{
			{
				ID: "x", Name: "X", Command: []string{"x"},
				EnvPassthrough: []string{"FOO"},
				Adapter:        "claude",
				Signal:         &Signal{Kind: "pattern", Pattern: "ready$", ToolEvents: true},
			},
			{ID: "y", Name: "Y", Command: []string{"y"}, Signal: &Signal{Kind: "hook"}},
		},
		Hidden: []string{"shell"},
	}
	b, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"agents":[` +
		`{"id":"x","name":"X","command":["x"],"allowArgs":false,"envPassthrough":["FOO"],"adapter":"claude","signal":{"kind":"pattern","pattern":"ready$","toolEvents":true}},` +
		`{"id":"y","name":"Y","command":["y"],"allowArgs":false,"signal":{"kind":"hook"}}` +
		`],"hidden":["shell"]}`
	if string(b) != want {
		t.Fatalf("json shape changed:\n got %s\nwant %s", b, want)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var back Overlay
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, overlay) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", back, overlay)
	}
}

func TestReadFileRejectsTrailingData(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"agents":[]} x`, "after the JSON value"},
		{`{"agents":[]}{"agents":[]}`, "more than one JSON value"},
		{"", "empty document"},
	} {
		path := filepath.Join(t.TempDir(), "agents.json")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%q: %v", tc.body, err)
		}
	}
}

// Every field an agent carries into a launch or a page is bounded, whether it
// comes from the config or from the Agents page.
func TestValidateBoundsCwdIconAdapterAndEnv(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Agent)
		field  string // the field the error must name; empty when the agent is valid
	}{
		{"cwd of 4096 bytes", func(a *Agent) { a.Cwd = "/" + strings.Repeat("d", 4095) }, ""},
		{"cwd of 4097 bytes", func(a *Agent) { a.Cwd = "/" + strings.Repeat("d", 4096) }, "cwd"},
		{"cwd with NUL", func(a *Agent) { a.Cwd = "/srv\x00x" }, "cwd"},
		{"a lucide icon", func(a *Agent) { a.Icon = "i-lucide-pi-square" }, ""},
		{"icon of 64 bytes", func(a *Agent) { a.Icon = "i" + strings.Repeat("-", 63) }, ""},
		{"icon of 65 bytes", func(a *Agent) { a.Icon = "i" + strings.Repeat("-", 64) }, "icon"},
		{"icon with a space", func(a *Agent) { a.Icon = "i-lucide-x y" }, "icon"},
		{"icon in upper case", func(a *Agent) { a.Icon = "I-Lucide-X" }, "icon"},
		{"adapter", func(a *Agent) { a.Adapter = "claude" }, ""},
		{"adapter of 33 characters", func(a *Agent) { a.Adapter = strings.Repeat("a", 33) }, "adapter"},
		{"adapter with a slash", func(a *Agent) { a.Adapter = "../x" }, "adapter"},
		{"env key of 128 bytes", func(a *Agent) { a.Env = map[string]string{strings.Repeat("K", 128): "v"} }, ""},
		{"env key of 129 bytes", func(a *Agent) { a.Env = map[string]string{strings.Repeat("K", 129): "v"} }, "env"},
		{"env value of 16384 bytes", func(a *Agent) { a.Env = map[string]string{"K": strings.Repeat("v", 16384)} }, ""},
		{"env value of 16385 bytes", func(a *Agent) { a.Env = map[string]string{"K": strings.Repeat("v", 16385)} }, "env"},
		{"envPassthrough name of 128 bytes", func(a *Agent) { a.EnvPassthrough = []string{strings.Repeat("P", 128)} }, ""},
		{"envPassthrough name of 129 bytes", func(a *Agent) { a.EnvPassthrough = []string{strings.Repeat("P", 129)} }, "envPassthrough"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := Agent{ID: "x", Name: "X", Command: []string{"x"}}
			c.change(&a)
			err := validate(a)
			switch {
			case c.field == "" && err != nil:
				t.Fatalf("valid agent rejected: %v", err)
			case c.field != "" && err == nil:
				t.Fatal("agent over the limit accepted")
			case c.field != "" && !strings.Contains(err.Error(), c.field):
				t.Fatalf("error does not name %q: %v", c.field, err)
			}
		})
	}
}

// A saved agent that replaces another keeps what it leaves out: the adapter,
// the signal, and every env value it holds as the mask, which the Agents page
// stores for a key it did not change. A masked key the replaced agent does not
// have is dropped, and so is every masked value of an agent that replaces
// nothing.
func TestOverlayInheritsWhatAnOverrideLeavesOut(t *testing.T) {
	base, err := Load(File{DisableDefaults: true, Agents: []Agent{
		{ID: "keyed", Name: "keyed", Command: []string{"k"}, Adapter: "claude", Signal: &Signal{Kind: SignalPattern, Pattern: `^> $`},
			Env: map[string]string{"API_KEY": "v1", "REGION": "eu"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ov := Overlay{Agents: []Agent{
		{ID: "keyed", Name: "Keyed", Command: []string{"k2"}, Env: map[string]string{"API_KEY": RedactedValue, "REGION": "us", "GONE": RedactedValue}},
		{ID: "fresh", Name: "fresh", Command: []string{"f"}, Env: map[string]string{"X": RedactedValue, "Y": "y"}},
	}}
	got := base.Clone()
	if err := got.ApplyOverlay(ov); err != nil {
		t.Fatal(err)
	}
	k, _ := got.Get("keyed")
	if k.Command[0] != "k2" || k.Adapter != "claude" || k.Signal == nil || k.Signal.Kind != SignalPattern || k.Signal.Pattern != `^> $` ||
		!reflect.DeepEqual(k.Env, map[string]string{"API_KEY": "v1", "REGION": "us"}) {
		t.Fatalf("keyed %+v (signal %+v)", k, k.Signal)
	}
	if f, _ := got.Get("fresh"); !reflect.DeepEqual(f.Env, map[string]string{"Y": "y"}) || f.Adapter != "" || f.Signal != nil {
		t.Fatalf("fresh %+v", f)
	}
	// What an override names it keeps; an env it does not list is not inherited.
	own := base.Clone()
	if err := own.ApplyOverlay(Overlay{Agents: []Agent{{ID: "keyed", Name: "k", Command: []string{"k"}, Adapter: "codex", Signal: &Signal{Kind: SignalNone}}}}); err != nil {
		t.Fatal(err)
	}
	if k, _ := own.Get("keyed"); k.Adapter != "codex" || k.Signal.Kind != SignalNone || len(k.Env) != 0 {
		t.Fatalf("own %+v", k)
	}
	// Neither the overlay nor the base changes.
	if ov.Agents[0].Env["API_KEY"] != RedactedValue || ov.Agents[0].Adapter != "" {
		t.Fatal("ApplyOverlay changed the overlay")
	}
	if b, _ := base.Get("keyed"); b.Env["REGION"] != "eu" {
		t.Fatal("ApplyOverlay changed the base")
	}
}

// An override's env value equal to the replaced agent's is that agent's value,
// as the mask is, and a value of its own stays. A rotation in the config
// reaches an override saved with the old value once a save has stored the mask
// for it (internal/api, keepMaskedEnv).
func TestOverlayValueEqualToTheBaseIsTheBase(t *testing.T) {
	apply := func(secret, saved string) string {
		t.Helper()
		c, err := Load(File{DisableDefaults: true, Agents: []Agent{{ID: "keyed", Name: "keyed", Command: []string{"k"}, Env: map[string]string{"API_KEY": secret}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "keyed", Name: "keyed", Command: []string{"k"}, Env: map[string]string{"API_KEY": saved}}}}); err != nil {
			t.Fatal(err)
		}
		a, _ := c.Get("keyed")
		return a.Env["API_KEY"]
	}
	for _, tc := range []struct{ secret, saved, want string }{
		{"v1", "v1", "v1"},
		{"v2", RedactedValue, "v2"}, // what the next save stores for "v1" while the config held it
		{"v1", "other", "other"},
		{"v2", "other", "other"},
	} {
		if got := apply(tc.secret, tc.saved); got != tc.want {
			t.Errorf("config %s, saved %s: runs with %s, want %s", tc.secret, tc.saved, got, tc.want)
		}
	}
}

func TestSourceOfEachAgent(t *testing.T) {
	c, err := Load(File{Agents: []Agent{
		{ID: "claude", Name: "Claude (pinned)", Command: []string{"claude"}},
		{ID: "my-tool", Name: "My tool", Command: []string{"mytool"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]Source{"codex": SourceBuiltIn, "claude": SourceConfig, "my-tool": SourceConfig, "nope": ""} {
		if got := c.Source(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	err = c.ApplyOverlay(Overlay{
		Agents: []Agent{{ID: "codex", Name: "Codex", Command: []string{"codex"}}, {ID: "added", Name: "Added", Command: []string{"a"}}},
		Hidden: []string{"my-tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]Source{"codex": SourceSaved, "added": SourceSaved, "my-tool": "", "shell": SourceBuiltIn} {
		if got := c.Source(id); got != want {
			t.Errorf("after the overlay, %s: %q, want %q", id, got, want)
		}
	}
}

func TestUpsertStoresACopy(t *testing.T) {
	var c Catalog
	a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Env: map[string]string{"K": "v"}, Signal: &Signal{Kind: SignalBell}}
	if err := c.Upsert(a); err != nil {
		t.Fatal(err)
	}
	a.Command[0], a.Env["K"], a.Signal.Kind = "changed", "changed", SignalNone
	if got, _ := c.Get("x"); got.Command[0] != "x" || got.Env["K"] != "v" || got.Signal.Kind != SignalBell {
		t.Fatalf("the catalog shares the caller's agent: %+v", got)
	}
	// Clone takes a value: a catalog a call returns clones without a variable.
	if ids := idsOf(Default().Clone()); ids != builtIns {
		t.Fatalf("clone lists %s", ids)
	}
}

// The workbench fetches no icon at runtime: it bundles the agent icons that
// web/app/utils/agentIcons.ts lists (AGENT_ICONS, which web/nuxt.config.ts
// takes), and the built-in agents' icons reach it from the server. The list
// holds exactly the built-ins' icons and two more: i-lucide-bot, which an
// agent without an icon, or with one the list lacks, shows
// (AGENT_ICON_FALLBACK), and i-lucide-wrench, the tool icon of the example
// config (conductor.example.json). A built-in icon missing from the list would
// show the fallback; a name nothing uses only weighs on the bundle.
func TestBuiltInIconsAreInTheWorkbenchBundle(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "app", "utils", "agentIcons.ts"))
	if err != nil {
		t.Fatalf("the workbench's icon list: %v", err)
	}
	list := regexp.MustCompile(`(?s)export const AGENT_ICONS\b[^=]*=\s*\[(.*?)\]`).FindSubmatch(b)
	if list == nil {
		t.Fatal("web/app/utils/agentIcons.ts has no AGENT_ICONS = [...] list")
	}
	lucide := regexp.MustCompile(`^i-lucide-[a-z0-9-]+$`)
	bundled := map[string]bool{}
	for _, quoted := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(list[1], -1) {
		name := string(quoted[1])
		if !lucide.MatchString(name) {
			t.Errorf("AGENT_ICONS holds %q, not an i-lucide-<name> icon", name)
			continue
		}
		bundled[name] = true
	}
	want := map[string]bool{"i-lucide-bot": true, "i-lucide-wrench": true}
	for _, a := range Default().List() {
		if a.Icon != "" {
			want[a.Icon] = true
		}
	}
	var missing, extra []string
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if !bundled[name] {
			missing = append(missing, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(bundled)) {
		if !want[name] {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("AGENT_ICONS in web/app/utils/agentIcons.ts lacks %q (a built-in agent's icon, or a generic one), and holds %q, which no built-in agent uses and which is neither generic icon", missing, extra)
	}
}

// site is an optional https URL with a host and no user info, at most 200 bytes.
func TestSiteValidation(t *testing.T) {
	base := Agent{ID: "x", Name: "X", Command: []string{"x"}}
	for _, ok := range []string{"", "https://example.com", "https://example.com/docs/cli?x=1", "https://example.com:8443/x", "https://example.com:65535"} {
		a := base
		a.Site = ok
		if err := validate(a); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://example.com", "example.com", "https://", "https://user:pw@example.com", "https://example.com/" + strings.Repeat("a", 200), "javascript:alert(1)", "https://exa mple.com",
		// What a browser's URL parser refuses too: no host name, a port out of range, a no-break space in the host.
		"https://:443", "https://example.com:99999", "https://example.com:0", "https://exa\u00a0mple.com"} {
		a := base
		a.Site = bad
		if err := validate(a); err == nil || !strings.Contains(err.Error(), "site") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// Every built-in but Shell names its website, held to the site rule. The
// three a check on 2026-10-02 found moved are pinned where they live now:
// developers.openai.com/codex/cli redirects to learn.chatgpt.com, Goose's
// docs left block.github.io, and github.com/deepseek-ai/dsh is a 404.
func TestDefaultsHaveSites(t *testing.T) {
	moved := map[string]string{
		"codex": "https://learn.chatgpt.com/docs/codex/cli",
		"goose": "https://goose-docs.ai",
		"dsh":   "https://github.com/deepseek-ai/deepseek-harness",
	}
	for _, a := range defaults() {
		if a.ID == "shell" {
			if a.Site != "" {
				t.Fatalf("shell has a site: %q", a.Site)
			}
			continue
		}
		if a.Site == "" {
			t.Errorf("%s: no site", a.ID)
		}
		if want, ok := moved[a.ID]; ok && a.Site != want {
			t.Errorf("%s: site %q, want %q", a.ID, a.Site, want)
		}
		if err := validate(a); err != nil {
			t.Errorf("%s: %v", a.ID, err)
		}
	}
}

// A saved override that leaves the site out keeps the built-in's, as it keeps
// its adapter and signal; one with a site of its own keeps that.
func TestOverlayInheritsTheSite(t *testing.T) {
	c := Default()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{
		{ID: "claude", Name: "Claude, mine", Command: []string{"claude"}},
		{ID: "codex", Name: "Codex", Command: []string{"codex"}, Site: "https://example.com/codex"},
	}}); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Get("claude"); a.Site != "https://claude.com/claude-code" {
		t.Fatalf("claude: %q", a.Site)
	}
	if a, _ := c.Get("codex"); a.Site != "https://example.com/codex" {
		t.Fatalf("codex: %q", a.Site)
	}
}

// A yolo recipe is bounded like the command and the environment it adds to,
// and never names Conductor's own variables, which BuildEnv would drop.
func TestYoloValidation(t *testing.T) {
	ok := Agent{ID: "x", Name: "X", Command: []string{"x"}, Yolo: &Yolo{Args: []string{"--yes"}, Env: map[string]string{"X_MODE": "auto"}}}
	if err := validate(ok); err != nil {
		t.Fatal(err)
	}
	for _, y := range []Yolo{
		{Args: []string{""}},
		{Args: []string{"a\x00b"}},
		{Args: []string{strings.Repeat("a", 4097)}},
		{Args: slices.Repeat([]string{"-y"}, 17)},
		{Env: map[string]string{"CONDUCTOR_NOTIFY_TOKEN": "x"}},
		{Env: map[string]string{"BAD NAME": "x"}},
		{Env: map[string]string{"X": strings.Repeat("v", 4097)}},
		{Env: map[string]string{"X": "a\x00"}},
	} {
		a := ok
		a.Yolo = &y
		if err := validate(a); err == nil || !strings.Contains(err.Error(), "yolo") {
			t.Errorf("%+v: %v", y, err)
		}
	}
	empty := ok
	empty.Yolo = &Yolo{}
	if err := validate(empty); err != nil || !empty.Yolo.Empty() || !(*Yolo)(nil).Empty() {
		t.Fatalf("an empty recipe: %v", err)
	}
	bad := ok
	bad.TrustPrompt = ".*"
	if err := validate(bad); err == nil || !strings.Contains(err.Error(), "trustPrompt") {
		t.Fatalf("a trust prompt matching nothing: %v", err)
	}
}

// A saved override that leaves the recipe out takes the replaced agent's, an
// empty one has none, and one of its own replaces it whole; the trust prompt
// is inherited too. Redacted leaves the recipe as it is: its values are
// flags, not secrets.
func TestOverlayInheritsTheYoloRecipe(t *testing.T) {
	c := Default()
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{
		{ID: "claude", Name: "Claude, mine", Command: []string{"claude"}},
		{ID: "codex", Name: "Codex", Command: []string{"codex"}, Yolo: &Yolo{}},
		{ID: "copilot", Name: "Copilot", Command: []string{"copilot"}, Yolo: &Yolo{Args: []string{"--allow-all-tools"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	claude, _ := c.Get("claude")
	if claude.Yolo == nil || !slices.Equal(claude.Yolo.Args, []string{"--dangerously-skip-permissions"}) || claude.TrustPrompt == "" {
		t.Fatalf("claude: %+v %q", claude.Yolo, claude.TrustPrompt)
	}
	if codex, _ := c.Get("codex"); !codex.Yolo.Empty() {
		t.Fatalf("codex: %+v", codex.Yolo)
	}
	copilot, _ := c.Get("copilot")
	if !slices.Equal(copilot.Yolo.Args, []string{"--allow-all-tools"}) || copilot.Yolo.Env != nil {
		t.Fatalf("copilot: %+v", copilot.Yolo)
	}
	if r := copilot.Redacted(); r.Yolo == nil || r.Yolo.Env != nil || !slices.Equal(r.Yolo.Args, copilot.Yolo.Args) {
		t.Fatalf("redacted: %+v", r.Yolo)
	}
	base, _ := Default().Get("copilot")
	if r := base.Redacted(); r.Yolo.Env["COPILOT_ALLOW_ALL"] != "true" {
		t.Fatalf("a recipe's env is not a secret: %+v", r.Yolo)
	}
	cp := c.Clone()
	got, _ := cp.Get("claude")
	got.Yolo.Args[0] = "changed"
	if again, _ := c.Get("claude"); again.Yolo.Args[0] != "--dangerously-skip-permissions" {
		t.Fatal("a clone shares the recipe")
	}
}

// The built-ins' recipes, as the yolo research found them (docs/features.md,
// the adapter matrix): pi and the shell have none.
func TestDefaultYoloRecipes(t *testing.T) {
	want := map[string]Yolo{
		"claude":   {Args: []string{"--dangerously-skip-permissions"}},
		"codex":    {Args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
		"agy":      {Args: []string{"--dangerously-skip-permissions"}},
		"copilot":  {Args: []string{"--yolo"}, Env: map[string]string{"COPILOT_ALLOW_ALL": "true"}},
		"cursor":   {Args: []string{"--yolo", "--trust"}},
		"opencode": {Args: []string{"--auto"}},
		"omp":      {Args: []string{"--yolo"}},
		"aider":    {Args: []string{"--yes-always"}},
		"goose":    {Env: map[string]string{"GOOSE_MODE": "auto"}},
		"amp":      {Args: []string{"--dangerously-allow-all"}},
		"dsh":      {Env: map[string]string{"DSH_PERMISSION_MODE": "danger-full-access"}},
	}
	for _, a := range defaults() {
		w, ok := want[a.ID]
		if !ok {
			if !a.Yolo.Empty() {
				t.Errorf("%s has a recipe: %+v", a.ID, a.Yolo)
			}
			continue
		}
		if a.Yolo == nil || !slices.Equal(a.Yolo.Args, w.Args) || !maps.Equal(a.Yolo.Env, w.Env) {
			t.Errorf("%s: %+v, want %+v", a.ID, a.Yolo, w)
		}
	}
	// The words the CLIs drew in the prompt investigation's captures (Claude
	// Code 2.1.287, Codex 0.159.0), as the trust watcher reads a screen: each
	// escape sequence a space.
	for id, tc := range map[string]struct{ screen, words string }{
		"claude": {"root/c1 Quick safety check: Is this a project you created or one you trust? (Like your own code", "Is this a project you created or one you trust?"},
		"codex":  {"root/x1 Trust this folder? Codex can read, edit, and run files here", "Trust this folder?"},
	} {
		a, _ := Default().Get(id)
		re, err := CompilePattern(a.TrustPrompt)
		if words := tc.words; err != nil || re.FindString(tc.screen) != words {
			t.Errorf("%s's trust prompt %q does not find %q: %v", id, a.TrustPrompt, tc.words, err)
		}
	}
}

// A session recipe is bounded like a command, places {id} as a whole argument
// once, says how the id is known, and anchors an id pattern that cannot match
// an empty id or one that reads as a flag. The empty recipe disables an
// inherited one.
func TestSessionRecipeValidation(t *testing.T) {
	good := SessionRecipe{StartArgs: []string{"--session-id", IDArg}, ResumeArgs: []string{"--resume", IDArg}, IDPattern: `^[0-9a-f-]{36}$`}
	a := Agent{ID: "x", Name: "X", Command: []string{"x"}, Session: &good}
	if err := validate(a); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]SessionRecipe{
		"no resume args":       {StartArgs: good.StartArgs, IDPattern: good.IDPattern, ResumeArgs: []string{"--resume"}},
		"{id} twice":           {StartArgs: good.StartArgs, IDPattern: good.IDPattern, ResumeArgs: []string{IDArg, IDArg}},
		"{id} inside an arg":   {StartArgs: good.StartArgs, IDPattern: good.IDPattern, ResumeArgs: []string{"--resume=" + IDArg}},
		"no way to the id":     {IDPattern: good.IDPattern, ResumeArgs: good.ResumeArgs},
		"unanchored pattern":   {IDFrom: "hook", IDPattern: `[0-9a-f]+`, ResumeArgs: good.ResumeArgs},
		"pattern takes a flag": {IDFrom: "hook", IDPattern: `^.+$`, ResumeArgs: good.ResumeArgs},
		"pattern takes empty":  {IDFrom: "hook", IDPattern: `^[a-z]*$`, ResumeArgs: good.ResumeArgs},
		"bad policy":           {IDFrom: "hook", IDPolicy: "newest", IDPattern: good.IDPattern, ResumeArgs: good.ResumeArgs},
		"bad idFrom":           {IDFrom: "screen", IDPattern: good.IDPattern, ResumeArgs: good.ResumeArgs},
		"bad newId":            {StartArgs: good.StartArgs, NewID: "ulid", IDPattern: good.IDPattern, ResumeArgs: good.ResumeArgs},
		"NUL":                  {StartArgs: good.StartArgs, IDPattern: good.IDPattern, ResumeArgs: []string{"--resume\x00", IDArg}},
		"too many args":        {StartArgs: good.StartArgs, IDPattern: good.IDPattern, ResumeArgs: append(slices.Repeat([]string{"-x"}, 16), IDArg)},
	} {
		b := a
		b.Session = &r
		if err := validate(b); err == nil || !strings.Contains(err.Error(), "session") {
			t.Errorf("%s: %v", name, err)
		}
	}
	b := a
	b.Session = &SessionRecipe{}
	if err := validate(b); err != nil || !b.Session.Empty() || !(*SessionRecipe)(nil).Empty() {
		t.Fatalf("the empty recipe: %v", err)
	}
	if got := Expand([]string{"resume", IDArg, "-c", "x={id}y"}, "abc"); !slices.Equal(got, []string{"resume", "abc", "-c", "x={id}y"}) {
		t.Fatalf("Expand: %q", got)
	}
}

// The built-ins' session recipes, as the resume research found them (the
// adapter matrix says which are verified live): the agents whose ids reach
// Conductor only through a plugin, and the shell, have none yet.
func TestDefaultSessionRecipes(t *testing.T) {
	with := map[string][]string{
		"claude":  {"--resume", IDArg},
		"codex":   {"resume", IDArg, "-c", `tui.resume_cwd="session"`},
		"agy":     {"--conversation", IDArg},
		"copilot": {"--session-id", IDArg},
		"cursor":  {"--resume", IDArg},
		"pi":      {"--session-id", IDArg},
		"goose":   {"session", "--resume", "--name", IDArg},
	}
	for _, a := range defaults() {
		want, ok := with[a.ID]
		if !ok {
			if !a.Session.Empty() {
				t.Errorf("%s has a recipe: %+v", a.ID, a.Session)
			}
			continue
		}
		if a.Session.Empty() || !slices.Equal(a.Session.ResumeArgs, want) {
			t.Errorf("%s: %+v", a.ID, a.Session)
		}
	}
	claude, _ := Default().Get("claude")
	re := regexp.MustCompile(claude.Session.IDPattern)
	if !re.MatchString("3f80c8bd-0000-4000-8000-000000000001") || re.MatchString("--dangerously-skip-permissions") {
		t.Fatal("claude's id pattern")
	}
	codex, _ := Default().Get("codex")
	if codex.Session.IDPolicy != "lowest" || codex.Session.IDFrom != "hook" || len(codex.Session.StartArgs) != 0 {
		t.Fatalf("codex: %+v", codex.Session)
	}
	if c := Default(); c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "claude", Name: "C", Command: []string{"claude"}}}}) != nil {
		t.Fatal("overlay")
	} else if a, _ := c.Get("claude"); a.Session.Empty() {
		t.Fatal("a saved override that leaves the recipe out lost the built-in's")
	}
}

// A saved override that leaves probe out takes the replaced agent's, and
// one that turns it off keeps that off; clones share nothing.
func TestOverlayInheritsProbe(t *testing.T) {
	c := Default()
	off := false
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{
		{ID: "claude", Name: "Claude, mine", Command: []string{"claude"}},
		{ID: "codex", Name: "Codex, quiet", Command: []string{"codex"}, Probe: &off},
	}}); err != nil {
		t.Fatal(err)
	}
	claude, _ := c.Get("claude")
	if !claude.Probed() || claude.Probe != nil {
		t.Fatalf("claude: %v", claude.Probe)
	}
	codex, _ := c.Get("codex")
	if codex.Probed() {
		t.Fatal("codex: the override's off was lost")
	}
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "codex", Name: "Codex again", Command: []string{"codex"}}}}); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.Get("codex"); again.Probed() {
		t.Fatal("a later override without probe must take the replaced agent's off")
	}
	cl := codex.clone()
	*cl.Probe = true
	if codex.Probed() {
		t.Fatal("clone shares the probe")
	}
}

// The trust question's answers: the built-in agents carry theirs (Claude
// Code's trusting answer is Down then Enter, since it highlights "No, exit";
// Codex's is Enter, and its other answer, Codex 0.161's "2. Quit", says it
// quits), a saved override inherits them, and a bad answer is refused.
func TestTrustAnswers(t *testing.T) {
	c := Default()
	claude, _ := c.Get("claude")
	if len(claude.TrustAnswers) != 2 || claude.TrustAnswers[0].Input != "\x1b[B\r" || claude.TrustAnswers[1].Label != "No, exit" || claude.TrustAnswers[1].Input != "\r" {
		t.Fatalf("claude: %+v", claude.TrustAnswers)
	}
	codex, _ := c.Get("codex")
	if len(codex.TrustAnswers) != 2 || codex.TrustAnswers[0].Input != "\r" || codex.TrustAnswers[1].Label != "No, quit" || codex.TrustAnswers[1].Input != "\x1b[B\r" {
		t.Fatalf("codex: %+v", codex.TrustAnswers)
	}
	for id, a := range map[string]Agent{"claude": claude, "codex": codex} {
		if a.TrustPrompt == "" {
			t.Fatalf("%s: answers without a question", id)
		}
	}
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "claude", Name: "Claude, mine", Command: []string{"claude"}}}}); err != nil {
		t.Fatal(err)
	}
	if mine, _ := c.Get("claude"); len(mine.TrustAnswers) != 2 || mine.TrustAnswers[0].Label != "Yes, I trust this folder" {
		t.Fatalf("the override lost the answers: %+v", mine.TrustAnswers)
	}
	ok := Agent{ID: "x", Name: "X", Command: []string{"x"}, TrustPrompt: `Trust\s*this\s*folder\?`}
	for _, bad := range [][]Answer{
		{{Label: "", Input: "\r"}},
		{{Label: "Yes", Input: ""}},
		{{Label: strings.Repeat("y", 61), Input: "\r"}},
		{{Label: "Yes", Input: strings.Repeat("\r", 49)}},
		{{Label: "1", Input: "\r"}, {Label: "2", Input: "\r"}, {Label: "3", Input: "\r"}, {Label: "4", Input: "\r"}, {Label: "5", Input: "\r"}, {Label: "6", Input: "\r"}, {Label: "7", Input: "\r"}},
	} {
		a := ok
		a.TrustAnswers = bad
		if err := validate(a); err == nil || !strings.Contains(err.Error(), "trustAnswers") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	good := ok
	good.TrustAnswers = []Answer{{Label: "Yes", Input: "\r"}}
	if err := validate(good); err != nil {
		t.Fatal(err)
	}
}

// The agent's other startup questions (round 13, G2c): Codex's built-in
// entry has "Hooks need review" with its two answers, a saved override
// inherits them, and a bad question is refused.
func TestStartupQuestions(t *testing.T) {
	c := Default()
	codex, _ := c.Get("codex")
	if len(codex.Questions) != 7 || len(codex.Questions[0].Answers) != 2 || codex.Questions[0].Answers[0] != (Answer{Label: "Trust all and continue", Input: "2\r"}) || codex.Questions[0].Answers[1].Input != "3\r" {
		t.Fatalf("codex: %+v", codex.Questions)
	}
	// The background-server question picks on a digit, and the update question skips on Escape: no answer
	// carries an Enter, which would land on the next screen (the update question's Update now).
	for i, want := range map[int]string{1: "Background server has incompatible feature settings This session requires", 2: "› 1. Update now (runs `npm install -g @openai/codex`) 2. Skip"} {
		q := codex.Questions[i]
		re, err := CompilePattern(q.Prompt)
		if err != nil || !re.MatchString(want) {
			t.Fatalf("question %d %q does not match %q: %v", i, q.Prompt, want, err)
		}
		for _, a := range q.Answers {
			if strings.ContainsAny(a.Input, "\r\n") {
				t.Fatalf("question %d answer %+v carries an Enter", i, a)
			}
		}
	}
	if up, _ := CompilePattern(codex.Questions[2].Prompt); up.MatchString("Update available! 0.161.0 -> 0.162.0 Run npm install -g @openai/codex to update.") {
		t.Fatal("the update banner (no question) must not hold the session")
	}
	// The update question's words are its heading, as Codex 0.161 draws it (→) and as the e2e stub does (->);
	// its answers are Escape (skip) and 3 (skip until the next version), which picks at once.
	upd := codex.Questions[2]
	words, err := CompilePattern(upd.Words)
	if err != nil {
		t.Fatalf("update words %q: %v", upd.Words, err)
	}
	for text, want := range map[string][]string{
		"Update available · 0.161.0 → 0.162.1 Release notes: https://github.com/openai/codex/releases/latest › 1. Update now (runs `npm install -g @openai/codex`)": {"Update available", "0.161.0 → 0.162.1"},
		"Update available · 0.161.0 -> 0.162.0 > 1. Update now (runs `npm install -g @openai/codex`)":                                                               {"Update available", "0.161.0 -> 0.162.0"},
	} {
		if m := words.FindStringSubmatch(text); len(m) != 3 || m[1] != want[0] || m[2] != want[1] {
			t.Fatalf("update words in %q: %q", text, m)
		}
	}
	if len(upd.Answers) != 2 || upd.Answers[0] != (Answer{Label: "Skip this update", Input: "\x1b"}) || upd.Answers[1] != (Answer{Label: "Skip until next version", Input: "3"}) {
		t.Fatalf("update answers %+v", upd.Answers)
	}
	re, err := CompilePattern(codex.Questions[0].Prompt)
	if err != nil || !re.MatchString("Hooks need review 7 hooks are new or changed") || !re.MatchString("Hooks  eed review") || re.MatchString("Trust this folder?") {
		t.Fatalf("codex's pattern %q: %v", codex.Questions[0].Prompt, err)
	}
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "codex", Name: "Codex, mine", Command: []string{"codex"}}}}); err != nil {
		t.Fatal(err)
	}
	if mine, _ := c.Get("codex"); len(mine.Questions) != len(codex.Questions) || mine.Questions[0].Prompt != codex.Questions[0].Prompt || mine.Questions[2].Words != codex.Questions[2].Words {
		t.Fatalf("the override lost the questions: %+v", mine.Questions)
	}
	ok := Agent{ID: "x", Name: "X", Command: []string{"x"}}
	for _, bad := range [][]Question{
		{{Prompt: "", Answers: []Answer{{Label: "Yes", Input: "\r"}}}},
		{{Prompt: "(", Answers: []Answer{{Label: "Yes", Input: "\r"}}}},
		{{Prompt: ".*", Answers: []Answer{{Label: "Yes", Input: "\r"}}}},
		{{Prompt: "Ready\\?", Answers: []Answer{{Label: "", Input: "\r"}}}},
		{{Prompt: "Ready\\?", Words: "(", Answers: []Answer{{Label: "Yes", Input: "\r"}}}},
		{{Prompt: "Ready\\?", Words: ".*", Answers: []Answer{{Label: "Yes", Input: "\r"}}}},
		{{Prompt: "a"}, {Prompt: "b"}, {Prompt: "c"}, {Prompt: "d"}, {Prompt: "e"}, {Prompt: "f"}, {Prompt: "g"}, {Prompt: "h"}, {Prompt: "i"}},
		// Answers read off the screen are a question asked at any time's,
		// with no answers of its own; the mode pattern is a pattern.
		{{Prompt: "Ready\\?", AnswersFromScreen: true}},
		{{Prompt: "Ready\\?", Anytime: true, AnswersFromScreen: true, Answers: []Answer{{Label: "Yes", Input: "1"}}}},
		{{Prompt: "Ready\\?", Anytime: true, AnswersFromScreen: true, ModeAnswers: "("}},
		{{Prompt: "Ready\\?", Anytime: true, AnswersFromScreen: true, ModeAnswers: ".*"}},
		{{Prompt: "Ready\\?", Anytime: true, AnswersFromScreen: true, TypedAnswers: "("}},
		{{Prompt: "Ready\\?", Anytime: true, AnswersFromScreen: true, TypedAnswers: ".*"}},
	} {
		a := ok
		a.Questions = bad
		if err := validate(a); err == nil || !strings.Contains(err.Error(), "questions") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	good := ok
	good.Questions = []Question{{Prompt: "Ready\\?", Answers: []Answer{{Label: "Yes", Input: "y\r"}}}, {Prompt: "Proceed\\?", Anytime: true, AnswersFromScreen: true, ModeAnswers: "(?i)auto mode", TypedAnswers: "with this feedback"}}
	if err := validate(good); err != nil {
		t.Fatal(err)
	}
}

// Claude Code's dialogs during the work are a question whose answers are
// read off the screen (#288), its choices that change the permission mode
// marked by their words and its feedback field told by its hint; and
// AskUserQuestion's last page, which has no footer, is one of its own.
// They round-trip through a saved override as the other question fields
// do.
func TestClaudeCodesDialogQuestion(t *testing.T) {
	claude, _ := Default().Get("claude")
	if len(claude.Questions) != 6 {
		t.Fatalf("claude: %+v", claude.Questions)
	}
	q, last := claude.Questions[0], claude.Questions[1]
	if !q.Anytime || !q.AnswersFromScreen || len(q.Answers) != 0 || q.Words != "" || q.TypedAnswers == "" {
		t.Fatalf("claude's question %+v", q)
	}
	if !last.Anytime || !last.AnswersFromScreen || len(last.Answers) != 0 || last.ModeAnswers != "" || last.TypedAnswers != "" {
		t.Fatalf("claude's last page %+v", last)
	}
	typed, err := CompilePattern(q.TypedAnswers)
	if err != nil {
		t.Fatal(err)
	}
	// The plan's feedback field, by its hint, and AskUserQuestion's
	// "Type something.", the choice a rule follows (one ─ in its whole),
	// typed into or not.
	for whole, want := range map[string]bool{
		"3. Tell Claude what to change shift+tab to approve with this feedback": true,
		"3. abc shift+tab to approve with this feedback":                        true,
		"3. Type something. ─":                                                  true,
		"3. xy ─":                                                               true,
		"2. Yes, manually approve edits":                                        false,
		"1. Red Prefer red.":                                                    false,
		"4. Chat about this":                                                    false,
		"3. Yes, and switch to auto mode":                                       false,
	} {
		if typed.MatchString(whole) != want {
			t.Errorf("typed answers %q on %q: %v", q.TypedAnswers, whole, !want)
		}
	}
	mode, err := CompilePattern(q.ModeAnswers)
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]bool{
		"Yes, and switch to auto mode · auto mode handles these prompts for you":                                          true,
		"Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)": true,
		"Yes, and use auto mode": true,
		"Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session": true,
		"Yes, and auto-accept edits":  true,
		"Yes":                         false,
		"Yes, manually approve edits": false,
		"Yes, and always allow access to /home/dev/project from this project": false,
		"Tell Claude what to change":                                          false,
		"No":                                                                  false,
	} {
		if mode.MatchString(label) != want {
			t.Errorf("%q: mode %v", label, !want)
		}
	}
	b, err := json.Marshal(claude)
	if err != nil {
		t.Fatal(err)
	}
	var back Agent
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back.Questions, claude.Questions) {
		t.Fatalf("round trip %+v %v", back.Questions, err)
	}
}

// Codex's command approval (Codex 0.161, seen in the rc.7 app on 2026-10-11)
// is a question asked at any time, not only at start: found by its choices'
// keys, told by its heading and command, answered by y, p and Escape alone
// (each picks at once; an Enter would pick the highlighted Yes). The dialog
// without its "don't ask again" of a prefix is found by the second entry,
// with Yes and No alone; neither is found on a screen that only mentions
// the words.
func TestCodexCommandApproval(t *testing.T) {
	codex, _ := Default().Get("codex")
	withP, deny, plain, other := codex.Questions[3], codex.Questions[4], codex.Questions[5], codex.Questions[6]
	for _, q := range codex.Questions[3:] {
		if !q.Anytime {
			t.Fatalf("not asked at any time: %+v", q)
		}
	}
	for _, q := range codex.Questions[:3] {
		if q.Anytime {
			t.Fatalf("a startup question marked anytime: %+v", q)
		}
	}
	yes, dont, no := Answer{Label: "Yes, proceed", Input: "y"}, Answer{Label: "Yes, and don't ask again", Input: "p"}, Answer{Label: "No, tell Codex what to do differently", Input: "\x1b"}
	if len(withP.Answers) != 3 || withP.Answers[0] != yes || withP.Answers[1] != dont || withP.Answers[2] != no {
		t.Fatalf("answers %+v", withP.Answers)
	}
	if len(plain.Answers) != 2 || plain.Answers[0] != yes || plain.Answers[1] != no {
		t.Fatalf("answers without the prefix %+v", plain.Answers)
	}
	if len(deny.Answers) != 3 || deny.Answers[0] != yes || deny.Answers[1] != (Answer{Label: "No, continue without running it", Input: "d"}) || deny.Answers[2].Input != "\x1b" {
		t.Fatalf("answers of the accept-or-decline dialog %+v", deny.Answers)
	}
	if len(other.Answers) != 2 || other.Answers[0].Input != "y" || other.Answers[1].Input != "\x1b" {
		t.Fatalf("answers of another approval %+v", other.Answers)
	}
	// The first pattern that matches gives the choices: each dialog finds its own.
	which := func(text string) int {
		t.Helper()
		for i, q := range codex.Questions {
			if re, err := CompilePattern(q.Prompt); err != nil {
				t.Fatalf("%q: %v", q.Prompt, err)
			} else if re.MatchString(text) {
				return i
			}
		}
		return -1
	}
	denyScreen := "  Would you like to run the following command?\n\n  $ git push --force\n\n› 1. Yes, proceed (y)\n  2. No, continue without running it (d)\n\n  Press enter to confirm or esc to cancel"
	network := "  Do you want to approve network access to \"example.com\"?\n\n› 1. Yes, just this once (y)\n  2. Yes, and allow this host in the future (p)\n  3. No, and block this host in the future (d)\n\n  Press enter to confirm or esc to cancel"
	if got := which(denyScreen); got != 4 {
		t.Fatalf("the accept-or-decline dialog is question %d", got)
	}
	if got := which(network); got != 6 {
		t.Fatalf("the network dialog is question %d", got)
	}
	if m := regexp.MustCompile(other.Words).FindStringSubmatch(network); len(m) != 3 || m[1] != `Do you want to approve network access to "example.com"?` {
		t.Fatalf("the network dialog's words %q", m)
	}
	// The dialog as the session's Screen reads it: its rows joined by line breaks, the history above.
	screen := strings.Join([]string{
		"• Ran curl -sI https://example.com",
		"  └ curl: (6) Could not resolve host: example.com",
		"",
		"  Would you like to run the following command?",
		"",
		"  Environment: local",
		"",
		"  Reason: May I rerun the curl command with network access? The sandboxed",
		"  attempt failed to resolve the host.",
		"",
		"  $ curl -sI https://example.com",
		"",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for commands that start with `curl -sI",
		"     https://example.com` (p)",
		"  3. No, and tell Codex what to do differently (esc)",
		"",
		"  Press enter to confirm or esc to cancel",
	}, "\n")
	noPrefix := "  Would you like to run the following command?\n\n  $ rm -rf build\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel"
	re := func(p string) *regexp.Regexp {
		t.Helper()
		r, err := CompilePattern(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		return r
	}
	if !re(withP.Prompt).MatchString(screen) || re(withP.Prompt).MatchString(noPrefix) || !re(plain.Prompt).MatchString(noPrefix) {
		t.Fatal("the approval's patterns do not tell the two dialogs apart")
	}
	// A key drawn in a style of its own reads with a space on each side.
	if styled := strings.NewReplacer("(y)", "( y )", "(p)", "( p )", "(esc)", "( esc )").Replace(screen); !re(withP.Prompt).MatchString(styled) {
		t.Fatalf("the styled keys are not found: %q", styled)
	}
	// Not the dialog: its words elsewhere, or the dialog's text above what Codex draws now (a file shown, a log).
	for _, text := range []string{
		"Codex would proceed (y) if you said so",
		"  Would you like to run the following command?",
		"› Ask Codex to do anything",
		screen + "\n\n• Ran cat notes.txt\n  └ (the dialog above was in the file)\n\n› Ask Codex to do anything\n\n  ⏎ send   ⌃J newline   ⌃T transcript",
	} {
		if re(withP.Prompt).MatchString(text) || re(plain.Prompt).MatchString(text) {
			t.Fatalf("%q held as the approval", text)
		}
	}
	words := re(withP.Words)
	if m := words.FindStringSubmatch(screen); len(m) != 3 || m[1] != "Would you like to run the following command?" || m[2] != "$ curl -sI https://example.com" {
		t.Fatalf("words %q", m)
	}
	// The command is the first "$ " row's, whole: a "$ " inside it, or rows of it, stay in.
	tricky := strings.Replace(screen, "  $ curl -sI https://example.com", "  $ rm -rf build; printf '$ done'\n  echo second row", 1)
	if m := words.FindStringSubmatch(tricky); len(m) != 3 || m[2] != "$ rm -rf build; printf '$ done'\n  echo second row" {
		t.Fatalf("words of a command with a $ in it %q", m)
	}
	if m := words.FindStringSubmatch("  Would you like to make the following edits?\n\n  src/a.go +1 -1\n\n› 1. Yes, proceed (y)"); len(m) != 3 || m[1] != "Would you like to make the following edits?" || m[2] != "" {
		t.Fatalf("words without a command %q", m)
	}
	for _, a := range append(withP.Answers, plain.Answers...) {
		if strings.ContainsAny(a.Input, "\r\n") {
			t.Fatalf("answer %+v carries an Enter, which picks the highlighted Yes", a)
		}
	}
}

// The e2e suite's Codex (web/e2e/conductor.e2e.json, the stub with Codex's
// recipes) carries the built-in entry's questions and its working title, so
// that what the specs check is what ships.
func TestTheE2ECodexCarriesTheBuiltInQuestions(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "e2e", "conductor.e2e.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Catalog struct {
			Agents []Agent `json:"agents"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	builtIn, _ := Default().Get("codex")
	for _, a := range cfg.Catalog.Agents {
		if a.ID != "codex" {
			continue
		}
		if !reflect.DeepEqual(a.Questions, builtIn.Questions) {
			t.Fatalf("the e2e Codex's questions differ from the built-in's:\n%+v\n%+v", a.Questions, builtIn.Questions)
		}
		if a.Signal == nil || *a.Signal != *builtIn.Signal {
			t.Fatalf("the e2e Codex's signal %+v differs from the built-in's %+v", a.Signal, builtIn.Signal)
		}
		return
	}
	t.Fatal("no codex in the e2e catalog")
}

// The e2e suite's Claude Code (web/e2e/conductor.e2e.json, the stub with
// Claude Code's recipes) carries the built-in entry's question asked
// during the work, its dialogs read off the screen (#288), so that what the
// specs check is what ships, and every spec that runs the stub runs with it.
func TestTheE2EClaudeCodeCarriesTheBuiltInQuestion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "e2e", "conductor.e2e.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Catalog struct {
			Agents []Agent `json:"agents"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	builtIn, _ := Default().Get("claude")
	for _, a := range cfg.Catalog.Agents {
		if a.ID != "claude" {
			continue
		}
		if !reflect.DeepEqual(a.Questions, builtIn.Questions) {
			t.Fatalf("the e2e Claude Code's questions differ from the built-in's:\n%+v\n%+v", a.Questions, builtIn.Questions)
		}
		return
	}
	t.Fatal("no claude in the e2e catalog")
}

// Claude Code signed out does not exit: it waits at its sign-in screen
// (2.1.319, #659's live check). The catalog finds that screen as a
// startup question with no answers, a screen no key of Conductor's
// passes, its words the screen's own line and never the sign-in URL below
// it; an ordinary start's words are no such screen.
func TestClaudeCodesSignInScreenIsAStartupQuestionWithNoAnswers(t *testing.T) {
	claude, _ := Default().Get("claude")
	i := slices.IndexFunc(claude.Questions, func(q Question) bool { return strings.Contains(q.Prompt, "paste") })
	if i < 0 {
		t.Fatalf("claude: %+v", claude.Questions)
	}
	q := claude.Questions[i]
	if q.Anytime || q.AnswersFromScreen || len(q.Answers) != 0 {
		t.Fatalf("the sign-in screen: %+v", q)
	}
	screen := " Browser didn't open? Use the url below to sign in (c to copy)\n\nhttps://example.invalid/sign-in?code=true\n\n\n Paste code here if prompted >"
	prompt, err := CompilePattern(q.Prompt)
	if err != nil || !prompt.MatchString(screen) {
		t.Fatalf("the prompt %q on the screen: %v", q.Prompt, err)
	}
	words, err := CompilePattern(q.Words)
	if err != nil {
		t.Fatal(err)
	}
	if got := words.FindString(screen); got != "Browser didn't open? Use the url below to sign in" {
		t.Fatalf("the words: %q", got)
	}
	if prompt.MatchString("Welcome to Claude Code v2.1.319\n> Try \"fix lint errors\"") {
		t.Fatal("an ordinary start reads as the sign-in screen")
	}
}

// Claude Code on an account folder never used asks for a theme, then a
// login method, then opens a browser to sign in (2.1.319, #659's third
// live check, #714): each a startup screen with no answers, so no key is
// typed into it, found on the screens as that check captured them. Its
// prompt at rest is none of them.
func TestClaudeCodesFirstRunScreensAreStartupQuestionsWithNoAnswers(t *testing.T) {
	claude, _ := Default().Get("claude")
	screens := map[string]string{
		"Choose the text style that looks best with your terminal": "Welcome to Claude Code v2.1.319\n Let's get started.\n Choose the text style that looks best with your terminal\n To change this later, run /theme\n     Auto (match terminal)\n ❯ ✔ Dark mode\n     Light mode\n     Dark mode (colorblind-friendly)",
		"Select login method:":       "Welcome to Claude Code v2.1.319\n Claude Code can be used with your Claude subscription or billed based on API\n usage through your Console account.\n Select login method:\n ❯ 1. Claude account with subscription · Individual, Team, or Enterprise\n   2. Anthropic Console account · API usage billing",
		"Opening browser to sign in": "Welcome to Claude Code v2.1.319\n ✻ Opening browser to sign in…",
	}
	prompt := "Welcome to Claude Code v2.1.319\n────────\n❯ Try \"create a util logging.py that...\"\n────────\n  ? for shortcuts"
	for words, screen := range screens {
		found := 0
		for _, q := range claude.Questions {
			re, err := CompilePattern(q.Prompt)
			if err != nil {
				t.Fatal(err)
			}
			if re.MatchString(prompt) {
				t.Fatalf("Claude Code's prompt reads as %q", q.Prompt)
			}
			if !re.MatchString(screen) {
				continue
			}
			found++
			if q.Anytime || q.AnswersFromScreen || len(q.Answers) != 0 || q.Words != "" {
				t.Fatalf("the screen %q: %+v", words, q)
			}
			if got := re.FindString(screen); got != words {
				t.Fatalf("the words of %q: %q", words, got)
			}
		}
		if found != 1 {
			t.Fatalf("%d questions find the screen %q", found, words)
		}
	}
}

// Claude Code and Codex carry their prompts' shapes (#714; the screens
// they are read on are the session's tests, TestTheAgentsPromptIsKnownByItsShape):
// each compiles as a pattern, a saved override that leaves the shape out
// takes the replaced agent's, and a shape that matches an empty screen,
// or one past 200 bytes, is refused.
func TestThePromptShape(t *testing.T) {
	c := Default()
	for _, id := range []string{"claude", "codex"} {
		a, _ := c.Get(id)
		if _, err := CompilePattern(a.PromptShape); err != nil {
			t.Fatalf("%s's prompt shape %q: %v", id, a.PromptShape, err)
		}
	}
	claude, _ := c.Get("claude")
	if err := c.ApplyOverlay(Overlay{Agents: []Agent{{ID: "claude", Name: "Claude, mine", Command: []string{"claude"}}}}); err != nil {
		t.Fatal(err)
	}
	if mine, _ := c.Get("claude"); mine.PromptShape != claude.PromptShape {
		t.Fatalf("the override's shape: %q", mine.PromptShape)
	}
	ok := Agent{ID: "x", Name: "X", Command: []string{"x"}, PromptShape: `(?m)^› .*\z`}
	if err := validate(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{".*", "(", strings.Repeat("x", 201)} {
		a := ok
		a.PromptShape = bad
		if err := validate(a); err == nil || !strings.Contains(err.Error(), "promptShape") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
