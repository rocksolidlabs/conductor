//go:build recipes && unix

package agents

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// The usage readers against the real tools (make test-recipes). The
// schema and flag checks need no login and run nightly; the reads need
// the tool signed in and are skipped otherwise: they are the by-hand
// check of each version a reader's Qualified moves to. Nothing a tool
// prints about the account is logged or kept: a read is reported by its
// state and the tool's version.

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

// usageRecipeRun runs the command with args, its output into a file (a
// tool may exit before a pipe has taken all of its help).
func usageRecipeRun(t *testing.T, command []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, command[0], append(append([]string{}, command[1:]...), args...)...)
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=1")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = f, f
	runErr := cmd.Run()
	out, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
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

func TestRecipeCodexUsageSchema(t *testing.T) {
	codex := usageRecipeCommand(t, "codex")
	help, _ := usageRecipeRun(t, codex, "app-server", "--help")
	for _, w := range []string{"--listen", "stdio://", "generate-json-schema"} {
		if !strings.Contains(help, w) {
			t.Errorf("codex app-server --help does not name %s", w)
		}
	}
	out := t.TempDir()
	if text, err := usageRecipeRun(t, codex, "app-server", "generate-json-schema", "--out", out); err != nil {
		t.Fatalf("generate-json-schema: %v\n%s", err, text)
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
	read := func(name string) (string, map[string]bool) {
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
			t.Fatalf("%s: %v", name, err)
		}
		props := map[string]bool{}
		properties(v, props)
		return string(b), props
	}
	requests, _ := read("ClientRequest.json")
	for _, m := range []string{codexUsageMethods[0], codexUsageMethods[2], codexUsageMethods[3]} {
		if !strings.Contains(requests, `"`+m+`"`) {
			t.Errorf("ClientRequest has no method %s", m)
		}
	}
	if notes, _ := read("ClientNotification.json"); !strings.Contains(notes, `"`+codexUsageMethods[1]+`"`) {
		t.Errorf("ClientNotification has no %s", codexUsageMethods[1])
	}
	if init, props := read("InitializeParams.json"); !props["clientInfo"] || !props["name"] || !props["version"] {
		t.Errorf("InitializeParams lacks clientInfo's fields: %s", init)
	}
	text, props := read("GetAccountResponse.json")
	for _, f := range []string{"account", "requiresOpenaiAuth", "type", "planType"} {
		if !props[f] {
			t.Errorf("GetAccountResponse has no %s", f)
		}
	}
	for _, k := range []string{"apiKey", "chatgpt", "amazonBedrock"} {
		if !strings.Contains(text, `"`+k+`"`) {
			t.Errorf("GetAccountResponse has no account type %s", k)
		}
	}
	_, props = read("GetAccountRateLimitsResponse.json")
	for _, f := range []string{"rateLimits", "rateLimitsByLimitId", "ordinaryUsageAllowed", "limitId", "limitName", "primary", "secondary", "credits", "planType", "rateLimitReachedType", "spendControlReached", "usedPercent", "windowDurationMins", "resetsAt", "hasCredits", "unlimited", "balance"} {
		if !props[f] {
			t.Errorf("GetAccountRateLimitsResponse has no %s", f)
		}
	}
}

func TestRecipeClaudeUsageFlags(t *testing.T) {
	claude := usageRecipeCommand(t, "claude")
	help, _ := usageRecipeRun(t, claude, "--help")
	for _, f := range claudeUsageArgs {
		// --max-turns is taken and no longer listed (2.1.322); the
		// logged-in read proves it is taken.
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
	out, err := usageRecipeRun(t, claude, claudeAuthArgs...)
	var fields map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(out), &fields); jerr != nil {
		t.Fatalf("auth status --json is not JSON (%v, %v)", jerr, err)
	}
	var in bool
	if json.Unmarshal(fields["loggedIn"], &in) != nil {
		t.Fatal("auth status --json gives no loggedIn")
	}
	if in {
		for _, k := range []string{"authMethod", "apiProvider", "subscriptionType"} {
			if _, ok := fields[k]; !ok {
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

func recipeRead(t *testing.T, id string, read func(context.Context, UsageInput) UsageReport, adm *fakeAdmit) UsageReport {
	t.Helper()
	command := usageRecipeCommand(t, id)
	a, _ := Get(id)
	probe := RunProbe(context.Background(), command, *a.Probe, nil)
	in := UsageInput{Command: command, Env: os.Environ(), Dir: usageRecipeDir(t)}
	if adm != nil {
		in.Admit = adm.admit
	}
	rep := read(context.Background(), in)
	t.Logf("%s %s (qualified %s): %s", id, rep.Version, a.Usage.Qualified, rep.State)
	if rep.State == UsageSignedOut {
		t.Skipf("%s is signed out here", id)
	}
	if rep.Version != probe.Version {
		t.Errorf("the read found %s, the probe %s", rep.Version, probe.Version)
	}
	if rep.State != UsageOK {
		t.Fatalf("%s: %s", rep.State, rep.Words)
	}
	for i, w := range rep.Windows {
		if w.Kind == "" || w.Name == "" || w.Percent < 0 || w.Percent > 100 {
			t.Errorf("window %d is not read whole (kind %q)", i, w.Kind)
		}
	}
	return rep
}

func TestRecipeClaudeUsageRead(t *testing.T) {
	// The admin's check: every version is admitted here.
	adm := &fakeAdmit{}
	rep := recipeRead(t, "claude", readClaudeUsage, adm)
	if rep.Outcome != Local || len(adm.outcomes()) != 1 || adm.outcomes()[0] != Local {
		t.Fatalf("outcome %s, done %v", rep.Outcome, adm.outcomes())
	}
}

func TestRecipeCodexUsageRead(t *testing.T) {
	recipeRead(t, "codex", readCodexUsage, nil)
}
