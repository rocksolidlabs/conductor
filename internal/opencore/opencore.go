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
// text search would do. What the build would compile from a place the walk
// does not read is reported instead: an import of this module's package
// from a skipped directory, and another module's tree brought in by a
// local replace or a workspace. The package imports the standard library
// only.
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
	// RuleGoMod: go.mod or a go.work at the root requires, replaces or
	// names as a tool such a module, replaces a module with a local
	// directory, or (go.work) uses a directory other than the root: the
	// last two build another module's tree, which the walk does not read.
	RuleGoMod = "go-mod"
	// RuleFeatureTag: a build constraint names a tag that is not a
	// platform (a GOOS or GOARCH the toolchain knows, unix), a compiler or
	// cgo, a Go version or one of the test tiers.
	RuleFeatureTag = "feature-tag"
	// RuleExtStd: a file under ext/ imports anything but the standard
	// library and ext's own packages.
	RuleExtStd = "ext-std"
	// RuleUnreadPackage: a file imports a package of this module from a
	// directory the walk does not read (testdata, node_modules, vendor,
	// internal/web/dist, a name beginning with "." or "_", another
	// module's directory, a symlinked directory), which the build compiles
	// all the same.
	RuleUnreadPackage = "unread-package"
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

// ownImport is a file's import of a package of this module, kept until
// the walk has seen every directory it skips.
type ownImport struct {
	file string
	line int
	path string
}

// Check reads go.mod, a go.work beside it if there is one, and every .go
// file of the module rooted at root, a symlinked one as if it stood where
// its link does, and returns the problems it finds, ordered by file, line
// and rule. It does not read the directories the go command and the
// package managers own (testdata, node_modules, vendor), the generated web
// bundle (internal/web/dist), any directory whose name begins with "." or
// "_", any directory with a go.mod of its own (another module: a git
// worktree under .claude/ is both) or a symlinked directory; an import of
// this module's package from one of them is a problem. root's go.mod must
// name Module. An error is a file Check could not read or parse, never a
// problem.
func Check(root string) ([]Problem, error) {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	problems, gomod, err := checkGoMod("go.mod", mod)
	if err != nil {
		return nil, err
	}
	if gomod.module != Module {
		return nil, fmt.Errorf("opencore: %s: go.mod names module %q, not %q", root, gomod.module, Module)
	}
	modules := gomod.modules
	switch work, err := os.ReadFile(filepath.Join(root, "go.work")); {
	case err == nil:
		found, gowork, err := checkGoMod("go.work", work)
		if err != nil {
			return nil, err
		}
		problems = append(problems, found...)
		modules = append(modules, gowork.modules...)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	fset := token.NewFileSet()
	var unread []string
	var own []ownImport
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
				unread = append(unread, rel)
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				unread = append(unread, rel)
				return filepath.SkipDir
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		isGo := strings.HasSuffix(d.Name(), ".go")
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			// The go command follows a link: a linked .go file is compiled
			// as one of its directory's, and an import reaches a package
			// through a linked directory, which WalkDir does not enter.
			st, err := os.Stat(p)
			if err != nil {
				if isGo {
					return fmt.Errorf("opencore: %s: %w", rel, err)
				}
				return nil
			}
			if st.IsDir() {
				unread = append(unread, rel)
				return nil
			}
			if !st.Mode().IsRegular() || !isGo {
				return nil
			}
		case !d.Type().IsRegular() || !isGo:
			return nil
		}
		found, imports, err := checkFile(fset, p, rel, modules)
		problems = append(problems, found...)
		own = append(own, imports...)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, imp := range own {
		sub := strings.TrimPrefix(strings.TrimPrefix(imp.path, Module), "/")
		for _, dir := range unread {
			if sub == dir || strings.HasPrefix(sub, dir+"/") {
				problems = append(problems, Problem{RuleUnreadPackage, imp.file, imp.line,
					fmt.Sprintf("imports %s, from %s, which the lint does not read and the build compiles", imp.path, dir)})
				break
			}
		}
	}
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		if c := strings.Compare(a.Rule, b.Rule); c != 0 {
			return c
		}
		return strings.Compare(a.Detail, b.Detail)
	})
	return problems, nil
}

// checkFile applies the file rules to one .go file, rel its slash path
// under the root and modules the module paths go.mod and go.work name; it
// returns the file's imports of this module's packages too.
func checkFile(fset *token.FileSet, p, rel string, modules []string) ([]Problem, []ownImport, error) {
	f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("opencore: %w", err)
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
	var own []ownImport
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("opencore: %s: import %s: %w", fset.Position(spec.Pos()), spec.Path.Value, err)
		}
		line := fset.Position(spec.Pos()).Line
		if imp == Module || strings.HasPrefix(imp, Module+"/") {
			own = append(own, ownImport{rel, line, imp})
		}
		if privateModule(imp) {
			problems = append(problems, Problem{RulePrivateModule, rel, line,
				fmt.Sprintf("imports %s, of a module of the rocksolidlabs organization other than %s", imp, Module)})
		}
		if inExt && !standard(imp, modules) && imp != Module+"/ext" && !strings.HasPrefix(imp, Module+"/ext/") {
			problems = append(problems, Problem{RuleExtStd, rel, line,
				fmt.Sprintf("imports %s: ext/ takes only the standard library and ext's own packages", imp)})
		}
	}
	return problems, own, nil
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
// first element has no dot, as the go command decides it, and it is in no
// module that go.mod or go.work names. A module whose path has no dot
// cannot be downloaded, so the main module or the workspace must replace
// it, and both name it: modules lists every path they require or replace.
// "C", cgo's pseudo-package, is not.
func standard(p string, modules []string) bool {
	first, _, _ := strings.Cut(p, "/")
	if p == "C" || first == "" || strings.Contains(first, ".") {
		return false
	}
	for _, m := range modules {
		if p == m || strings.HasPrefix(p, m+"/") {
			return false
		}
	}
	return true
}

