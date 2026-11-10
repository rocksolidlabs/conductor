//go:build unix

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// variant writes the main stream fixture with each old replaced by new,
// and returns its path.
func variant(t *testing.T, name string, pairs ...string) string {
	t.Helper()
	b, err := os.ReadFile(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(s, pairs[i]) {
			t.Fatalf("%s has no %q", name, pairs[i])
		}
		s = strings.ReplaceAll(s, pairs[i], pairs[i+1])
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func claudeRead(t *testing.T, s *stub, adm *fakeAdmit) UsageReport {
	t.Helper()
	var admit func(string) (Admission, error)
	if adm != nil {
		admit = adm.admit
	}
	return readClaudeUsage(context.Background(), s.input(self(t), admit))
}

func TestClaudeUsageReadsReport(t *testing.T) {
	s := newStub(t, "claude")
	adm := &fakeAdmit{}
	rep := claudeRead(t, s, adm)
	if rep.State != UsageOK || rep.Words != "" || rep.Version != claudeUsageQualified || rep.Outcome != Local || rep.Plan != "max" || rep.ReadAt.IsZero() {
		t.Fatalf("%+v", rep)
	}
	reset := time.Date(2026, 11, 17, 10, 0, 0, 123456000, time.UTC)
	if len(rep.Windows) != 2 {
		t.Fatalf("windows %+v", rep.Windows)
	}
	all, scoped := rep.Windows[0], rep.Windows[1]
	if all.Kind != "weekly_all" || all.Name != "Weekly, all models" || all.Percent != 40 || all.Severity != "normal" || all.Active == nil || !*all.Active || all.ResetsAt == nil || !all.ResetsAt.Equal(reset) || all.Minutes != 0 {
		t.Fatalf("weekly_all %+v", all)
	}
	if scoped.Kind != "weekly_scoped" || scoped.Name != "Weekly, Fixture Model" || scoped.Percent != 12 || scoped.Severity != "warning" || scoped.Active == nil || *scoped.Active {
		t.Fatalf("weekly_scoped %+v", scoped)
	}
	if len(rep.Credits) != 1 {
		t.Fatalf("credits %+v", rep.Credits)
	}
	c := rep.Credits[0]
	if c.Kind != "extra_usage" || c.Enabled == nil || *c.Enabled || c.Used == nil || *c.Used != "0" || c.Limit == nil || *c.Limit != "0" || c.Currency != "USD" {
		t.Fatalf("extra usage %+v", c)
	}
	if got := adm.outcomes(); !slices.Equal(got, []Outcome{Local}) || !slices.Equal(adm.asked, []string{claudeUsageQualified}) {
		t.Fatalf("admission asked %q, done %v", adm.asked, got)
	}
	// A window reported as null figures keeps them nil, and one not
	// reported is not there.
	s = newStub(t, "claude", "USAGE_STREAM="+variant(t, "claude-usage.jsonl",
		`"resets_at":"2026-11-17T10:00:00.123456+00:00","scope":null,"severity":"normal","is_active":true`, `"resets_at":null,"scope":null,"severity":"normal"`,
		`"monthly_limit":0`, `"monthly_limit":null`))
	rep = claudeRead(t, s, &fakeAdmit{})
	if rep.State != UsageOK || len(rep.Windows) != 2 || rep.Windows[0].ResetsAt != nil || rep.Windows[0].Active != nil || rep.Credits[0].Limit != nil {
		t.Fatalf("null figures: %+v", rep)
	}
}

func TestClaudeUsageKeepsNoText(t *testing.T) {
	rep := claudeRead(t, newStub(t, "claude"), &fakeAdmit{})
	b, err := json.Marshal(rep)
	if err != nil || rep.State != UsageOK {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, x := range []string{"FIXTURE-PEOPLE-TEXT", "fixture-secret-project", "fixture-person", "example.invalid", "Fixture Organisation", "00000000-0000-4000-8000", "2031-01-01", "fixture_tier", "fixture-model"} {
		if strings.Contains(string(b), x) {
			t.Errorf("the report holds %q: %s", x, b)
		}
	}
}

func TestClaudeUsageRefusesModelTurn(t *testing.T) {
	for name, pairs := range map[string][]string{
		"turns":   {`"num_turns":0`, `"num_turns":1`},
		"command": {`"local_command":"usage"`, `"local_command":"compact"`},
		// The proof of a turn is read before what only makes a run
		// inconclusive: another command with no count of turns, or with
		// one not a number, and a turn after a line that is not JSON.
		"command, no turns":        {`"num_turns":0,`, ``, `"local_command":"usage"`, `"local_command":"compact"`},
		"command, turns malformed": {`"num_turns":0`, `"num_turns":"0"`, `"local_command":"usage"`, `"local_command":"compact"`},
		"turns, command malformed": {`"num_turns":0`, `"num_turns":2`, `"local_command":"usage"`, `"local_command":7`},
		"turns after a bad line":   {`{"type":"result"`, "not json\n" + `{"type":"result"`, `"num_turns":0`, `"num_turns":1`},
		"turns, an error flag":     {`"num_turns":0`, `"num_turns":1`, `"is_error":false`, `"is_error":true`},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "claude", "USAGE_STREAM="+variant(t, "claude-usage.jsonl", pairs...))
			adm := &fakeAdmit{}
			rep := claudeRead(t, s, adm)
			if rep.State != UsageUnqualified || rep.Outcome != Turned || len(rep.Windows) != 0 || !strings.Contains(rep.Words, "model turn") {
				t.Fatalf("%+v", rep)
			}
			if got := adm.outcomes(); !slices.Equal(got, []Outcome{Turned}) {
				t.Fatalf("Done %v", got)
			}
		})
	}
}

