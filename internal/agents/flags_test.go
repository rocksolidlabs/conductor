package agents

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/catalog"
)

var updateFlags = flag.Bool("update-flags", false, "rewrite testdata/flags/*.golden.json from the parser")

func builtIn(t *testing.T, id string) catalog.Agent {
	t.Helper()
	a, ok := catalog.Default().Get(id)
	if !ok {
		t.Fatalf("%s is not in the built-in catalog", id)
	}
	return a
}

func flagsFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "flags", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parse(t *testing.T, a catalog.Agent, help string) []Flag {
	t.Helper()
	flags, err := ParseFlags(a, help)
	if err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	return flags
}

func flagNamed(t *testing.T, flags []Flag, name string) Flag {
	t.Helper()
	for _, f := range flags {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %s among %d flags", name, len(flags))
	return Flag{}
}

func names(flags []Flag) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		out = append(out, f.Name)
	}
	return out
}

func values(cs []Choice) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Value)
	}
	return out
}

// Each form's fixture, written for these tests in each tool's form and in
// our own words, read to a golden list: a change of a rule shows in the
// golden file's diff (go test -run TestFlagsFixtures -update-flags).
func TestFlagsFixtures(t *testing.T) {
	plain := builtIn(t, "claude")
	plain.Adapter = ""
	for _, c := range []struct {
		fixture string
		agent   catalog.Agent
		rows    int
	}{
		{"commander.txt", builtIn(t, "claude"), 18},
		{"clap.txt", builtIn(t, "codex"), 14},
		// The e2e stub's help as it is: three bare rows, the others not
		// in a row's form (a value word without brackets), and no
		// placeholder anywhere, so every value is unknown.
		{"plain.txt", plain, 3},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			flags := parse(t, c.agent, flagsFixture(t, c.fixture))
			if len(flags) != c.rows {
				t.Errorf("%d rows, want %d: %v", len(flags), c.rows, names(flags))
			}
			got, err := json.MarshalIndent(flags, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "flags", strings.TrimSuffix(c.fixture, ".txt")+".golden.json")
			if *updateFlags {
				if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got)+"\n" != string(want) {
				t.Errorf("the list differs from %s (rerun with -update-flags and read the diff):\n%s", golden, got)
			}
		})
	}
	flags := parse(t, plain, flagsFixture(t, "plain.txt"))
	if !slices.Equal(names(flags), []string{"--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox", "--version"}) {
		t.Fatalf("the stub's rows: %v", names(flags))
	}
	for _, f := range flags {
		if f.Value != ValueUnknown {
			t.Errorf("%s: value %s in a help with no placeholder", f.Name, f.Value)
		}
	}
}

// The fixture of the plain form is the e2e stub's own help, as it prints it
// today: the two must not drift apart.
func TestFlagsPlainFixtureIsTheStubsHelp(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this machine")
	}
	out, err := exec.Command(bash, filepath.Join("..", "..", "web", "e2e", "stub-agent.sh"), "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != flagsFixture(t, "plain.txt") {
		t.Fatalf("the stub's help is not testdata/flags/plain.txt:\n%s", out)
	}
}

