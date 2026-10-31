package opencore

import (
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

// TestGoModRule: go.mod's require, replace (either side) and tool
// directives, on a line or in a block, quoted or not; this module itself,
// another organization and a comment are not problems.
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
	problems, modulePath, err := checkGoMod([]byte(mod))
	if err != nil {
		t.Fatal(err)
	}
	if modulePath != Module {
		t.Errorf("module = %q, want %q", modulePath, Module)
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
		"go.mod:13: go-mod: replace github.com/rocksolidlabs/boxcar",
		"go.mod:14: go-mod: replace github.com/RockSolidLabs/websocket",
		"go.mod:16: go-mod: tool github.com/rocksolidlabs/conductor-cloud/cmd/gen",
		"go.mod:17: go-mod: require github.com/rocksolidlabs/inline",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
