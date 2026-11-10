//go:build unix

package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func codexRead(t *testing.T, s *stub) UsageReport {
	t.Helper()
	return readCodexUsage(context.Background(), s.input(self(t), nil))
}

func (s *stub) methods(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range s.calls(t) {
		if c.Method != "" {
			out = append(out, c.Method)
		}
	}
	return out
}

func jsonFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexUsageMethodsClosed(t *testing.T) {
	if codexUsageMethods != [4]string{"initialize", "initialized", "account/read", "account/rateLimits/read"} {
		t.Fatalf("%q", codexUsageMethods)
	}
	for _, m := range codexUsageMethods {
		if strings.HasPrefix(m, "config/") {
			t.Fatalf("%s is sent", m)
		}
	}
}

func TestCodexUsageExchange(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	s := newStub(t, "codex", "USAGE_PIDFILE="+pidfile, "USAGE_THEN=hang", "CODEX_HOME=/fixture/codex")
	rep := codexRead(t, s)
	if rep.State != UsageOK || rep.Version != codexUsageQualified || rep.Outcome != Inconclusive {
		t.Fatalf("%+v", rep)
	}
	if got := s.methods(t); !slices.Equal(got, codexUsageMethods[:]) {
		t.Fatalf("methods %q, want %q", got, codexUsageMethods)
	}
	for _, c := range s.calls(t) {
		if c.Method == "" && len(c.Args) > 0 && c.Args[0] == "app-server" && !slices.Equal(c.Args, codexAppServerArgs) {
			t.Fatalf("args %q", c.Args)
		}
	}
	// The helper stays after its stdin closed: the read ends its group.
	waitGone(t, readPid(t, pidfile))
}

func TestCodexUsageWindows(t *testing.T) {
	rep := codexRead(t, newStub(t, "codex"))
	if rep.State != UsageOK || rep.Plan != "pro" {
		t.Fatalf("%+v", rep)
	}
	if len(rep.Windows) != 3 {
		t.Fatalf("windows %+v", rep.Windows)
	}
	week, five, other := rep.Windows[0], rep.Windows[1], rep.Windows[2]
	if week.Kind != "primary" || week.Name != "Weekly" || week.Minutes != 10080 || week.Percent != 7 || week.Limit != "codex" || week.ResetsAt == nil || !week.ResetsAt.Equal(time.Unix(1794830400, 0)) || week.Severity != "" || week.Active != nil {
		t.Fatalf("weekly %+v", week)
	}
	if five.Kind != "secondary" || five.Name != "5-hour" || five.Minutes != 300 || five.Percent != 33 {
		t.Fatalf("5-hour %+v", five)
	}
	// The other limit's null secondary is absent, its null reset nil.
	if other.Kind != "primary" || other.Name != "Fixture model, Weekly" || other.Limit != "codex_fixture" || other.ResetsAt != nil || other.Percent != 0 {
		t.Fatalf("other %+v", other)
	}
	if len(rep.Credits) != 1 || rep.Credits[0].Kind != "credits" || rep.Credits[0].Balance == nil || *rep.Credits[0].Balance != "1250" || rep.Credits[0].Enabled == nil || !*rep.Credits[0].Enabled {
		t.Fatalf("credits %+v", rep.Credits)
	}
	if rep.Limits.OrdinaryAllowed == nil || !*rep.Limits.OrdinaryAllowed || rep.Limits.SpendControl == nil || *rep.Limits.SpendControl || rep.Limits.Reached != "" {
		t.Fatalf("limits %+v", rep.Limits)
	}
	b, _ := json.Marshal(rep)
	for _, x := range []string{"fixture-limits-account-id", "fixture-routing-account-id", "fixture-person", "example.invalid", "fixture-reset-credit-id", "/fixture"} {
		if strings.Contains(string(b), x) {
			t.Errorf("the report holds %q", x)
		}
	}
	// Without the by-id view, the single bucket is read.
	s := newStub(t, "codex", "USAGE_LIMITS="+jsonFile(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":50,"windowDurationMins":120,"resetsAt":null},"secondary":{"usedPercent":5,"windowDurationMins":45}},"rateLimitsByLimitId":null}`))
	rep = codexRead(t, s)
	if rep.State != UsageOK || len(rep.Windows) != 2 || rep.Windows[0].Name != "2-hour" || rep.Windows[1].Name != "45-minute" || rep.Limits.OrdinaryAllowed != nil || rep.Limits.SpendControl != nil {
		t.Fatalf("single bucket: %+v", rep)
	}
}

func TestCodexUsageLimitsWithoutWindows(t *testing.T) {
	s := newStub(t, "codex", "USAGE_LIMITS="+jsonFile(t, `{"ordinaryUsageAllowed":false,"rateLimits":{"limitId":"codex","primary":null,"secondary":null,"credits":null,"spendControlReached":false,"rateLimitReachedType":"rate_limit_reached"}}`))
	rep := codexRead(t, s)
	if rep.State != UsageOK || len(rep.Windows) != 0 || len(rep.Credits) != 0 {
		t.Fatalf("%+v", rep)
	}
	l := rep.Limits
	if l.Reached != "rate_limit_reached" || l.SpendControl == nil || *l.SpendControl || l.OrdinaryAllowed == nil || *l.OrdinaryAllowed {
		t.Fatalf("limits %+v", l)
	}
	// A window under a reached limit is critical.
	s = newStub(t, "codex", "USAGE_LIMITS="+jsonFile(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300},"rateLimitReachedType":"rate_limit_reached"}}`))
	if rep := codexRead(t, s); rep.State != UsageOK || len(rep.Windows) != 1 || rep.Windows[0].Severity != "critical" || rep.Limits.SpendControl != nil || rep.Limits.OrdinaryAllowed != nil {
		t.Fatalf("reached: %+v", rep)
	}
}

func TestCodexUsageKeyPaid(t *testing.T) {
	for _, body := range []string{`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`, `{"account":{"type":"amazonBedrock"},"requiresOpenaiAuth":false}`} {
		s := newStub(t, "codex", "USAGE_ACCOUNT="+jsonFile(t, body))
		rep := codexRead(t, s)
		if rep.State != UsageKeyPaid || len(rep.Windows) != 0 || slices.Contains(s.methods(t), "account/rateLimits/read") {
			t.Fatalf("%s: %+v, methods %q", body, rep, s.methods(t))
		}
	}
}

func TestCodexUsageSignedOut(t *testing.T) {
	s := newStub(t, "codex", "USAGE_ACCOUNT="+jsonFile(t, `{"account":null,"requiresOpenaiAuth":true}`))
	rep := codexRead(t, s)
	if rep.State != UsageSignedOut || slices.Contains(s.methods(t), "account/rateLimits/read") {
		t.Fatalf("%+v", rep)
	}
	s = newStub(t, "codex", "USAGE_ACCOUNT="+jsonFile(t, `{"account":null,"requiresOpenaiAuth":false}`))
	if rep := codexRead(t, s); rep.State != UsageError {
		t.Fatalf("no login needed: %+v", rep)
	}
}

func TestCodexUsageFormatChanged(t *testing.T) {
	for name, body := range map[string]string{
		"percent":  `{"rateLimits":{"primary":{"usedPercent":"most","windowDurationMins":300}}}`,
		"range":    `{"rateLimits":{"primary":{"usedPercent":101,"windowDurationMins":300}}}`,
		"missing":  `{"rateLimits":{"primary":{"windowDurationMins":300}}}`,
		"none":     `{"ordinaryUsageAllowed":true}`,
		"duration": `{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":30.5}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "codex", "USAGE_LIMITS="+jsonFile(t, body))
			if rep := codexRead(t, s); rep.State != UsageError || len(rep.Windows) != 0 || rep.Words == "" {
				t.Fatalf("%+v", rep)
			}
		})
	}
	s := newStub(t, "codex", "USAGE_ACCOUNT="+jsonFile(t, `{"account":{"type":"someNewKind"},"requiresOpenaiAuth":true}`))
	if rep := codexRead(t, s); rep.State != UsageError || !strings.Contains(rep.Words, "type Conductor does not know") || strings.Contains(rep.Words, "someNewKind") {
		t.Fatalf("unknown account: %+v", rep)
	}
}