// What the commander form gives, for the built-in Claude Code entry.
func TestFlagsCommanderForm(t *testing.T) {
	flags := parse(t, builtIn(t, "claude"), flagsFixture(t, "commander.txt"))
	addDir := flagNamed(t, flags, "--add-dir")
	if addDir.Value != ValueRequired || !addDir.Repeat || addDir.Placeholder != "directories" || addDir.Kind != KindFolder || addDir.Set != "" {
		t.Errorf("--add-dir: %+v", addDir)
	}
	allowed := flagNamed(t, flags, "--allowedTools")
	if !slices.Equal(allowed.Aliases, []string{"--allowed-tools"}) || allowed.Placeholder != "tools" || !allowed.Repeat || allowed.Examples != nil {
		t.Errorf("--allowedTools, its words on the next line: %+v", allowed)
	}
	if allowed.About != `Tools that run without a question (e.g. "Bash(git *) Edit")` {
		t.Errorf("--allowedTools about %q", allowed.About)
	}
	cont := flagNamed(t, flags, "--continue")
	if !slices.Equal(cont.Aliases, []string{"-c"}) || cont.Value != ValueNone {
		t.Errorf("-c, --continue: %+v", cont)
	}
	wt := flagNamed(t, flags, "--worktree")
	if wt.Value != ValueOptional || wt.Placeholder != "name" || wt.About != "Works in a new worktree (optionally named)" {
		t.Errorf("--worktree: %+v", wt)
	}
	if f := flagNamed(t, flags, "--verbose"); f.Value != ValueNone || f.Kind != "" || f.Set != "" {
		t.Errorf("--verbose: %+v", f)
	}
	// The model flag: Claude Code's ChoosesModel, and the examples.
	model := flagNamed(t, flags, "--model")
	if model.Kind != KindModel || model.Set != SetModel || !slices.Equal(model.Examples, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("--model: %+v", model)
	}
	// A model by its placeholder alone is of kind model, not set model.
	if f := flagNamed(t, flags, "--fallback-model"); f.Kind != KindModel || f.Set != "" || f.Choices != nil {
		t.Errorf("--fallback-model: %+v", f)
	}
	for name, set := range map[string]string{
		"--dangerously-skip-permissions":       SetYolo, // the yolo recipe's
		"--allow-dangerously-skip-permissions": SetYolo, // a Skips flag
		"--session-id":                         SetSession,
		"--settings":                           SetLaunch,
		"--help":                               SetExit,
		"--version":                            SetExit,
		"--permission-mode":                    "",
	} {
		if f := flagNamed(t, flags, name); f.Set != set {
			t.Errorf("%s: set %q, want %q", name, f.Set, set)
		}
	}
	// The Skips value: one choice of --permission-mode is set yolo, the
	// others not.
	pm := flagNamed(t, flags, "--permission-mode")
	if pm.Kind != KindChoice || !slices.Equal(values(pm.Choices), []string{"ask", "bypassPermissions", "plan"}) {
		t.Fatalf("--permission-mode: %+v", pm)
	}
	for _, c := range pm.Choices {
		if want := map[bool]string{true: SetYolo, false: ""}[c.Value == "bypassPermissions"]; c.Set != want {
			t.Errorf("--permission-mode %s: set %q, want %q", c.Value, c.Set, want)
		}
	}
	if f := flagNamed(t, flags, "--input-format"); !slices.Equal(values(f.Choices), []string{"text", "stream-json"}) {
		t.Errorf("--input-format: %+v", f.Choices)
	}
	// The fourth form, a list in words, and what it must not take.
	if f := flagNamed(t, flags, "--effort"); f.Kind != KindChoice || !slices.Equal(values(f.Choices), []string{"low", "medium", "high"}) {
		t.Errorf("--effort: %+v", f)
	}
	for _, name := range []string{"--autocompact", "--fallback-model", "--sources", "--allowedTools"} {
		if f := flagNamed(t, flags, name); f.Choices != nil {
			t.Errorf("%s took %v as values", name, f.Choices)
		}
	}
}

