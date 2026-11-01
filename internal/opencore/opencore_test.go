package opencore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestModuleKeepsTheRules is the lint itself: make lint runs it after go
// vet, and CI's race suite runs it again. The module must have no
// problem.
func TestModuleKeepsTheRules(t *testing.T) {
	problems, err := Check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s", p)
	}
	if len(problems) > 0 {
		t.Log("the open-core rules: CONTRIBUTING.md, The open-core rules; docs/plans/ext.md, The lint")
	}
}

// TestEachRuleCatchesItsPlant runs Check on each fixture under testdata/,
// a module with one planted problem beside what the rule lets through,
// and wants exactly that problem.
func TestEachRuleCatchesItsPlant(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    Problem
		detail  string
	}{
		{"private-module", Problem{Rule: RulePrivateModule, File: "plant.go", Line: 9}, "github.com/rocksolidlabs/conductor-premium/register"},
		{"go-mod", Problem{Rule: RuleGoMod, File: "go.mod", Line: 9}, "require github.com/rocksolidlabs/conductor-cloud"},
		{"feature-tag", Problem{Rule: RuleFeatureTag, File: "premium.go", Line: 1}, `"premium"`},
		{"ext-std", Problem{Rule: RuleExtStd, File: "ext/plant.go", Line: 9}, "github.com/rocksolidlabs/conductor/internal/session"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			problems, err := Check(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if len(problems) != 1 {
				t.Fatalf("problems = %q, want exactly one, %s at %s:%d", problems, tc.want.Rule, tc.want.File, tc.want.Line)
			}
			got := problems[0]
			if got.Rule != tc.want.Rule || got.File != tc.want.File || got.Line != tc.want.Line {
				t.Errorf("problem = %s, want %s at %s:%d", got, tc.want.Rule, tc.want.File, tc.want.Line)
			}
			if !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("detail = %q, want it to name %s", got.Detail, tc.detail)
			}
		})
	}
}

// TestPlatformTagsMatchTheToolchain holds the known GOOS and GOARCH lists
// to the ports the toolchain running the test prints: a port it adds is a
// platform tag the lint would refuse, one it drops a tag nothing builds.
func TestPlatformTagsMatchTheToolchain(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command, which go test puts on PATH: %v", err)
	}
	out, err := exec.CommandContext(t.Context(), goTool, "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	var oses, arches []string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		goos, goarch, ok := strings.Cut(line, "/")
		if !ok {
			t.Fatalf("go tool dist list printed %q, not GOOS/GOARCH", line)
		}
		if !slices.Contains(oses, goos) {
			oses = append(oses, goos)
		}
		if !slices.Contains(arches, goarch) {
			arches = append(arches, goarch)
		}
	}
	for _, c := range []struct {
		name        string
		known, tool []string
	}{{"GOOS", knownOS, oses}, {"GOARCH", knownArch, arches}} {
		known, tool := slices.Sorted(slices.Values(c.known)), slices.Sorted(slices.Values(c.tool))
		if !slices.Equal(known, tool) {
			t.Errorf("known %s = %q, go tool dist list prints %q: update the list in opencore.go", c.name, known, tool)
		}
	}
}

// TestFeatureTagRule: a platform, a compiler, cgo, a Go version and a test
// tier are allowed; every other tag is refused.
func TestFeatureTagRule(t *testing.T) {
	for _, tag := range []string{"linux", "darwin", "windows", "wasip1", "amd64", "arm64", "wasm", "unix", "cgo", "gc", "gccgo", "go1.26", "go1.0", "go2.10", "recipes", "pebble", "network"} {
		if !allowedTag(tag) {
			t.Errorf("tag %q refused, want it allowed", tag)
		}
	}
	for _, tag := range []string{"premium", "cloud", "enterprise", "ignore", "Linux", "go1", "go1.026", "go01.2", "goexperiment.jsonv2", "boringcrypto", "integration", ""} {
		if allowedTag(tag) {
			t.Errorf("tag %q allowed, want it refused", tag)
		}
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module " + Module + "\n",
		"plus.go":   "// +build cloud\n\npackage x\n",
		"broken.go": "//go:build linux &&\n\npackage x\n",
		"twice.go":  "//go:build premium && (premium || linux)\n\npackage x\n",
		"late.go":   "package x\n\n//go:build premium\n",
	})
	got := rules(t, dir)
	want := []string{"broken.go:1: feature-tag", "plus.go:1: feature-tag", "twice.go:1: feature-tag"}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q (a +build line, a constraint that does not parse, a tag once per line; none after the package clause)", got, want)
	}
}

