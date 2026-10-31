// Package opencore checks the rules that keep this repository open core
// (CONTRIBUTING.md, The open-core rules; docs/plans/ext.md, The lint) on
// the module's source, as a test CI fails on: no module of the
// rocksolidlabs organization but this one is imported or required, no
// build constraint names a feature tag, and ext/ stays on the standard
// library so that another module can take its types without this
// module's internals.
//
// The rules read each file's imports and build constraints only, with
// go/parser, whatever the file's tags: a file a plain build leaves out is
// read as well as one it compiles, which no build of the module and no
// text search would do. The package imports the standard library only.
package opencore

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Module is this module's path: an import of it or of a package under it
// is this repository's own.
const Module = "github.com/rocksolidlabs/conductor"

// The rules, each named in the problems it reports.
const (
	// RulePrivateModule: a file imports a package of a module of the
	// rocksolidlabs organization other than Module.
	RulePrivateModule = "private-module"
	// RuleGoMod: go.mod requires, replaces or names as a tool such a
	// module.
	RuleGoMod = "go-mod"
	// RuleFeatureTag: a build constraint names a tag that is not a
	// platform (a GOOS or GOARCH the toolchain knows, unix), a compiler or
	// cgo, a Go version or one of the test tiers.
	RuleFeatureTag = "feature-tag"
	// RuleExtStd: a file under ext/ imports anything but the standard
	// library and ext's own packages.
	RuleExtStd = "ext-std"
)

// Problem is one breach of a rule: the rule, the file (a slash path
// relative to the root Check was given) and the line.
type Problem struct {
	Rule   string
	File   string
	Line   int
	Detail string
}

// String formats the problem as the go tools do: file:line: rule: detail.
func (p Problem) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", p.File, p.Line, p.Rule, p.Detail)
}

// The platform tags: every GOOS and GOARCH `go tool dist list` prints for
// the module's toolchain (TestPlatformTagsMatchTheToolchain holds the two
// lists to it), and unix, which the go command sets for every Unix GOOS.
var (
	knownOS = []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios",
		"js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1",
		"windows",
	}
	knownArch = []string{
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64",
		"mips64le", "mipsle", "ppc64", "ppc64le", "riscv64", "s390x", "wasm",
	}
	// otherTags are the go command's own tags besides the platforms and the
	// Go versions: unix, the cgo switch and the two compilers.
	otherTags = []string{"unix", "cgo", "gc", "gccgo"}
	// testTiers are the tags of the test tiers the Makefile runs on their
	// own (make test-recipes, test-pebble, test-network).
	testTiers = []string{"recipes", "pebble", "network"}
	goVersion = regexp.MustCompile(`^go[1-9][0-9]*\.(0|[1-9][0-9]*)$`)
)

// allowedTag reports whether a build constraint may name tag.
func allowedTag(tag string) bool {
	return slices.Contains(knownOS, tag) || slices.Contains(knownArch, tag) ||
		slices.Contains(otherTags, tag) || slices.Contains(testTiers, tag) ||
		goVersion.MatchString(tag)
}

// skipDirs are the directories Check never enters, wherever they are: the
// go command's testdata (the fixtures of this package's own test among
// them) and the package managers' trees.
var skipDirs = []string{"testdata", "node_modules", "vendor"}

// Check reads go.mod and every .go file of the module rooted at root and
// returns the problems it finds, ordered by file, line and rule. It skips
// the directories the go command and the package managers own (testdata,
// node_modules, vendor), the generated web bundle (internal/web/dist),
// every directory whose name begins with "." or "_", and every directory
// with a go.mod of its own (another module: a git worktree under .claude/
// is both). root's go.mod must name Module. An error is a file Check
// could not read or parse, never a problem.
func Check(root string) ([]Problem, error) {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	problems, modulePath, err := checkGoMod(mod)
	if err != nil {
		return nil, err
	}
	if modulePath != Module {
		return nil, fmt.Errorf("opencore: %s: go.mod names module %q, not %q", root, modulePath, Module)
	}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if slices.Contains(skipDirs, name) || strings.HasPrefix(name, ".") ||
				strings.HasPrefix(name, "_") || rel == "internal/web/dist" {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		found, err := checkFile(fset, p, rel)
		problems = append(problems, found...)
		return err
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Rule, b.Rule)
	})
	return problems, nil
}