// What the clap form gives, for the built-in Codex entry.
func TestFlagsClapForm(t *testing.T) {
	flags := parse(t, builtIn(t, "codex"), flagsFixture(t, "clap.txt"))
	sandbox := flagNamed(t, flags, "--sandbox")
	if sandbox.Kind != KindChoice || !slices.Equal(values(sandbox.Choices), []string{"read-only", "workspace-write", "danger-full-access"}) || !slices.Equal(sandbox.Aliases, []string{"-s"}) {
		t.Fatalf("--sandbox: %+v", sandbox)
	}
	if sandbox.About != "How the commands it runs are fenced in" {
		t.Errorf("--sandbox about %q: the first paragraph alone", sandbox.About)
	}
	ask := flagNamed(t, flags, "--ask-for-approval")
	if !slices.Equal(values(ask.Choices), []string{"on-request", "never"}) || ask.Choices[0].About != "The model asks when it wants to" || ask.Choices[1].About != "Never asks" {
		t.Fatalf("--ask-for-approval, the block form: %+v", ask.Choices)
	}
	// The Skips values.
	for _, c := range append(slices.Clone(sandbox.Choices), ask.Choices...) {
		want := ""
		if c.Value == "danger-full-access" || c.Value == "never" {
			want = SetYolo
		}
		if c.Set != want {
			t.Errorf("%s: set %q, want %q", c.Value, c.Set, want)
		}
	}
	if f := flagNamed(t, flags, "--local-provider"); !slices.Equal(values(f.Choices), []string{"first", "second"}) {
		t.Errorf("--local-provider, a list in words with or: %+v", f.Choices)
	}
	model := flagNamed(t, flags, "--model")
	if model.Kind != KindModel || model.Set != SetModel || !slices.Equal(model.Aliases, []string{"-m"}) || model.Examples != nil {
		t.Errorf("--model: %+v", model)
	}
	cd := flagNamed(t, flags, "--cd")
	if cd.Kind != KindFolder || cd.Set != SetLaunch || !slices.Equal(cd.Aliases, []string{"-C"}) {
		t.Errorf("--cd: %+v", cd)
	}
	if f := flagNamed(t, flags, "--add-dir"); f.Kind != KindFolder || f.Set != "" {
		t.Errorf("--add-dir: %+v", f)
	}
	for _, name := range []string{"--dangerously-bypass-approvals-and-sandbox", "--approve-for-me", "--dangerously-bypass-hook-trust"} {
		if f := flagNamed(t, flags, name); f.Set != SetYolo {
			t.Errorf("%s: set %q", name, f.Set)
		}
	}
	if f := flagNamed(t, flags, "--image"); f.Placeholder != "FILE" || !f.Repeat || f.Kind != "" {
		t.Errorf("--image: %+v", f)
	}
	// -c's words hold one plain word in brackets, which is no list.
	if f := flagNamed(t, flags, "--config"); f.Choices != nil || f.Set != "" || f.Kind != "" {
		t.Errorf("--config: %+v", f)
	}
	for _, name := range []string{"--help", "--version"} {
		if f := flagNamed(t, flags, name); f.Set != SetExit {
			t.Errorf("%s: set %q", name, f.Set)
		}
	}
	// The sections it does not read: Commands and Arguments.
	for _, f := range flags {
		if !strings.HasPrefix(f.Name, "-") || f.Name == "--exec" {
			t.Errorf("read a row outside Options: %+v", f)
		}
	}
}

// Every Skips flag and value of the built-in Claude Code and Codex entries
// is set yolo, whatever its words: the list leaves to the Yolo switch what
// turns the agent's questions off.
func TestFlagsSkipsAreSetYolo(t *testing.T) {
	for _, id := range []string{"claude", "codex"} {
		ad, _ := Get(id)
		if ad.Flags == nil || len(ad.Flags.Skips) == 0 {
			t.Fatalf("%s has no Skips", id)
		}
		var help strings.Builder
		help.WriteString("Options:\n")
		for _, s := range ad.Flags.Skips {
			name, value, ok := strings.Cut(s, "=")
			if ok {
				fmt.Fprintf(&help, "  %s <x>  words (choices: \"other\", \"%s\")\n", name, value)
			} else {
				fmt.Fprintf(&help, "  %s  words\n", name)
			}
		}
		for _, f := range parse(t, builtIn(t, id), help.String()) {
			if len(f.Choices) == 0 {
				if f.Set != SetYolo {
					t.Errorf("%s %s: set %q", id, f.Name, f.Set)
				}
				continue
			}
			if f.Set != "" || f.Choices[0].Set != "" || f.Choices[1].Set != SetYolo {
				t.Errorf("%s %s: %+v", id, f.Name, f)
			}
		}
	}
}

