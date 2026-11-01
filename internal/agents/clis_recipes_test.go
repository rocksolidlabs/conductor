//go:build recipes

package agents_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// The arguments a Brakeman turn is launched with, checked against the real
// CLIs as the recipes are (internal/catalog/recipes_ext_test.go): each flag
// must be a word of the CLI's help, and each Codex -c override a setting
// this Codex reads, which it proves by refusing a wrong value of it by name.
// No account is needed. A CLI that is not on the PATH is skipped, unless
// CONDUCTOR_RECIPES_REQUIRE names its agent, when its absence fails;
// CONDUCTOR_RECIPES_OUT, a directory, receives what each CLI printed. Runs
// with -tags recipes (make test-recipes).

// cli finds the agent's command on the PATH, the built-in catalog's.
func cli(t *testing.T, id string) []string {
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

// runCLI runs argv after the command, and returns what it printed and its
// error (a non-zero exit, a timeout). The output goes to a file, not a pipe:
// Claude Code 2.1.314 exits before a pipe has taken all of its help
// (229 of its 313 lines read, --tools not among them), and writes a file
// whole.
func runCLI(t *testing.T, command []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

func keepOut(t *testing.T, name, text string) {
	t.Helper()
	dir := os.Getenv("CONDUCTOR_RECIPES_OUT")
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o644); err != nil {
		t.Logf("keep %s: %v", name, err)
	}
}

// helpOf is the CLI's help, which must name its flags.
func helpOf(t *testing.T, id string) string {
	t.Helper()
	help, _ := runCLI(t, cli(t, id), "--help")
	keepOut(t, id+"-brakeman-help", help)
	return help
}

// inHelp checks that every flag of args (the word before any =) is a word
// of help.
func inHelp(t *testing.T, id, help string, args []string) {
	t.Helper()
	n := 0
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			continue
		}
		f := strings.SplitN(x, "=", 2)[0]
		if !regexp.MustCompile(`(^|[\s,\[|])` + regexp.QuoteMeta(f) + `([\s,=\]|]|$)`).MatchString(help) {
			t.Errorf("%s: flag %q is not in its CLI's help (verify the adapter)", id, f)
		}
		n++
	}
	if n == 0 {
		t.Errorf("%s: no flag in %q", id, args)
	}
}

func adapter(t *testing.T, id string) agents.Adapter {
	t.Helper()
	a, ok := agents.Get(id)
	if !ok {
		t.Fatalf("no adapter %s", id)
	}
	return a
}

// codexTakes runs Codex with overrides and a command that only reads its
// configuration: it fails when a setting holds a value Codex does not know.
func codexTakes(t *testing.T, name string, overrides ...string) (string, error) {
	t.Helper()
	out, err := runCLI(t, cli(t, "codex"), append(overrides, "features", "list")...)
	keepOut(t, "codex-brakeman-"+name, out)
	return out, err
}

func TestModelFlagsMatchTheCLIs(t *testing.T) {
	for _, id := range []string{"claude", "codex"} {
		t.Run(id, func(t *testing.T) {
			a := adapter(t, id)
			if a.ModelArgs == nil {
				t.Fatalf("%s has no model flag", id)
			}
			inHelp(t, id, helpOf(t, id), a.ModelArgs("some-model"))
		})
	}
}

func TestClaudeAllowedToolsMatchesTheCLI(t *testing.T) {
	inHelp(t, "claude", helpOf(t, "claude"), adapter(t, "claude").AllowTools("conductor"))
}

// Codex's approval mode for one server's tools: the override is read (a
// launch with it starts), and the setting is the one Codex names when it
// refuses another value, "approve" among its values.
func TestCodexToolApprovalMatchesTheCLI(t *testing.T) {
	cmd := []string{"-c", `mcp_servers.cprobe.command="true"`}
	allow := adapter(t, "codex").AllowTools("cprobe")
	if out, err := codexTakes(t, "allow", append(append([]string{}, cmd...), allow...)...); err != nil {
		t.Fatalf("codex refuses %q: %v\n%s", allow, err, out)
	}
	wrong := append([]string{}, cmd...)
	for _, x := range allow {
		wrong = append(wrong, strings.ReplaceAll(x, `"approve"`, `"not-a-mode"`))
	}
	out, err := codexTakes(t, "allow-wrong", wrong...)
	if err == nil || !strings.Contains(out, "mcp_servers.cprobe.default_tools_approval_mode") || !strings.Contains(out, "`approve`") {
		t.Fatalf("codex took a wrong approval mode, or names another setting or no approve: %v\n%s", err, out)
	}
}

func TestReadOnlyArgsMatchTheCLIs(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		inHelp(t, "claude", helpOf(t, "claude"), adapter(t, "claude").ReadOnlyArgs())
	})
	t.Run("codex", func(t *testing.T) {
		args := adapter(t, "codex").ReadOnlyArgs()
		help := helpOf(t, "codex")
		inHelp(t, "codex", help, args)
		// The sandbox's value is one Codex lists.
		if len(args) < 2 || args[0] != "--sandbox" || !regexp.MustCompile(`possible values:[^\]]*\b`+regexp.QuoteMeta(args[1])+`\b`).MatchString(help) {
			t.Errorf("codex: sandbox %q is not among the values its help lists", args)
		}
		// The approval policy is read, and each setting it turns down is
		// one Codex requires of the granular policy: without it Codex
		// names it missing.
		if out, err := codexTakes(t, "read-only", args...); err != nil {
			t.Fatalf("codex refuses %q: %v\n%s", args, err, out)
		}
		policy := ""
		for i, x := range args {
			if x == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], "approval_policy=") {
				policy = args[i+1]
			}
		}
		if !strings.Contains(policy, "granular=") {
			t.Fatalf("codex: no granular approval policy in %q", args)
		}
		for _, field := range []string{"sandbox_approval", "rules", "mcp_elicitations"} {
			without := regexp.MustCompile(`\b`+field+`=[a-z]+,?`).ReplaceAllString(policy, "")
			without = strings.ReplaceAll(without, ",}", "}")
			out, err := codexTakes(t, "without-"+field, "-c", without)
			if err == nil || !strings.Contains(out, "missing field `"+field+"`") {
				t.Errorf("codex: %s is not a field of its granular policy (%q): %v\n%s", field, without, err, out)
			}
		}
		if out, err := codexTakes(t, "policy-wrong", "-c", `approval_policy="not-a-policy"`); err == nil || !strings.Contains(out, "`granular`") {
			t.Errorf("codex names no granular approval policy: %v\n%s", err, out)
		}
	})
}