func TestClaudeUsageInconclusive(t *testing.T) {
	noResult := func(t *testing.T) string {
		b, err := os.ReadFile(fixture(t, "claude-usage.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitAfter(strings.TrimSpace(string(b)), "\n")
		p := filepath.Join(t.TempDir(), "noresult.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines[:2], "")), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name  string
		env   func(t *testing.T, pidfile string) []string
		words string
	}{
		{"timeout", func(t *testing.T, pidfile string) []string {
			expireOnPid(t, pidfile, claudeUsageTimeout)
			return []string{"USAGE_PIDFILE=" + pidfile, "USAGE_BEFORE=hang"}
		}, "did not answer within 90 s"},
		// Its output closed and its leader never exits: ended at the
		// deadline, which the test expires once the end waits for the exit.
		{"output ends", func(t *testing.T, pidfile string) []string {
			expire := readDeadline(t)
			none := func([]string) {}
			starterHooks(t, none, none, func(argv []string) {
				if childKind(argv) == "usage" {
					expire()
				}
			}, none)
			return []string{"USAGE_STREAM=" + noResult(t), "USAGE_THEN=close"}
		}, "did not answer within 90 s"},
		{"not JSON, then no result", func(t *testing.T, pidfile string) []string {
			b, err := os.ReadFile(noResult(t))
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "bad.jsonl")
			if err := os.WriteFile(p, append([]byte("not json\n"), b...), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"USAGE_STREAM=" + p, "USAGE_THEN=fail"}
		}, "ended before it answered"},
		{"child exits", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + noResult(t), "USAGE_THEN=fail"}
		}, "ended before it answered (exit status 3; "},
		{"malformed", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `{"type":"result"`, "not json\n"+`{"type":"result"`)}
		}, "not JSON"},
		{"no turns", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"num_turns":0,`, ``)}
		}, "gave no turns"},
		{"no command", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"local_command":"usage",`, ``)}
		}, "named no local command"},
		// An explicit null is not an absent field: the error flag and the
		// subtype given as null fail closed (review round 3).
		{"error flag null", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"is_error":false`, `"is_error":null`)}
		}, "says the /usage run failed"},
		{"subtype null", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"type":"result","subtype":"success"`, `"type":"result","subtype":null`)}
		}, "says the /usage run failed"},
		{"bound", func(t *testing.T, pidfile string) []string {
			p := filepath.Join(t.TempDir(), "big.jsonl")
			if err := os.WriteFile(p, []byte(strings.Repeat("y", usageMaxLine+10)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"USAGE_STREAM=" + p}
		}, "longer than 1 MiB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			s := newStub(t, "claude", c.env(t, pidfile)...)
			adm := &fakeAdmit{}
			rep := claudeRead(t, s, adm)
			if rep.State != UsageError || rep.Outcome != Inconclusive || !strings.Contains(rep.Words, c.words) || len(rep.Windows) != 0 {
				t.Fatalf("%+v", rep)
			}
			if strings.Contains(rep.Words, "second line") {
				t.Fatalf("more than the first line of stderr: %q", rep.Words)
			}
			if got := adm.outcomes(); !slices.Equal(got, []Outcome{Inconclusive}) {
				t.Fatalf("Done %v", got)
			}
			if c.name == "timeout" {
				waitGone(t, readPid(t, pidfile))
			}
		})
	}
}