// Each of the four forms of values, and what the fourth must not take.
func TestFlagsChoices(t *testing.T) {
	a := builtIn(t, "claude")
	one := func(row string, more ...string) Flag {
		t.Helper()
		flags := parse(t, a, "Options:\n"+row+"\n"+strings.Join(more, "\n")+"\n")
		return flags[0]
	}
	for _, c := range []struct {
		name string
		flag Flag
		want []string
	}{
		{"commander", one(`  --x <v>  words (choices: "a", "b.c", default: "a")`), []string{"a", "b.c"}},
		{"commander, preset", one(`  --x <v>  words (choices: "a", "b", preset: "b")`), []string{"a", "b"}},
		{"commander on a flag of no value", one(`  --x  words (choices: "a", "b")`), []string{"a", "b"}},
		{"clap inline", one(`  --x <V>`, `          words`, ``, `          [possible values: one, two]`), []string{"one", "two"}},
		{"clap block", one(`  --x <V>`, `          words`, `          Possible values:`, `          - one: first`, `          - two: second`), []string{"one", "two"}},
		{"words, commas", one(`  --x <v>  how much (low, mid, high)`), []string{"low", "mid", "high"}},
		{"words, bars", one(`  --x <v>  how much (low|mid|high)`), []string{"low", "mid", "high"}},
		{"words, or", one(`  --x <v>  which (one or two)`), []string{"one", "two"}},
		{"words, comma and or", one(`  --x <v>  which (one, two, or three)`), []string{"one", "two", "three"}},
		{"words, the last group", one(`  --x <v>  which (one, two) or rather (three, four)`), []string{"three", "four"}},
		{"not words: a phrase", one(`  --x <v>  models (comma-separated, in order)`), nil},
		{"not words: auto, or more", one(`  --x <v>  when (auto, or at least 100k tokens)`), nil},
		{"not words: e.g.", one(`  --x <v>  which (e.g., one, two)`), nil},
		{"not words: a default", one(`  --x <v>  which (default: [])`), nil},
		{"not words: one alone", one(`  --x <v>  which (one)`), nil},
		{"not words: capitals", one(`  --x <v>  which (One, Two)`), nil},
		{"not words: a flag of no value", one(`  --x  which (one, two)`, `  --y <v>  z`), nil},
		{"values of another shape dropped", one(`  --x <v>  w (choices: "a b", "-c", "ok", "")`), []string{"ok"}},
		{"repeats dropped", one(`  --x <v>  w (choices: "a", "a", "b")`), []string{"a", "b"}},
	} {
		if got := values(c.flag.Choices); !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if (len(c.want) > 0) != (c.flag.Kind == KindChoice) {
			t.Errorf("%s: kind %q", c.name, c.flag.Kind)
		}
	}
	// A list in words is read only in a help that says which rows take a
	// value: in one that says it of none, every value is unknown, and the
	// list is read.
	if f := one(`  --x  which (one, two)`, `  --y  z`); !slices.Equal(values(f.Choices), []string{"one", "two"}) || f.Value != ValueUnknown {
		t.Errorf("a help without placeholders: %+v", f)
	}
}

func TestFlagsExamples(t *testing.T) {
	a := builtIn(t, "claude")
	one := func(row string) Flag {
		t.Helper()
		return parse(t, a, "Options:\n"+row+"\n  --z <v>  z\n")[0]
	}
	for _, c := range []struct {
		row  string
		want []string
	}{
		{`  --x <v>  by alias (e.g. 'a', 'b', or 'c') or name`, []string{"a", "b", "c"}},
		{`  --x <v>  by alias (e.g., "a" or "b")`, []string{"a", "b"}},
		{`  --x <v>  one (e.g. 'a')`, []string{"a"}},
		{`  --x <v>  json (e.g. '{"k": "v"}')`, nil},
		{`  --x <v>  rules (e.g. "Bash,Edit")`, nil},
		{`  --x <v>  files (e.g., --x a.txt b.txt)`, nil},
		{`  --x <v>  mixed (e.g. 'a', b)`, nil},
		{`  --x  none taken (e.g. 'a', 'b')`, nil},
	} {
		if got := one(c.row).Examples; !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.row, got, c.want)
		}
	}
	nine := "  --x <v>  w (e.g. 'a1', 'a2', 'a3', 'a4', 'a5', 'a6', 'a7', 'a8', 'a9')"
	if got := one(nine).Examples; len(got) != maxFlagExamples || got[7] != "a8" {
		t.Errorf("nine examples kept %v", got)
	}
}

