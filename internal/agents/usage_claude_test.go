//go:build unix

package agents

import (
	"context"
	"encoding/json"
	"errors"
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
		{"output ends", func(t *testing.T, pidfile string) []string {
			return []string{"USAGE_STREAM=" + noResult(t), "USAGE_THEN=close"}
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