// TestGoModRule: go.mod's require, replace (either side, and a directory
// on the right whatever the module) and tool directives, on a line or in a
// block, quoted or not; this module itself (replaced by a directory too,
// which the go command ignores), another organization and a comment are
// not problems.
func TestGoModRule(t *testing.T) {
	mod := `module github.com/rocksolidlabs/conductor // the module

go 1.26.0

require github.com/rocksolidlabs/conductor-premium v1.0.0
require (
	github.com/coder/websocket v1.8.15 // github.com/rocksolidlabs/in-a-comment
	"github.com/rocksolidlabs/quoted" v0.1.0
	github.com/rocksolidlabsx/other v0.1.0
)
replace github.com/rocksolidlabs/conductor => ../conductor
replace (
	github.com/rocksolidlabs/boxcar v0.1.0 => ./boxcar
	github.com/coder/websocket=>github.com/RockSolidLabs/websocket v1.8.16
)
tool github.com/rocksolidlabs/conductor-cloud/cmd/gen
require(github.com/rocksolidlabs/inline v0.1.0)
`
	problems, file, err := checkGoMod("go.mod", []byte(mod))
	if err != nil {
		t.Fatal(err)
	}
	if file.module != Module {
		t.Errorf("module = %q, want %q", file.module, Module)
	}
	var got []string
	for _, p := range problems {
		if p.Rule != RuleGoMod || p.File != "go.mod" {
			t.Errorf("problem %s, want rule %s in go.mod", p, RuleGoMod)
		}
		got = append(got, p.String()[:strings.Index(p.String(), ",")])
	}
	want := []string{
		"go.mod:5: go-mod: require github.com/rocksolidlabs/conductor-premium",
		"go.mod:8: go-mod: require github.com/rocksolidlabs/quoted",
		"go.mod:13: go-mod: replace github.com/rocksolidlabs/boxcar with the directory ./boxcar",
		"go.mod:13: go-mod: replace github.com/rocksolidlabs/boxcar",
		"go.mod:14: go-mod: replace github.com/RockSolidLabs/websocket",
		"go.mod:16: go-mod: tool github.com/rocksolidlabs/conductor-cloud/cmd/gen",
		"go.mod:17: go-mod: require github.com/rocksolidlabs/inline",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGoWorkRule: a go.work beside go.mod is read with go.mod's rule: a
// use of any directory but the root's and a replace (either side, or a
// directory on the right) are problems in go.work; without a go.work
// nothing is reported for it.
func TestGoWorkRule(t *testing.T) {
	work := `go 1.26.0

use .
use ./
use (
	.
	../conductor-premium
)
replace github.com/coder/websocket => github.com/rocksolidlabs/websocket v1.8.16
replace github.com/coder/websocket v1.8.15 => /src/websocket
`
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":  "module " + Module + "\n",
		"go.work": work,
		"x.go":    "package x\n",
	})
	problems, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	var got []string
	for _, p := range problems {
		got = append(got, p.String()[:strings.Index(p.String(), ",")])
	}
	want := []string{
		"go.work:7: go-mod: use ../conductor-premium",
		"go.work:9: go-mod: replace github.com/rocksolidlabs/websocket",
		"go.work:10: go-mod: replace github.com/coder/websocket with the directory /src/websocket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGoModQuotedTokens: a path written as a Go string is one token
// whatever it holds, in go.mod and in go.work alike: a "//" inside it is
// not a comment, and a space or a parenthesis does not split it, so a
// quoted directory on the right of a replace is reported like a bare one.
// A comment after a string is still a comment.
func TestGoModQuotedTokens(t *testing.T) {
	directives := "replace example.com/helper => \"./testdata//helper\"\n" +
		"replace example.com/spaced => \"./my dir/helper\" // github.com/rocksolidlabs/in-a-comment\n" +
		"replace (\n\texample.com/paren => \"./a(b)\"\n)\n" +
		"replace \"example.com/q\" => \"github.com/rocksolidlabs//x\" v1.0.0\n"
	want := []string{
		"%s:%d: go-mod: replace example.com/helper with the directory ./testdata//helper",
		"%s:%d: go-mod: replace example.com/spaced with the directory ./my dir/helper",
		"%s:%d: go-mod: replace example.com/paren with the directory ./a(b)",
		"%s:%d: go-mod: replace github.com/rocksolidlabs//x",
	}
	lines := []int{1, 2, 4, 6}
	for _, name := range []string{"go.mod", "go.work"} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"go.mod": "module " + Module + "\n", "x.go": "package x\n"}
			head := "go 1.26.0\nuse \"./\" // the root\n"
			if name == "go.mod" {
				head = "module \"" + Module + "\" // quoted\ngo 1.26.0\n"
			}
			files[name] = head + directives
			dir := t.TempDir()
			writeFiles(t, dir, files)
			problems, err := Check(dir)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			var got, wanted []string
			for _, p := range problems {
				got = append(got, p.String()[:strings.Index(p.String(), ",")])
			}
			for i, w := range want {
				wanted = append(wanted, fmt.Sprintf(w, name, lines[i]+2))
			}
			if !slices.Equal(got, wanted) {
				t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wanted, "\n"))
			}
		})
	}
}