// R7's kinds by placeholder and name.
func TestFlagsKinds(t *testing.T) {
	flags := parse(t, builtIn(t, "codex"), "Options:\n"+strings.Join([]string{
		"  --a <DIR>  x",
		"  --b <folders...>  x",
		"  --c <path>  x",
		"  --plugin-dir <path>  x",
		"  --cache-dir  x",
		"  --d <Model>  x",
	}, "\n")+"\n")
	for name, kind := range map[string]string{"--a": KindFolder, "--b": KindFolder, "--c": "", "--plugin-dir": KindFolder, "--cache-dir": "", "--d": KindModel} {
		if f := flagNamed(t, flags, name); f.Kind != kind {
			t.Errorf("%s: kind %q, want %q", name, f.Kind, kind)
		}
	}
	// --model of a help that writes no placeholder: model by its name.
	if f := flagNamed(t, parse(t, catalog.Agent{ID: "x"}, "Options:\n  --model  x\n  --y  y\n"), "--model"); f.Kind != KindModel || f.Set != "" || f.Value != ValueUnknown {
		t.Errorf("--model, value unknown, no adapter: %+v", f)
	}
}

func TestFlagsBounds(t *testing.T) {
	a := builtIn(t, "claude")
	var help strings.Builder
	help.WriteString("Options:\n")
	for i := range maxFlags + 1 {
		fmt.Fprintf(&help, "  --f%d  words\n", i)
	}
	if flags := parse(t, a, help.String()); len(flags) != maxFlags || flags[maxFlags-1].Name != fmt.Sprintf("--f%d", maxFlags-1) {
		t.Errorf("%d rows kept", len(flags))
	}
	var choices []string
	for i := range maxFlagChoices + 1 {
		choices = append(choices, fmt.Sprintf(`"c%d"`, i))
	}
	f := parse(t, a, "Options:\n  --x <v>  w (choices: "+strings.Join(choices, ", ")+")\n")[0]
	if len(f.Choices) != maxFlagChoices || f.Choices[maxFlagChoices-1].Value != fmt.Sprintf("c%d", maxFlagChoices-1) {
		t.Errorf("%d choices kept", len(f.Choices))
	}
	// An about of 241 bytes, its last runes of two bytes: cut on a rune,
	// with an ellipsis, within the bound.
	about := strings.Repeat("a", 231) + strings.Repeat("é", 5)
	if len(about) != maxFlagAbout+1 {
		t.Fatalf("about of %d bytes", len(about))
	}
	f = parse(t, a, "Options:\n  --x  "+about+"\n")[0]
	if len(f.About) > maxFlagAbout || !utf8.ValidString(f.About) || !strings.HasSuffix(f.About, "…") || !strings.HasPrefix(f.About, strings.Repeat("a", 231)+"é") {
		t.Errorf("about cut to %d bytes: %q", len(f.About), f.About)
	}
	f = parse(t, a, "Options:\n  --x  "+about[:maxFlagAbout-1]+"\n")[0]
	if f.About != about[:maxFlagAbout-1] {
		t.Errorf("an about within the bound was cut: %q", f.About)
	}
	// A spelling of 64 bytes is a row; of 65 it is not.
	long := "--" + strings.Repeat("x", 62)
	if got := parse(t, a, "Options:\n  "+long+"  w\n  "+long+"y  w\n"); len(got) != 1 || got[0].Name != long {
		t.Errorf("spellings at the bound: %v", names(got))
	}
	// Four aliases kept, the fifth dropped.
	if got := parse(t, a, "Options:\n  -a, -b, -c, -d, -e, --x  w\n")[0]; got.Name != "--x" || !slices.Equal(got.Aliases, []string{"-a", "-b", "-c", "-d"}) {
		t.Errorf("aliases %+v", got)
	}
	// A placeholder of 60 bytes is read and kept to 40; one of 61 is no row.
	if got := parse(t, a, "Options:\n  --x <"+strings.Repeat("p", 58)+">  w\n")[0]; got.Placeholder != strings.Repeat("p", maxFlagHolder) {
		t.Errorf("placeholder %q", got.Placeholder)
	}
	if got := parse(t, a, "Options:\n  --x <"+strings.Repeat("p", 59)+">  w\n  --y  w\n"); len(got) != 1 || got[0].Name != "--y" {
		t.Errorf("a placeholder past the bound: %v", names(got))
	}
	// A help past MaxHelp is not read.
	big := "Options:\n  --x  w\n" + strings.Repeat("z", MaxHelp)
	if _, err := ParseFlags(a, big); !errors.Is(err, ErrHelpTooLong) {
		t.Errorf("a help of %d bytes: %v", len(big), err)
	}
}