// TestCodexUsageResetOutOfRange: a reset time that is no time a report can
// carry (past year 9999, or given in milliseconds) fails the read with
// its field, never a report that cannot be written.
func TestCodexUsageResetOutOfRange(t *testing.T) {
	for name, at := range map[string]string{"year 10000": "253402300800", "milliseconds": "1794830400000", "negative": "-1"} {
		t.Run(name, func(t *testing.T) {
			s := newStub(t, "codex", "USAGE_LIMITS="+jsonFile(t, `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":`+at+`}}}`))
			rep := codexRead(t, s)
			if _, err := json.Marshal(rep); err != nil {
				t.Fatalf("the report cannot be written: %v", err)
			}
			if rep.State != UsageError || !strings.Contains(rep.Words, "resetsAt") || len(rep.Windows) != 0 {
				t.Fatalf("%+v", rep)
			}
		})
	}
}

// TestCodexUsageKeepsNoToolWords: Codex's own error messages, an unknown
// account type or plan, and names or figures of a shape Conductor does not
// show reach no report.
func TestCodexUsageKeepsNoToolWords(t *testing.T) {
	mark := "USAGE_MARK=" + markMail + " " + markWord
	limits := func(old, new string) string {
		return "USAGE_LIMITS=" + variant(t, "codex-limits.json", old, new)
	}
	cases := map[string][]string{
		"version":      {"USAGE_VERSION=" + markWord},
		"rpc error":    {"USAGE_RPC_ERROR=account/read"},
		"account type": {"USAGE_ACCOUNT=" + jsonFile(t, `{"account":{"type":"`+markWord+`"},"requiresOpenaiAuth":true}`)},
		"plan":         {"USAGE_ACCOUNT=" + jsonFile(t, `{"account":{"type":"chatgpt","planType":"`+markWord+`"},"requiresOpenaiAuth":true}`), limits(`"planType":"pro"`, `"planType":"`+markWord+`"`)},
		"limit name":   {limits(`"limitName":"Fixture model"`, `"limitName":"`+markMail+`"`)},
		"limit id":     {limits(`"codex_fixture":{"limitId":"codex_fixture"`, `"`+markMail+`":{"limitId":null`)},
		"reached":      {limits(`"spendControlReached":null,"planType":"pro","rateLimitReachedType":null`, `"spendControlReached":null,"planType":"pro","rateLimitReachedType":"`+markWord+`"`)},
		"balance":      {limits(`"balance":"1250"},"individualLimit":null,"spendControlReached":false,"planType":"pro","rateLimitReachedType":null},"rateLimitsByLimitId"`, `"balance":"`+markWord+`"},"individualLimit":null,"spendControlReached":false,"planType":"pro","rateLimitReachedType":null},"rateLimitsByLimitId":null,"x"`)},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			rep := codexRead(t, newStub(t, "codex", append(kv, mark)...))
			if name == "plan" && (rep.State != UsageOK || rep.Plan != "") {
				t.Fatalf("an unknown plan is not reported, and the read goes on: %+v", rep)
			}
			if name != "plan" && rep.State == UsageOK {
				t.Fatalf("read as ok: %+v", rep)
			}
			noMarks(t, name, rep)
		})
	}
}