// modFile is what checkGoMod reads from a go.mod or go.work besides its
// problems: the module path go.mod names, and every module path it
// requires or replaces.
type modFile struct {
	module  string
	modules []string
}

// checkGoMod applies the go-mod rule to the text of go.mod or go.work
// (name) and returns its problems and what it names; an error is a line
// too long to read or a string the go command would refuse (modTokens
// splits each line). It reads the directives the go command writes, one
// per line or in a parenthesised block: require, replace (either side of
// the arrow; a directory on the right, unless it replaces Module itself),
// tool and use (a directory other than the root's).
func checkGoMod(name string, data []byte) ([]Problem, modFile, error) {
	var problems []Problem
	var file modFile
	var block string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		fields, err := modTokens(sc.Text())
		if err != nil {
			return nil, modFile{}, fmt.Errorf("opencore: %s:%d: %w", name, n, err)
		}
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
		report := func(detail string) {
			problems = append(problems, Problem{RuleGoMod, name, n, detail})
		}
		var paths []string
		switch verb {
		case "module":
			file.module = unquote(fields[0])
		case "require", "tool":
			paths = append(paths, unquote(fields[0]))
			if verb == "require" {
				file.modules = append(file.modules, unquote(fields[0]))
			}
		case "replace":
			old := unquote(fields[0])
			paths = append(paths, old)
			file.modules = append(file.modules, old)
			if i := slices.Index(fields, "=>"); i >= 0 && i+1 < len(fields) {
				// The main module is never replaced (the go command ignores
				// it), so only another module's directory is reported.
				if target := unquote(fields[i+1]); localPath(target) {
					if old == Module {
						continue
					}
					report(fmt.Sprintf("replace %s with the directory %s, another module's tree, which the lint does not read", old, target))
				} else {
					paths = append(paths, target)
					file.modules = append(file.modules, target)
				}
			}
		case "use":
			if dir := unquote(fields[0]); filepath.Clean(filepath.FromSlash(dir)) != "." {
				report(fmt.Sprintf("use %s, another module's tree, which the lint does not read", dir))
			}
		}
		for _, p := range paths {
			if privateModule(p) {
				report(fmt.Sprintf("%s %s, a module of the rocksolidlabs organization other than %s", verb, p, Module))
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, modFile{}, fmt.Errorf("opencore: %s: %w", name, err)
	}
	return problems, file, nil
}

// modTokens splits a line of go.mod or go.work into tokens as the go
// command's lexer does: outside a string, "//" begins a comment that ends
// the line; a Go string ("..." with escapes) is one token, kept quoted,
// whatever it holds (a "//", a space, a parenthesis); ( ) [ ] { } and ,
// are tokens of their own; anything else runs to a space, one of those
// marks or a comment. "=>" is split out of a word too, which the go
// command would refuse, so the reading is never laxer than the build's. A
// string not closed on its line or not a valid Go string, and a `...`
// string, are errors, as they are to the go command.
func modTokens(line string) ([]string, error) {
	const marks = "()[]{},"
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f' }
	var out []string
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case space(c):
			i++
		case strings.HasPrefix(line[i:], "//"):
			return out, nil
		case strings.IndexByte(marks, c) >= 0:
			out = append(out, line[i:i+1])
			i++
		case strings.HasPrefix(line[i:], "=>"):
			out = append(out, "=>")
			i += 2
		case c == '`':
			return nil, fmt.Errorf("a `...` string, which the go command refuses: %s", line[i:])
		case c == '"':
			j := i + 1
			for ; j < len(line) && line[j] != c; j++ {
				if line[j] == '\\' {
					j++
				}
			}
			if j >= len(line) {
				return nil, fmt.Errorf("a string not closed on its line: %s", line[i:])
			}
			tok := line[i : j+1]
			if _, err := strconv.Unquote(tok); err != nil {
				return nil, fmt.Errorf("string %s: %w", tok, err)
			}
			out = append(out, tok)
			i = j + 1
		default:
			j := i
			for j < len(line) && !space(line[j]) && strings.IndexByte(marks, line[j]) < 0 &&
				!strings.HasPrefix(line[j:], "//") && !strings.HasPrefix(line[j:], "=>") {
				j++
			}
			out = append(out, line[i:j])
			i = j
		}
	}
	return out, nil
}

// localPath reports whether a replacement is a directory rather than a
// module path, by the go command's own rule (modfile.IsDirectoryPath),
// which reads every platform's syntax on every platform because a go.mod
// moves between systems: "." or "..", a path beginning with ./ or ../ (or
// .\ and ..\), a rooted path (/ or \), or a drive prefix (C:). On Linux
// the go command joins C:/../x to the module's root and builds it, so the
// test is not filepath.IsAbs, which is this machine's syntax only.
func localPath(p string) bool {
	return p == "." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, `.\`) ||
		p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, `..\`) ||
		strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		len(p) >= 2 && ('A' <= p[0] && p[0] <= 'Z' || 'a' <= p[0] && p[0] <= 'z') && p[1] == ':'
}

// unquote takes the quotes off a go.mod token written as a Go string, and
// leaves any other token as it is.
func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