// TestGoModDirectoryPaths: the right of a replace is a directory by the go
// command's rule (modfile.IsDirectoryPath), which reads every platform's
// syntax on every platform: a drive prefix (on Linux the go command joins
// C:/../testdata/helper to the root and builds the module there), a rooted
// backslash path and the dot forms with either slash are directories in
// go.mod and go.work alike, wherever the lint runs. A module path with a
// colon later in it is not.
func TestGoModDirectoryPaths(t *testing.T) {
	dirs := []string{
		"C:/../testdata/helper", `c:\testdata\helper`, "Z:", `\testdata\helper`, `\\server\share`,
		"/src/helper", ".", "..", "./helper", "../helper", `.\helper`, `..\helper`,
	}
	var directives string
	var want []string
	for i, d := range dirs {
		directives += fmt.Sprintf("replace example.com/h%d => %q\n", i, d)
		want = append(want, fmt.Sprintf("replace example.com/h%d with the directory %s", i, d))
	}
	directives += "replace example.com/m => example.com/x:y v1.0.0\nreplace example.com/n => 1:/x v1.0.0\n"
	for _, name := range []string{"go.mod", "go.work"} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"go.mod": "module " + Module + "\n", "x.go": "package x\n"}
			head := "go 1.26.0\n"
			if name == "go.mod" {
				head = "module " + Module + "\n" + head
			}
			files[name] = head + directives
			dir := t.TempDir()
			writeFiles(t, dir, files)
			problems, err := Check(dir)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			var got []string
			for _, p := range problems {
				if p.Rule != RuleGoMod || p.File != name {
					t.Errorf("problem %s, want rule %s in %s", p, RuleGoMod, name)
				}
				got = append(got, p.Detail[:strings.Index(p.Detail, ",")])
			}
			if !slices.Equal(got, want) {
				t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
	for _, p := range []string{"example.com/x", "1:/x", "C", "c/x", "_:/x"} {
		if localPath(p) {
			t.Errorf("localPath(%q) = true, want false (a module path)", p)
		}
	}
}

// TestExtStdKnowsADotlessModule: an import under ext/ with no dot in its
// first element is the standard library's only when no module go.mod or
// go.work requires or replaces holds it; helper, replaced by another
// module, and tools/gen, required, are refused, and net/http is not.
func TestExtStdKnowsADotlessModule(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":   "module " + Module + "\n\nrequire (\n\thelper v0.0.0\n\ttools v0.0.0\n)\n\nreplace helper => example.com/helper v1.0.0\n",
		"go.work":  "go 1.26.0\n\nuse .\n\nreplace tools => example.com/tools v1.0.0\n",
		"ext/x.go": "package ext\n\nimport (\n\t\"net/http\"\n\t\"helper\"\n\t\"tools/gen\"\n\t\"helpers\"\n)\n",
	})
	got := rules(t, dir)
	want := []string{"ext/x.go:5: ext-std", "ext/x.go:6: ext-std"}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q (helper and tools/gen are modules; net/http and helpers are in none)", got, want)
	}
}

