//go:build recipes && unix

package agents

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// The usage readers against the real tools (make test-recipes). The
// schema and flag checks need no login and run nightly; the reads need
// the tool signed in and are skipped otherwise: they are the by-hand
// check of each version a reader's Qualified moves to, and a Claude Code
// version outside the checked range is read only when CONDUCTOR_USAGE_CHECK
// names it (recipeAdmission). Nothing a tool prints is written to disk,
// logged or printed: its output is held in memory, under a bound, only
// for the check, and a read is reported by recipeSummary alone (the
// version, the state, the outcome, which fields were present).

// usageRecipeMax bounds what one recipe command may print.
const usageRecipeMax = 1 << 20

// usageCheckEnv names the one Claude Code version a maintainer asks the
// recipes tier to check by hand, signed in: a version outside the
// reader's checked range is started only when it is named there.
const usageCheckEnv = "CONDUCTOR_USAGE_CHECK"

func usageRecipeCommand(t *testing.T, id string) []string {
	t.Helper()
	a, ok := catalog.Default().Get(id)
	if !ok || len(a.Command) == 0 {
		t.Fatalf("%s is not in the built-in catalog", id)
	}
	path, err := exec.LookPath(a.Command[0])
	if err != nil {
		if os.Getenv("CONDUCTOR_RECIPES_REQUIRE") == id {
			t.Fatalf("%s is required but %s is not on the PATH", id, a.Command[0])
		}
		t.Skipf("%s is not on the PATH", a.Command[0])
	}
	return append([]string{path}, a.Command[1:]...)
}

// usageRecipeRun runs the command with args as a reader runs its programs
// (startUsage: a session of its own with no terminal, stdin from nothing,
// its group ended on every path), its stdout and stderr together held in
// memory, at most usageRecipeMax bytes, within a minute; ok says it exited
// 0. The caller prints none of it.
func usageRecipeRun(t *testing.T, command []string, args ...string) (out string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	in := UsageInput{Env: append(os.Environ(), "CI=1"), Dir: t.TempDir()}
	proc, err := startUsage(ctx, append(append([]string{}, command...), args...), in, false, true)
	if err != nil {
		t.Fatalf("%s could not be started", filepath.Base(command[0]))
	}
	b, rerr := io.ReadAll(io.LimitReader(proc.out, usageRecipeMax+1))
	proc.end()
	if len(b) > usageRecipeMax {
		t.Fatalf("%s %s printed more than 1 MiB", filepath.Base(command[0]), strings.Join(args, " "))
	}
	return string(b), rerr == nil && ctx.Err() == nil && proc.cmd.ProcessState.Success()
}

// properties collects every property name of a JSON schema.
func properties(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if p, ok := x["properties"].(map[string]any); ok {
			for k := range p {
				into[k] = true
			}
		}
		for _, y := range x {
			properties(y, into)
		}
	case []any:
		for _, y := range x {
			properties(y, into)
		}
	}
}