func TestClaudeUsageNewerVersionNotStarted(t *testing.T) {
	newer := "2.9.1 (Claude Code)"
	for name, adm := range map[string]*fakeAdmit{"nil": nil, "refused": {refuse: errors.New("2.9.1 was not checked")}} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "claude", "USAGE_VERSION="+newer)
			rep := claudeRead(t, s, adm)
			if rep.State != UsageUnqualified || rep.Version != "2.9.1" || rep.Outcome != Inconclusive || s.started(t, "-p") != 0 {
				t.Fatalf("%+v", rep)
			}
			if adm != nil && (rep.Words != "2.9.1 was not checked" || len(adm.outcomes()) != 0) {
				t.Fatalf("refused: %+v, done %v", rep, adm.outcomes())
			}
		})
	}
	s := newStub(t, "claude", "USAGE_VERSION="+newer)
	adm := &fakeAdmit{}
	if rep := claudeRead(t, s, adm); rep.State != UsageOK || rep.Version != "2.9.1" || s.started(t, "-p") != 1 || !slices.Equal(adm.outcomes(), []Outcome{Local}) || !slices.Equal(adm.asked, []string{"2.9.1"}) {
		t.Fatalf("admitted: %+v %v", rep, adm.outcomes())
	}
	// The qualified version too starts only once admitted.
	s = newStub(t, "claude")
	if rep := claudeRead(t, s, nil); rep.State != UsageUnqualified || s.started(t, "-p") != 0 {
		t.Fatalf("qualified, no admission: %+v", rep)
	}
	// Below MinVersion: not even auth status.
	s = newStub(t, "claude", "USAGE_VERSION=2.0.0 (Claude Code)")
	if rep := claudeRead(t, s, &fakeAdmit{}); rep.State != UsageUnqualified || !strings.Contains(rep.Words, "read only from "+claudeUsageMinVersion) || s.started(t, "auth") != 0 {
		t.Fatalf("old: %+v", rep)
	}
}

func authFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeUsageSignedOut(t *testing.T) {
	s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, `{"loggedIn": false, "authMethod": "none", "apiProvider": "firstParty"}`))
	adm := &fakeAdmit{}
	rep := claudeRead(t, s, adm)
	if rep.State != UsageSignedOut || rep.Version != claudeUsageQualified || s.started(t, "-p") != 0 || len(adm.asked) != 0 {
		t.Fatalf("%+v", rep)
	}
	for _, body := range []string{`{"authMethod": "claude.ai"}`, `not json`, ``} {
		s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, body))
		if rep := claudeRead(t, s, adm); rep.State != UsageError || !strings.Contains(rep.Words, "no answer Conductor reads") || s.started(t, "-p") != 0 {
			t.Fatalf("%q: %+v", body, rep)
		}
	}
}