// TestCheckReadsWhatTheBuildCompiles: the go command compiles a package of
// the module that a file imports from a directory the walk skips, a .go
// file that is a symlink, and a package reached through a symlinked
// directory. A linked .go file is read where its link stands; an import
// into a skipped directory, another module's among them, is reported.
func TestCheckReadsWhatTheBuildCompiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                "module " + Module + "\n",
		"ext/local/local.go":    "package local\n\nimport (\n\t_ \"" + Module + "/ext/_impl\"\n\t_ \"" + Module + "/ext/testdata/t\"\n\t_ \"" + Module + "/ext\"\n)\n",
		"ext/ext.go":            "package ext\n",
		"ext/_impl/impl.go":     "package impl\n\nimport _ \"" + Module + "/internal/session\"\n",
		"ext/testdata/t/t.go":   "//go:build premium\n\npackage t\n",
		"internal/api/api.go":   "package api\n\nimport (\n\t_ \"" + Module + "/internal/linked\"\n\t_ \"" + Module + "/nested/x\"\n\t_ \"" + Module + "/internal/session\"\n)\n",
		"internal/session/s.go": "package session\n",
		"nested/go.mod":         "module " + Module + "/nested\n",
		"nested/x/x.go":         "//go:build premium\n\npackage x\n",
		"testdata/gate.go":      "//go:build premium\n\npackage api\n",
		"testdata/plant.go":     "package ext\n\nimport _ \"" + Module + "/internal/session\"\n",
		"testdata/pkg/p.go":     "//go:build premium\n\npackage linked\n",
		"testdata/notes.txt":    "not Go\n",
	})
	for link, target := range map[string]string{
		"internal/api/gate.go": "../../testdata/gate.go",
		"ext/plant.go":         "../testdata/plant.go",
		"internal/linked":      "../testdata/pkg",
		"internal/api/notes":   "../../testdata/notes.txt",
		"internal/api/gone":    "../../testdata/gone",
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Skipf("symlinks: %v", err)
		}
	}
	got := rules(t, dir)
	want := []string{
		"ext/local/local.go:4: unread-package",
		"ext/local/local.go:5: unread-package",
		"ext/plant.go:3: ext-std",
		"internal/api/api.go:4: unread-package",
		"internal/api/api.go:5: unread-package",
		"internal/api/gate.go:1: feature-tag",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q", got, want)
	}

	// A linked .go file whose target is gone cannot be read: an error, as
	// a file that does not parse is.
	if err := os.Symlink("../../testdata/gone.go", filepath.Join(dir, "internal", "api", "gone.go")); err != nil {
		t.Fatal(err)
	}
	if problems, err := Check(dir); err == nil {
		t.Errorf("Check with a dangling .go link = %q, nil; want an error", problems)
	}
}

// TestCheckSkipsWhatIsNotTheModule plants a problem in every directory
// Check skips and one where it reads, and wants only the last: the
// skipped ones are gitignored or another module's, so the fixture is
// built here rather than committed.
func TestCheckSkipsWhatIsNotTheModule(t *testing.T) {
	plant := "package x\n\nimport _ \"github.com/rocksolidlabs/conductor-premium/register\"\n"
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                           "module " + Module + "\n",
		"internal/seen/plant.go":           plant,
		"internal/seen/testdata/plant.go":  plant,
		"web/node_modules/x/plant.go":      plant,
		"vendor/x/plant.go":                plant,
		"internal/web/dist/plant.go":       plant,
		".claude/worktrees/x/go.mod":       "module " + Module + "\n",
		".claude/worktrees/x/plant.go":     plant,
		"_old/plant.go":                    plant,
		".cache/x/plant.go":                plant,
		"nested/go.mod":                    "module example.com/nested\n",
		"nested/plant.go":                  plant,
		"web/app/plant.ts":                 plant,
		"internal/web/distant/x/x_test.go": plant,
	})
	got := rules(t, dir)
	want := []string{"internal/seen/plant.go:3: private-module", "internal/web/distant/x/x_test.go:3: private-module"}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q", got, want)
	}
}

// TestCheckRefuses: a root without go.mod, a go.mod of another module and a
// .go file that does not parse are errors, not problems.
func TestCheckRefuses(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no go.mod":      {"x.go": "package x\n"},
		"another module": {"go.mod": "module example.com/fork\n", "x.go": "package x\n"},
		"a broken file":  {"go.mod": "module " + Module + "\n", "x.go": "package x\n\nimport (\n"},
		"an open string": {"go.mod": "module " + Module + "\n\nreplace example.com/h => \"./testdata//h\n", "x.go": "package x\n"},
		"a raw string":   {"go.mod": "module " + Module + "\n\nreplace example.com/h => `./testdata//h`\n", "x.go": "package x\n"},
		"a bad string":   {"go.mod": "module " + Module + "\n", "go.work": "go 1.26.0\n\nreplace example.com/h => \"./h\\q\"\n", "x.go": "package x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files)
			if problems, err := Check(dir); err == nil {
				t.Errorf("Check = %q, nil; want an error", problems)
			}
		})
	}
}

// rules runs Check on dir and answers each problem as file:line: rule.
func rules(t *testing.T, dir string) []string {
	t.Helper()
	problems, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	var out []string
	for _, p := range problems {
		out = append(out, p.File+":"+strconv.Itoa(p.Line)+": "+p.Rule)
	}
	return out
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