// R0: carriage returns, escape sequences and invalid UTF-8 do not stop a
// row from being read; a line past its bound is cut.
func TestFlagsText(t *testing.T) {
	a := builtIn(t, "claude")
	help := "Options:\r\n  \x1b[1m--bold\x1b[0m <v>  \x1b[2mwords\x1b[0m\r\n  --bad  w\xffx\r\n  --long  " + strings.Repeat("w", 5000) + "\n"
	flags := parse(t, a, help)
	if !slices.Equal(names(flags), []string{"--bold", "--bad", "--long"}) {
		t.Fatalf("%v", names(flags))
	}
	if flags[0].About != "words" || flags[0].Value != ValueRequired {
		t.Errorf("--bold: %+v", flags[0])
	}
	if flags[1].About != "w�x" {
		t.Errorf("--bad: %q", flags[1].About)
	}
	if len(flags[2].About) > maxFlagAbout {
		t.Errorf("--long: %d bytes", len(flags[2].About))
	}
}

// R1 and R2: only the rows of an options section, and only lines exactly a
// row's form.
func TestFlagsSections(t *testing.T) {
	a := builtIn(t, "claude")
	help := strings.Join([]string{
		"  --before  a row-like line before any section",
		"Usage: helper [options]",
		"Arguments:",
		"  --arg  a row-like line of another section",
		"Options:",
		"  --one  read",
		"   --three-spaces  read",
		"         --nine-spaces  not a row: its words",
		"\t--tab  not a row",
		"  --one-space x  not a row",
		"  -xy  not a row",
		"  --two <v> extra  not a row",
		"  ---three  not a row",
		"Commands:",
		"  --after  a row-like line after the section",
		"FLAGS:",
		"  --again  read: a second section",
		"Usage of helper:",
		"  -go  not a row",
		"  -g  read",
	}, "\n")
	flags := parse(t, a, help)
	if !slices.Equal(names(flags), []string{"--one", "--three-spaces", "--again", "-g"}) {
		t.Fatalf("%v", names(flags))
	}
	for _, empty := range []string{"", "Usage: helper\n  --x  not in a section\n", "Options:\n\n  plain words\n", "Commands:\n  run  x\n"} {
		if _, err := ParseFlags(a, empty); !errors.Is(err, ErrNoFlags) {
			t.Errorf("%q: %v", empty, err)
		}
	}
}

func TestHelpArgs(t *testing.T) {
	claude := builtIn(t, "claude")
	if args, ok := HelpArgs(claude); !ok || !slices.Equal(args, []string{"--help"}) {
		t.Errorf("claude: %v %v", args, ok)
	}
	if args, ok := HelpArgs(builtIn(t, "codex")); !ok || !slices.Equal(args, []string{"--help"}) {
		t.Errorf("codex: %v %v", args, ok)
	}
	// An entry whose start arguments an admin edited gives no word of them.
	edited := claude
	edited.Session = &catalog.SessionRecipe{StartArgs: []string{"sh", "-c", "touch x", "--session-id", catalog.IDArg}}
	edited.Command = []string{"claude", "extra"}
	if args, ok := HelpArgs(edited); !ok || !slices.Equal(args, []string{"--help"}) {
		t.Errorf("edited: %v %v", args, ok)
	}
	for _, id := range []string{"goose", "agy", "aider", ""} {
		a := claude
		a.Adapter = id
		if args, ok := HelpArgs(a); ok || args != nil {
			t.Errorf("adapter %q has no recipe: %v %v", id, args, ok)
		}
	}
	// A recipe's own sub-command comes first (Goose's session, in a later
	// row), and HelpArgs hands out a copy.
	r := &FlagsRecipe{Sub: []string{"session"}, Args: []string{"--help"}}
	args := helpArgsOf(r)
	if !slices.Equal(args, []string{"session", "--help"}) {
		t.Errorf("sub: %v", args)
	}
	args[0] = "changed"
	if r.Sub[0] != "session" {
		t.Error("HelpArgs handed out the recipe's own slice")
	}
	// The registry hands out copies of the recipes too.
	ad, _ := Get("claude")
	ad.Flags.Taken[0] = "--changed"
	if again, _ := Get("claude"); again.Flags.Taken[0] != "--settings" {
		t.Error("Get handed out the registry's own recipe")
	}
}