func TestClaudeUsageKeyPaid(t *testing.T) {
	for name, body := range map[string]string{
		"key":     `{"loggedIn": true, "authMethod": "api_key", "apiProvider": "firstParty"}`,
		"bedrock": `{"loggedIn": true, "authMethod": "third_party", "apiProvider": "bedrock"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, body))
			adm := &fakeAdmit{}
			if rep := claudeRead(t, s, adm); rep.State != UsageKeyPaid || s.started(t, "-p") != 0 || len(adm.asked) != 0 {
				t.Fatalf("%+v", rep)
			}
		})
	}
	// An unknown sign-in is not read.
	s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, `{"loggedIn": true, "authMethod": "something_new", "apiProvider": "firstParty"}`))
	if rep := claudeRead(t, s, &fakeAdmit{}); rep.State != UsageError || s.started(t, "-p") != 0 {
		t.Fatalf("unknown method: %+v", rep)
	}
	// The init line's guard: a key pays this run, no window is kept.
	s = newStub(t, "claude", "USAGE_STREAM="+variant(t, "claude-usage.jsonl", `"apiKeySource":"none"`, `"apiKeySource":"ANTHROPIC_API_KEY"`))
	adm := &fakeAdmit{}
	rep := claudeRead(t, s, adm)
	if rep.State != UsageKeyPaid || len(rep.Windows) != 0 || len(rep.Credits) != 0 || rep.Outcome != Local || !slices.Equal(adm.outcomes(), []Outcome{Local}) {
		t.Fatalf("init guard: %+v", rep)
	}
}

func TestClaudeUsageUnknownWindow(t *testing.T) {
	for name, pairs := range map[string][]string{
		"kind":     {`"kind":"weekly_all"`, `"kind":"monthly_all"`},
		"severity": {`"severity":"normal"`, `"severity":"dire"`},
		"percent":  {`"percent":40`, `"percent":140`},
		"reset":    {`"resets_at":"2026-11-17T10:00:00.123456+00:00","scope":null`, `"resets_at":"next week","scope":null`},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "claude", "USAGE_STREAM="+variant(t, "claude-usage.jsonl", pairs...))
			adm := &fakeAdmit{}
			rep := claudeRead(t, s, adm)
			if rep.State != UsageError || rep.Outcome != Local || len(rep.Windows) != 0 || !strings.Contains(rep.Words, "limits[0]") {
				t.Fatalf("%+v", rep)
			}
		})
	}
}

func TestClaudeUsageArgv(t *testing.T) {
	s := newStub(t, "claude", "CLAUDE_CONFIG_DIR=/fixture/account")
	if rep := claudeRead(t, s, &fakeAdmit{}); rep.State != UsageOK {
		t.Fatalf("%+v", rep)
	}
	calls := s.calls(t)
	if len(calls) != 3 {
		t.Fatalf("calls %+v", calls)
	}
	want := [][]string{{"--version"}, claudeAuthArgs, claudeUsageArgs}
	dir, _ := filepath.EvalSymlinks(s.dir)
	for i, c := range calls {
		if !slices.Equal(c.Args, want[i]) {
			t.Errorf("run %d: args %q, want %q", i, c.Args, want[i])
		}
		if got, _ := filepath.EvalSymlinks(c.Dir); got != dir {
			t.Errorf("run %d: dir %s, want %s", i, c.Dir, s.dir)
		}
		if c.ConfigDir != "/fixture/account" {
			t.Errorf("run %d: CLAUDE_CONFIG_DIR %q", i, c.ConfigDir)
		}
		for _, k := range c.Env {
			if strings.HasPrefix(k, "CONDUCTOR_") {
				t.Errorf("run %d has %s", i, k)
			}
		}
		if !slices.Contains(c.Env, "TERM") || !slices.Contains(c.Env, "NO_COLOR") {
			t.Errorf("run %d: env %q", i, c.Env)
		}
	}
	if i := slices.Index(claudeUsageArgs, "--tools"); i < 0 || claudeUsageArgs[i+1] != "" {
		t.Fatalf("--tools is not given the empty list: %q", claudeUsageArgs)
	}
	if rep := readClaudeUsage(context.Background(), UsageInput{Command: self(t), Env: s.env}); rep.State != UsageError || !strings.Contains(rep.Words, "no folder") {
		t.Fatalf("no folder: %+v", rep)
	}
}

// TestClaudeUsageUnknownPayer: a login whose payment provider is missing,
// null or not one Conductor knows, or whose sign-in is an unknown one
// that merely looks like a key, is unknown (error, the field named), never
// the nearest guess; /usage is not started.
func TestClaudeUsageUnknownPayer(t *testing.T) {
	for name, c := range map[string]struct{ body, words string }{
		"no provider":      {`{"loggedIn": true, "authMethod": "claude.ai", "subscriptionType": "max"}`, "apiProvider"},
		"null provider":    {`{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": null, "subscriptionType": "max"}`, "apiProvider"},
		"unknown provider": {`{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "elsewhere", "subscriptionType": "max"}`, "apiProvider"},
		"key-like method":  {`{"loggedIn": true, "authMethod": "keychain_sso", "apiProvider": "firstParty"}`, "authMethod"},
		"no method":        {`{"loggedIn": true, "apiProvider": "firstParty"}`, "authMethod"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, c.body))
			adm := &fakeAdmit{}
			rep := claudeRead(t, s, adm)
			if rep.State != UsageError || !strings.Contains(rep.Words, c.words) || s.started(t, "-p") != 0 || len(adm.asked) != 0 {
				t.Fatalf("%+v", rep)
			}
		})
	}
	// The providers Conductor knows are key-paid, in its own words.
	for _, p := range []string{"bedrock", "vertex", "foundry"} {
		s := newStub(t, "claude", "USAGE_AUTH="+authFile(t, `{"loggedIn": true, "authMethod": "third_party", "apiProvider": "`+p+`"}`))
		if rep := claudeRead(t, s, &fakeAdmit{}); rep.State != UsageKeyPaid || strings.Contains(rep.Words, p) || s.started(t, "-p") != 0 {
			t.Fatalf("%s: %+v", p, rep)
		}
	}
}