// enumOf is the values of the schema's definition name, an enum.
func enumOf(t *testing.T, schema []byte, name string) []string {
	t.Helper()
	var v struct {
		Definitions map[string]struct {
			Enum []string `json:"enum"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(schema, &v); err != nil || len(v.Definitions[name].Enum) == 0 {
		t.Fatalf("the schema has no enum %s", name)
	}
	return v.Definitions[name].Enum
}

func TestRecipeCodexUsageSchema(t *testing.T) {
	codex := usageRecipeCommand(t, "codex")
	help, _ := usageRecipeRun(t, codex, "app-server", "--help")
	for _, w := range []string{"--listen", "stdio://", "generate-json-schema"} {
		if !strings.Contains(help, w) {
			t.Errorf("codex app-server --help does not name %s", w)
		}
	}
	out := t.TempDir()
	if _, ok := usageRecipeRun(t, codex, "app-server", "generate-json-schema", "--out", out); !ok {
		t.Fatal("codex app-server generate-json-schema failed")
	}
	files := map[string]string{}
	_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if _, seen := files[d.Name()]; !seen {
				files[d.Name()] = p
			}
		}
		return nil
	})
	read := func(name string) ([]byte, map[string]bool) {
		t.Helper()
		p, ok := files[name]
		if !ok {
			t.Fatalf("the schema has no %s", name)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s is not JSON", name)
		}
		props := map[string]bool{}
		properties(v, props)
		return b, props
	}
	requests, _ := read("ClientRequest.json")
	for _, m := range []string{codexUsageMethods[0], codexUsageMethods[2], codexUsageMethods[3]} {
		if !strings.Contains(string(requests), `"`+m+`"`) {
			t.Errorf("ClientRequest has no method %s", m)
		}
	}
	if notes, _ := read("ClientNotification.json"); !strings.Contains(string(notes), `"`+codexUsageMethods[1]+`"`) {
		t.Errorf("ClientNotification has no %s", codexUsageMethods[1])
	}
	if _, props := read("InitializeParams.json"); !props["clientInfo"] || !props["name"] || !props["version"] {
		t.Error("InitializeParams lacks clientInfo's fields")
	}
	text, props := read("GetAccountResponse.json")
	for _, f := range []string{"account", "requiresOpenaiAuth", "type", "planType"} {
		if !props[f] {
			t.Errorf("GetAccountResponse has no %s", f)
		}
	}
	for _, k := range []string{"apiKey", "chatgpt", "amazonBedrock"} {
		if !strings.Contains(string(text), `"`+k+`"`) {
			t.Errorf("GetAccountResponse has no account type %s", k)
		}
	}
	limits, props := read("GetAccountRateLimitsResponse.json")
	for _, f := range []string{"rateLimits", "rateLimitsByLimitId", "ordinaryUsageAllowed", "limitId", "limitName", "primary", "secondary", "credits", "planType", "rateLimitReachedType", "spendControlReached", "usedPercent", "windowDurationMins", "resetsAt", "hasCredits", "unlimited", "balance"} {
		if !props[f] {
			t.Errorf("GetAccountRateLimitsResponse has no %s", f)
		}
	}
	// The closed sets the reader reports from: a value the schema adds is
	// a change to read before it is shown.
	for _, c := range []struct {
		name  string
		known map[string]bool
	}{{"PlanType", codexPlans}, {"RateLimitReachedType", codexReached}} {
		for _, v := range enumOf(t, limits, c.name) {
			if !c.known[v] {
				t.Errorf("the schema's %s has %s, which the reader does not know", c.name, v)
			}
		}
	}
}

func TestRecipeClaudeUsageFlags(t *testing.T) {
	claude := usageRecipeCommand(t, "claude")
	help, _ := usageRecipeRun(t, claude, "--help")
	for _, f := range claudeUsageArgs {
		// --max-turns is taken and no longer listed (2.1.322); only a
		// /usage run takes it, the signed-in read below, and a version that
		// refuses it fails that read closed (claudeUsageArgs).
		if !strings.HasPrefix(f, "-") || f == "--max-turns" {
			continue
		}
		if !regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(f) + `([\s,=]|$)`).MatchString(help) {
			t.Errorf("flag %s is not in claude --help", f)
		}
	}
	for _, v := range []string{"stream-json", "local"} {
		if !strings.Contains(help, v) {
			t.Errorf("claude --help does not name %s", v)
		}
	}
	if h, _ := usageRecipeRun(t, claude, "auth", "status", "--help"); !strings.Contains(h, "--json") {
		t.Error("claude auth status --help does not name --json")
	}
	// auth status through the reader's own decode, which holds its four
	// fields alone; what is printed is which of them were there.
	in := UsageInput{Env: os.Environ(), Dir: t.TempDir()}
	p, err := resolveProgram(claude, in.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	auth, words := claudeAuthStatus(ctx, p, in)
	if words != "" {
		t.Fatalf("auth status --json: %s", words)
	}
	if *auth.LoggedIn {
		for k, v := range map[string]*string{"authMethod": auth.AuthMethod, "apiProvider": auth.APIProvider, "subscriptionType": auth.SubscriptionType} {
			if v == nil {
				t.Errorf("auth status --json gives no %s", k)
			}
		}
	}
}

// usageRecipeDir is the one folder the reads run in, as the server's is.
func usageRecipeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "conductor-usage-recipe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func recipeRead(t *testing.T, id string, read func(context.Context, UsageInput) UsageReport, adm *recipeAdmission) UsageReport {
	t.Helper()
	command := usageRecipeCommand(t, id)
	a, _ := Get(id)
	out, _ := usageRecipeRun(t, command, a.Probe.Args...)
	var probed string
	if m := a.Probe.Match.FindStringSubmatch(out); len(m) > 1 {
		probed = m[1]
	}
	in := UsageInput{Command: command, Env: os.Environ(), Dir: usageRecipeDir(t)}
	if adm != nil {
		in.Admit = adm.admit
	}
	rep := read(context.Background(), in)
	t.Logf("%s (qualified %s): %s", id, a.Usage.Qualified, recipeSummary(rep))
	switch {
	case rep.State == UsageSignedOut:
		t.Skipf("%s is signed out here", id)
	case adm != nil && slices.Contains(adm.refused, rep.Version):
		t.Skipf("%s %s is not a version the reader was checked on: %s=%s checks it, signed in", id, rep.Version, usageCheckEnv, rep.Version)
	}
	if rep.Version != probed {
		t.Errorf("the read found %s, the version's own print %s", rep.Version, probed)
	}
	if rep.State != UsageOK {
		t.Fatalf("the read did not answer ok: %s", recipeSummary(rep))
	}
	for i, w := range rep.Windows {
		if w.Kind == "" || w.Name == "" || w.Percent < 0 || w.Percent > 100 {
			t.Errorf("window %d is not read whole (kind %s)", i, w.Kind)
		}
	}
	return rep
}

func TestRecipeClaudeUsageRead(t *testing.T) {
	// The versions the reader was checked on, and the one a maintainer
	// names to check by hand; never any other.
	adm := &recipeAdmission{min: claudeUsageMinVersion, qualified: claudeUsageQualified, check: os.Getenv(usageCheckEnv)}
	rep := recipeRead(t, "claude", readClaudeUsage, adm)
	if got := adm.outcomes(); rep.Outcome != Local || !slices.Equal(got, []Outcome{Local}) {
		t.Fatalf("outcome %s, done %v", rep.Outcome, got)
	}
}

func TestRecipeCodexUsageRead(t *testing.T) {
	recipeRead(t, "codex", readCodexUsage, nil)
}