// checkFile applies the file rules to one .go file, rel its slash path
// under the root.
func checkFile(fset *token.FileSet, p, rel string) ([]Problem, error) {
	f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("opencore: %w", err)
	}
	var problems []Problem
	// The build constraints are the //go:build and // +build lines before
	// the package clause; one anywhere else is no constraint (go vet's
	// buildtag check reports it).
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			line := fset.Position(c.Pos()).Line
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				problems = append(problems, Problem{RuleFeatureTag, rel, line,
					fmt.Sprintf("build constraint %q does not parse: %v", c.Text, err)})
				continue
			}
			for _, tag := range tags(expr) {
				if !allowedTag(tag) {
					problems = append(problems, Problem{RuleFeatureTag, rel, line,
						fmt.Sprintf("build constraint names %q, which is no platform, Go version or test tier (%s)",
							tag, strings.Join(testTiers, ", "))})
				}
			}
		}
	}
	inExt := rel == "ext" || strings.HasPrefix(rel, "ext/")
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("opencore: %s: import %s: %w", fset.Position(spec.Pos()), spec.Path.Value, err)
		}
		line := fset.Position(spec.Pos()).Line
		if privateModule(imp) {
			problems = append(problems, Problem{RulePrivateModule, rel, line,
				fmt.Sprintf("imports %s, of a module of the rocksolidlabs organization other than %s", imp, Module)})
		}
		if inExt && !standard(imp) && imp != Module+"/ext" && !strings.HasPrefix(imp, Module+"/ext/") {
			problems = append(problems, Problem{RuleExtStd, rel, line,
				fmt.Sprintf("imports %s: ext/ takes only the standard library and ext's own packages", imp)})
		}
	}
	return problems, nil
}

// tags lists the tags a build constraint names, each once, in order.
func tags(x constraint.Expr) []string {
	var out []string
	var walk func(constraint.Expr)
	walk = func(x constraint.Expr) {
		switch x := x.(type) {
		case *constraint.TagExpr:
			if !slices.Contains(out, x.Tag) {
				out = append(out, x.Tag)
			}
		case *constraint.NotExpr:
			walk(x.X)
		case *constraint.AndExpr:
			walk(x.X)
			walk(x.Y)
		case *constraint.OrExpr:
			walk(x.X)
			walk(x.Y)
		}
	}
	walk(x)
	return out
}

// privateModule reports whether a module or package path is under
// github.com/rocksolidlabs/ but not Module, compared by path elements:
// github.com/rocksolidlabs/conductor-premium is another module, and so is
// github.com/RockSolidLabs/x, which GitHub serves from the same
// organization.
func privateModule(p string) bool {
	elems := strings.Split(p, "/")
	if len(elems) < 2 || !strings.EqualFold(elems[0], "github.com") || !strings.EqualFold(elems[1], "rocksolidlabs") {
		return false
	}
	own := strings.Split(Module, "/")
	return len(elems) < len(own) || !slices.Equal(elems[:len(own)], own)
}

// standard reports whether an import path is the standard library's: its
// first element has no dot, as the go command decides it. "C", cgo's
// pseudo-package, is not.
func standard(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return p != "C" && first != "" && !strings.Contains(first, ".")
}

// checkGoMod applies the go-mod rule to go.mod's text and returns its
// problems and the module path it names; an error is a line too long to
// read. It reads the directives the go
// command writes, one per line or in a parenthesised block: require,
// replace (either side of the arrow) and tool.
func checkGoMod(data []byte) ([]Problem, string, error) {
	var problems []Problem
	var modulePath, block string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		text := sc.Text()
		if i := strings.Index(text, "//"); i >= 0 {
			text = text[:i]
		}
		text = strings.NewReplacer("(", " ( ", ")", " ) ", "=>", " => ").Replace(text)
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		verb := block
		if block == "" {
			verb, fields = fields[0], fields[1:]
			if len(fields) > 0 && fields[0] == "(" {
				block, fields = verb, fields[1:]
			}
		}
		if len(fields) > 0 && fields[len(fields)-1] == ")" {
			block, fields = "", fields[:len(fields)-1]
		}
		if len(fields) == 0 {
			continue
		}
		var paths []string
		switch verb {
		case "module":
			modulePath = unquote(fields[0])
		case "require", "tool":
			paths = append(paths, fields[0])
		case "replace":
			paths = append(paths, fields[0])
			if i := slices.Index(fields, "=>"); i >= 0 && i+1 < len(fields) {
				paths = append(paths, fields[i+1])
			}
		}
		for _, p := range paths {
			if p = unquote(p); privateModule(p) {
				problems = append(problems, Problem{RuleGoMod, "go.mod", n,
					fmt.Sprintf("%s %s, a module of the rocksolidlabs organization other than %s", verb, p, Module)})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, "", fmt.Errorf("opencore: go.mod: %w", err)
	}
	return problems, modulePath, nil
}

// unquote takes the quotes off a go.mod token written as a Go string, and
// leaves any other token as it is.
func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