// TestClaudeUsageFlagRefused: a version that no longer takes the turn
// bound's flag (--max-turns, which --help no longer lists) refuses it
// before any output; the read fails closed: unknown, in Conductor's words,
// with nothing proved, and no turn could have been taken.
func TestClaudeUsageFlagRefused(t *testing.T) {
	s := newStub(t, "claude", "USAGE_REFUSE=--max-turns")
	adm := &fakeAdmit{}
	rep := claudeRead(t, s, adm)
	if rep.State != UsageError || rep.Outcome != Inconclusive || !strings.Contains(rep.Words, "this version of the tool is not one Conductor has checked") || strings.Contains(rep.Words, "unknown option") {
		t.Fatalf("%+v", rep)
	}
	if got := adm.outcomes(); !slices.Equal(got, []Outcome{Inconclusive}) {
		t.Fatalf("Done %v", got)
	}
}

// The markers a stub plants in what it prints: one shaped as an account's
// email, one as a plain word. Neither may reach a report.
const (
	markMail = "fixture-marker-7a1c@example.invalid"
	markWord = "FixtureMarkerWord"
)

// noMarks fails when a report (as JSON, and as the recipes tier prints
// it) holds either marker.
func noMarks(t *testing.T, name string, rep UsageReport) {
	t.Helper()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, m := range []string{markMail, markWord, strings.ToLower(markWord)} {
		if strings.Contains(string(b), m) || strings.Contains(recipeSummary(rep), m) {
			t.Errorf("%s: the tool's text reached the report: %s", name, b)
		}
	}
}

