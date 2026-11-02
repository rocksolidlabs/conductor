//go:build recipes

package catalog_test

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

// The recipe checks against the real CLIs, no accounts needed (by-hand
// items 4 and 5 of round 4, as far as a help text tells): each built-in
// agent on this machine's PATH is asked its version (the identity probe
// must know it) and its help, and every flag of its yolo recipe, its
// session recipe and its adapter's launch injection must be a word of that
// help (or of the subcommand's help the recipe names), or a flag the CLI
// hides from its help and takes all the same (hiddenFlags). A CLI that is
// not on the PATH is skipped, unless CONDUCTOR_RECIPES_REQUIRE names it,
// when its absence fails. CONDUCTOR_RECIPES_OUT, a directory, receives each
// CLI's version and help output for the nightly artifacts. Runs with
// -tags recipes (make test-recipes).
func cliOf(t *testing.T, a catalog.Agent) (string, bool) {
	t.Helper()
	if len(a.Command) == 0 {
		return "", false
	}
	path, err := exec.LookPath(a.Command[0])
	required := os.Getenv("CONDUCTOR_RECIPES_REQUIRE") == a.ID
	if err != nil {
		if required {
			t.Fatalf("%s is required but %s is not on the PATH", a.ID, a.Command[0])
		}
		t.Skipf("%s is not on the PATH", a.Command[0])
		return "", false
	}
	return path, true
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, _ := runStatus(t, name, args...)
	return out
}

// runStatus is run with the command's error too: a non-zero exit, a
// timeout. The output goes to a file, not a pipe: Claude Code 2.1.314 exits
// before a pipe has taken all of its help (229 of its 313 lines read,
// --session-id not among them), and writes a file whole.
func runStatus(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, name, args...)
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

func keep(t *testing.T, id, kind, text string) {
	t.Helper()
	dir := os.Getenv("CONDUCTOR_RECIPES_OUT")
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, id+"-"+kind+".txt"), []byte(text), 0o644); err != nil {
		t.Logf("keep %s: %v", kind, err)
	}
}

func TestRecipeProbesIdentify(t *testing.T) {
	for _, a := range catalog.Default().List() {
		t.Run(a.ID, func(t *testing.T) {
			path, ok := cliOf(t, a)
			if !ok {
				return
			}
			p := agents.ProbeFor(a.Adapter)
			if p == nil {
				t.Skipf("%s has no probe", a.ID)
			}
			res := agents.RunProbe(context.Background(), append([]string{path}, a.Command[1:]...), *p, a.Env)
			keep(t, a.ID, "version", run(t, path, append(a.Command[1:], p.Args...)...))
			switch {
			case res.Identified:
				t.Logf("%s: %s %s", a.ID, a.Command[0], res.Version)
			case res.Impostor:
				t.Errorf("%s: %s is an impostor: %q", a.ID, path, res.Output)
			case !res.Ran:
				t.Errorf("%s: the probe did not run: %s", a.ID, res.Error)
			default:
				t.Errorf("%s: unidentified; %s %s printed %q (close the verify row with it)", a.ID, a.Command[0], strings.Join(p.Args, " "), res.Output)
			}
		})
	}
}

// recipeWords are the flags and subcommands a launch may pass: the yolo
// recipe, the session recipe (start and resume), the adapter's injection
// (hooks and yolo). "{id}" and values are not flags.
func recipeWords(a catalog.Agent) (flags, subcommands []string) {
	add := func(args []string) {
		for i, x := range args {
			switch {
			case strings.HasPrefix(x, "-"):
				flags = append(flags, strings.SplitN(x, "=", 2)[0])
			case i == 0 && regexp.MustCompile(`^[a-z][a-z-]*$`).MatchString(x):
				subcommands = append(subcommands, x)
			}
		}
	}
	if a.Yolo != nil {
		add(a.Yolo.Args)
	}
	if a.Session != nil {
		add(a.Session.StartArgs)
		add(a.Session.ResumeArgs)
	}
	for _, yolo := range []bool{false, true} {
		argv, _ := agents.InjectFor(a.Adapter, "/tmp/conductor-hooks", a.EffectiveSignal(), yolo)
		add(argv)
	}
	return flags, subcommands
}

// hiddenFlags are the flags a CLI takes though its help leaves them out,
// each with the arguments that check it instead: the flag goes before a
// command that only prints, which a parser that did not know the flag
// refuses ("unknown option").
var hiddenFlags = map[string]map[string][]string{
	// Amp's own refusals say "Rerun with --dangerously-allow-all to
	// bypass". `version`, not --version: Amp prints its version for
	// --version before it looks at the other flags.
	"amp": {"--dangerously-allow-all": {"version"}},
}

func TestRecipeFlagsAreInHelp(t *testing.T) {
	for _, a := range catalog.Default().List() {
		t.Run(a.ID, func(t *testing.T) {
			path, ok := cliOf(t, a)
			if !ok {
				return
			}
			if p := agents.ProbeFor(a.Adapter); p != nil {
				if res := agents.RunProbe(context.Background(), append([]string{path}, a.Command[1:]...), *p, a.Env); res.Impostor {
					t.Skipf("%s on the PATH is an impostor (%q): its help says nothing about %s", a.Command[0], res.Output, a.Name)
				}
			}
			help := run(t, path, append(a.Command[1:], "--help")...)
			keep(t, a.ID, "help", help)
			flags, subs := recipeWords(a)
			for _, sub := range subs {
				subHelp := run(t, path, append(append(a.Command[1:], sub), "--help")...)
				keep(t, a.ID, "help-"+sub, subHelp)
				if !regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(sub) + `\b`).MatchString(help) {
					t.Errorf("%s: subcommand %q is not in `%s --help`", a.ID, sub, a.Command[0])
				}
				help += "\n" + subHelp
			}
			for _, f := range flags {
				if regexp.MustCompile(`(^|[\s,\[|])` + regexp.QuoteMeta(f) + `([\s,=\]|]|$)`).MatchString(help) {
					continue
				}
				check, hidden := hiddenFlags[a.ID][f]
				if !hidden {
					t.Errorf("%s: flag %q is not in the help of %s (verify the recipe)", a.ID, f, a.Command[0])
					continue
				}
				args := append(append(append([]string{}, a.Command[1:]...), f), check...)
				out, err := runStatus(t, path, args...)
				keep(t, a.ID, "takes-"+strings.TrimLeft(f, "-"), out)
				if err != nil || strings.Contains(out, "unknown option") {
					t.Errorf("%s: flag %q is not in the help of %s, and `%s %s` refuses it: %v, %q (verify the recipe)", a.ID, f, a.Command[0], a.Command[0], strings.Join(args, " "), err, out)
					continue
				}
				t.Logf("%s: flag %q is hidden from the help; `%s %s` takes it", a.ID, f, a.Command[0], strings.Join(args, " "))
			}
			t.Logf("%s: %d flags and %d subcommands checked", a.ID, len(flags), len(subs))
		})
	}
}

// TestRecipeEnvIsDocumented logs the variables the recipes set, for the
// nightly artifacts; a help text rarely lists them.
func TestRecipeEnvIsDocumented(t *testing.T) {
	for _, a := range catalog.Default().List() {
		if a.Yolo != nil && len(a.Yolo.Env) > 0 {
			t.Logf("%s: yolo env %v", a.ID, a.Yolo.Env)
		}
	}
}