// TestClaudeUsageKeepsNoToolWords: whatever Claude Code prints, on stderr or
// in a field Conductor reads, the report's words are Conductor's own: the
// markers planted there reach no report, and no word of the recipes
// tier's line.
func TestClaudeUsageKeepsNoToolWords(t *testing.T) {
	mark := "USAGE_MARK=" + markMail + " " + markWord
	cases := map[string][]string{
		"version":       {"USAGE_VERSION=" + markWord + " " + markMail},
		"child exits":   {"USAGE_THEN=fail", "USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `{"type":"result"`, `{"type":"other"`)},
		"auth not json": {"USAGE_AUTH=" + authFile(t, markMail+" "+markWord)},
		"auth method":   {"USAGE_AUTH=" + authFile(t, `{"loggedIn": true, "authMethod": "`+markWord+`", "apiProvider": "firstParty"}`)},
		"auth provider": {"USAGE_AUTH=" + authFile(t, `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "`+markWord+`"}`)},
		"plan":          {"USAGE_AUTH=" + authFile(t, `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty", "subscriptionType": "`+markWord+`"}`)},
		"kind":          {"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"kind":"weekly_all"`, `"kind":"`+markWord+`"`)},
		"severity":      {"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"severity":"normal"`, `"severity":"`+markWord+`"`)},
		"model name":    {"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"display_name":"Fixture Model"`, `"display_name":"`+markMail+`"`)},
		"currency":      {"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"currency":"USD"`, `"currency":"`+markWord+`"`)},
		"key source":    {"USAGE_STREAM=" + variant(t, "claude-usage.jsonl", `"apiKeySource":"none"`, `"apiKeySource":"`+markWord+`"`)},
		"flag refused":  {"USAGE_REFUSE=--max-turns"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			rep := claudeRead(t, newStub(t, "claude", append(kv, mark)...), &fakeAdmit{})
			if name == "plan" && rep.State != UsageOK {
				t.Fatalf("an unknown plan is not reported, and the read goes on: %+v", rep)
			}
			if name != "plan" && rep.State == UsageOK {
				t.Fatalf("read as ok: %+v", rep)
			}
			noMarks(t, name, rep)
		})
	}
}

// fieldRecorder reads a line's fields as jsonFields does, and notes the
// name of every field asked.
type fieldRecorder struct {
	f    jsonFields
	read map[string]bool
}

func (r fieldRecorder) get(name string, out any) fieldIs {
	r.read[name] = true
	return r.f.get(name, out)
}

// TestClaudeVerdictTable walks claudeVerdict, the one table from a result
// line's evidence to the run's outcome: every field the table reads, each
// absent, null, of a wrong type and at each valid value, in every
// combination with the others and with a line or part the stream could
// not read. A null takes the decision of a wrong type, the fail-closed one,
// for every field. The proof of a turn comes first, whatever else is
// there; Local needs all of its evidence and nothing against it; every
// other row is Inconclusive. The fields the table reads are noted as it
// reads them: a field it reads that the walk does not name, a field added
// later with no decision here, fails the test.
func TestClaudeVerdictTable(t *testing.T) {
	type value struct {
		label, json string // json "" for absent
		kind        string // absent, null, wrong (a wrong type) or valid
		says        string // what it says of the run, for want
	}
	walk := []struct {
		name   string
		values []value
	}{
		{"num_turns", []value{
			{"absent", "", "absent", "none"}, {"null", "null", "null", "none"},
			{"a string", `"1"`, "wrong", "none"}, {"a fraction", "0.5", "wrong", "none"}, {"a boolean", "true", "wrong", "none"}, {"an object", "{}", "wrong", "none"},
			{"-1", "-1", "valid", "none"}, {"0", "0", "valid", "zero"}, {"1", "1", "valid", "turn"}, {"3", "3", "valid", "turn"},
		}},
		{"local_command", []value{
			{"absent", "", "absent", "none"}, {"null", "null", "null", "none"},
			{"a number", "7", "wrong", "none"}, {"a boolean", "false", "wrong", "none"}, {"a list", `["usage"]`, "wrong", "none"},
			{"usage", `"usage"`, "valid", "usage"}, {"compact", `"compact"`, "valid", "other"}, {"empty", `""`, "valid", "other"},
		}},
		{"is_error", []value{
			{"absent", "", "absent", "clean"}, {"null", "null", "null", "failed"},
			{"a string", `"no"`, "wrong", "failed"}, {"a number", "0", "wrong", "failed"},
			{"false", "false", "valid", "clean"}, {"true", "true", "valid", "failed"},
		}},
		{"subtype", []value{
			{"absent", "", "absent", "clean"}, {"null", "null", "null", "failed"},
			{"a number", "3", "wrong", "failed"}, {"a boolean", "true", "wrong", "failed"},
			{"success", `"success"`, "valid", "clean"}, {"error_during_execution", `"error_during_execution"`, "valid", "failed"},
		}},
	}
	// The walk itself: each field absent, null, of a wrong type and valid,
	// and its null deciding as each of its wrong types does.
	for _, w := range walk {
		kinds := map[string]bool{}
		var null string
		for _, v := range w.values {
			kinds[v.kind] = true
			if v.kind == "null" {
				null = v.says
			}
		}
		for _, k := range []string{"absent", "null", "wrong", "valid"} {
			if !kinds[k] {
				t.Fatalf("%s: no %s value walked", w.name, k)
			}
		}
		for _, v := range w.values {
			if v.kind == "wrong" && v.says != null {
				t.Fatalf("%s: null says %s, %s says %s", w.name, null, v.label, v.says)
			}
		}
	}
	// The plan's rows, read in their order.
	want := func(says map[string]string, bad bool) Outcome {
		switch {
		case says["num_turns"] == "turn", says["local_command"] == "other":
			return Turned
		case bad, says["num_turns"] != "zero", says["local_command"] != "usage", says["is_error"] != "clean", says["subtype"] != "clean":
			return Inconclusive
		}
		return Local
	}
	read := map[string]bool{}
	rows := 0
	var visit func(i int, parts []string, says map[string]string, labels []string)
	visit = func(i int, parts []string, says map[string]string, labels []string) {
		if i < len(walk) {
			for _, v := range walk[i].values {
				p := parts
				if v.json != "" {
					p = append(slices.Clone(parts), `"`+walk[i].name+`":`+v.json)
				}
				s := maps.Clone(says)
				s[walk[i].name] = v.says
				visit(i+1, p, s, append(slices.Clone(labels), walk[i].name+" "+v.label))
			}
			return
		}
		line := `{` + strings.Join(append(slices.Clone(parts), `"type":"result"`), ",") + `}`
		var f jsonFields
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		for _, bad := range []bool{false, true} {
			why := ""
			if bad {
				why = "Claude Code printed a line that is not JSON"
			}
			got, words := claudeVerdict(fieldRecorder{f, read}, why)
			if w := want(says, bad); got != w {
				t.Errorf("%s, bad line %v: %v, want %v", strings.Join(labels, ", "), bad, got, w)
			}
			if (got == Inconclusive) != (words != "") {
				t.Errorf("%s: outcome %v with words %q", line, got, words)
			}
			rows++
		}
	}
	visit(0, nil, map[string]string{}, nil)
	total := 2
	for _, w := range walk {
		total *= len(w.values)
	}
	if rows != total {
		t.Fatalf("%d rows, want %d", rows, total)
	}
	var walked []string
	for _, w := range walk {
		walked = append(walked, w.name)
	}
	if got := slices.Sorted(maps.Keys(read)); !slices.Equal(got, slices.Sorted(slices.Values(walked))) {
		t.Fatalf("claudeVerdict reads %q; the walk decides %q: a field read needs its row here", got, walked)
	}

	// The one reader of a field: absent, null, a value and a wrong type
	// are four answers.
	var f jsonFields
	if err := json.Unmarshal([]byte(`{"n":null,"v":1,"w":"x"}`), &f); err != nil {
		t.Fatal(err)
	}
	var n int
	for name, is := range map[string]fieldIs{"a": fieldAbsent, "n": fieldNull, "v": fieldValue, "w": fieldWrong} {
		if got := f.get(name, &n); got != is {
			t.Errorf("get(%s): %v, want %v", name, got, is)
		}
	}

	// The rows the review rounds named, as literals.
	for line, w := range map[string]Outcome{
		`{"type":"result","local_command":"compact"}`:                                              Turned,
		`{"type":"result","num_turns":0,"local_command":"usage"}`:                                  Local,
		`{"type":"result","num_turns":0}`:                                                          Inconclusive,
		`{"type":"result","local_command":"usage"}`:                                                Inconclusive,
		`{"type":"result","num_turns":1,"local_command":"usage"}`:                                  Turned,
		`{"type":"result","num_turns":"x","local_command":"compact"}`:                              Turned,
		`{"type":"result","num_turns":0,"local_command":"usage","is_error":null,"subtype":null}`:   Inconclusive,
		`{"type":"result","num_turns":0,"local_command":"usage","is_error":null}`:                  Inconclusive,
		`{"type":"result","num_turns":0,"local_command":"usage","is_error":false,"subtype":null}`:  Inconclusive,
		`{"type":"result","num_turns":1,"local_command":"usage","is_error":null,"subtype":null}`:   Turned,
		`{"type":"result","num_turns":0,"local_command":"compact","is_error":null,"subtype":null}`: Turned,
	} {
		var f jsonFields
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatal(err)
		}
		if got, _ := claudeVerdict(f, ""); got != w {
			t.Errorf("%s: %v, want %v", line, got, w)
		}
	}
}
