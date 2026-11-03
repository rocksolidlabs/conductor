package local

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// module is this module's path.
const module = "github.com/rocksolidlabs/conductor"

// rawStarts are the calls that start or signal a process with no runner
// between: by the package that declares them, by name. An exec.Cmd made
// without Command (a composite literal, or new) is a raw start too
// (cmdLiteral).
var rawStarts = map[string][]string{
	"os/exec":               {"Command", "CommandContext"},
	"os":                    {"StartProcess", "FindProcess"},
	"syscall":               {"ForkExec", "Exec", "Kill", "StartProcess"},
	"golang.org/x/sys/unix": {"Kill", "Exec"},
}

// rawAllowed are where a raw start or signal is made today, each for a
// reason of its own: internal/pty (sessions), internal/proc (checks, steps,
// services), internal/gitcli (git, and gitclitest), internal/nvim (the
// editor), internal/agents (the identity probe), internal/reach (the
// gateway lookup on darwin) and internal/cli/up.go (opening the browser).
// A directory allows its packages; a .go path its one file. A new site is
// a reviewed edit of this list.
var rawAllowed = []string{
	"internal/pty", "internal/proc", "internal/gitcli", "internal/nvim",
	"internal/agents", "internal/reach", "internal/cli/up.go",
}

// runners are the runners the Runtime wraps, by package: a session's
// terminal (pty.Start, and pty.StartHeld, the native Runtime's), a check's
// command and a tree's service (proc.Run, proc.Serve).
var runners = map[string][]string{
	module + "/internal/pty":  {"Start", "StartHeld"},
	module + "/internal/proc": {"Run", "Serve"},
}

// runnersAllowed are where a runner may be used: the native Runtime, the
// runners' own packages, and the files that start a process past the
// Runtime until the seam's next pull request moves them behind it
// (docs/plans/runtime.md, The PRs: PR-3 the server's and the host's
// session start; internal/crew's executor is the Runtime's since PR-2).
var runnersAllowed = []string{
	"ext/local", "internal/pty", "internal/proc",
	"internal/api/sessions.go", "internal/hostagent/agent.go",
}

// processSite is one place a process is started or signalled outside the
// places allowed: the file (a slash path from the root), the line and the
// call.
type processSite struct {
	file string
	line int
	call string
}

func (s processSite) String() string {
	return fmt.Sprintf("%s:%d: %s: article 12 of the Conductor Program puts every process of a session or a run behind the Runtime (ext.Runtime; docs/plans/runtime.md, The boundary test)", s.file, s.line, s.call)
}

// allowedAt says whether rel, a file's slash path, is under one of places:
// a directory and what is below it, or a file.
func allowedAt(rel string, places []string) bool {
	return slices.ContainsFunc(places, func(p string) bool {
		if strings.HasSuffix(p, ".go") {
			return rel == p
		}
		return strings.HasPrefix(rel, p+"/")
	})
}

// processSites walks the module rooted at root, as the go command reads
// it (no testdata, no directory whose name begins with "." or "_", none
// with a go.mod of its own, and no package manager's tree), parses each
// non-test Go file whatever its build tags, and answers each raw start or
// signal outside rawAllowed and each runner used outside runnersAllowed.
func processSites(root string) ([]processSite, error) {
	fset := token.NewFileSet()
	var sites []processSite
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		sites = append(sites, fileSites(fset, f, filepath.ToSlash(rel))...)
		return nil
	})
	return sites, err
}

// fileSites is processSites for one parsed file. It reads the syntax
// alone, with no type information, so it sees a site by its name: a raw
// start called through a package, an exec.Cmd literal or new(exec.Cmd), a
// runner called or taken as a value, and Kill or Signal on a selector
// ending in Process. A *os.Process signalled through a variable of its own
// (p := cmd.Process; p.Kill()), a start through a function value taken
// from an allowed package, or a method value (f := cmd.Start) is not seen:
// review catches those.
func fileSites(fset *token.FileSet, f *ast.File, rel string) []processSite {
	raw, runner := !allowedAt(rel, rawAllowed), !allowedAt(rel, runnersAllowed)
	var sites []processSite
	report := func(pos token.Pos, call string) {
		sites = append(sites, processSite{rel, fset.Position(pos).Line, call})
	}
	// names maps the file's name for each package of rawStarts and runners
	// to its path, whatever name the import gives it.
	names := map[string]string{}
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		_, isRaw := rawStarts[imp]
		_, isRunner := runners[imp]
		if !(isRaw && raw) && !(isRunner && runner) {
			continue
		}
		name := path.Base(imp)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch name {
		case "_":
		case ".":
			report(spec.Pos(), "a dot import of "+imp)
		default:
			names[name] = imp
		}
	}
	// cmdType says whether e is os/exec's Cmd, under the file's name for
	// the package (a raw start's place allowed or not: names holds os/exec
	// only where it is not).
	cmdType := func(e ast.Expr) (string, bool) {
		sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Cmd" {
			return "", false
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || names[x.Name] != "os/exec" {
			return "", false
		}
		return x.Name, true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			// &exec.Cmd{Path: ...}: a command made without Command.
			if name, ok := cmdType(n.Type); ok {
				report(n.Type.Pos(), name+".Cmd{} (os/exec)")
			}
		case *ast.SelectorExpr:
			x, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			imp, ok := names[x.Name]
			if !ok {
				return true
			}
			if slices.Contains(rawStarts[imp], n.Sel.Name) || slices.Contains(runners[imp], n.Sel.Name) {
				report(n.Sel.Pos(), x.Name+"."+n.Sel.Name+" ("+imp+")")
			}
		case *ast.CallExpr:
			// new(exec.Cmd): a command made without Command.
			if fn, ok := ast.Unparen(n.Fun).(*ast.Ident); ok && fn.Name == "new" && len(n.Args) == 1 {
				if name, ok := cmdType(n.Args[0]); ok {
					report(n.Args[0].Pos(), "new("+name+".Cmd) (os/exec)")
				}
				return true
			}
			// cmd.Process.Kill(), cmd.Process.Signal(sig): a process
			// signalled through what started it.
			sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr)
			if !ok || !raw || (sel.Sel.Name != "Kill" && sel.Sel.Name != "Signal") {
				return true
			}
			var last string
			switch x := sel.X.(type) {
			case *ast.SelectorExpr:
				last = x.Sel.Name
			case *ast.Ident:
				last = x.Name
			}
			if last == "Process" {
				report(sel.Sel.Pos(), "Process."+sel.Sel.Name)
			}
		}
		return true
	})
	return sites
}

// TestOnlyTheRunnersStartOrSignalAProcess makes constraints 1, 2 and 5 of
// the Program's section 2.5 a failing test: no file of the module starts
// or signals a process but those allowed (rawAllowed), and none uses a
// runner the Runtime wraps past it (runnersAllowed); and the walker
// catches its planted sites.
func TestOnlyTheRunnersStartOrSignalAProcess(t *testing.T) {
	t.Run("the module", func(t *testing.T) {
		sites, err := processSites(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sites {
			t.Errorf("%s", s)
		}
	})
	t.Run("its plants", boundaryPlants)
}

// boundaryPlants: the walker catches each planted site, under whatever
// name the import gives the package, and lets through what is allowed: a
// test file, a LookPath, the native Runtime's use of every runner, the
// runners' own packages, and the directories the go command does not
// read.
func boundaryPlants(t *testing.T) {
	root := t.TempDir()
	pty, proc := strconv.Quote(module+"/internal/pty"), strconv.Quote(module+"/internal/proc")
	files := map[string]string{
		"go.mod": "module " + module + "\n",
		"internal/api/plant.go": "package api\n\nimport (\n\t\"os/exec\"\n\tx \"os/exec\"\n)\n\n" +
			"func a() { exec.Command(\"sh\") }\n" + // line 8
			"func b() { x.CommandContext(nil, \"sh\") }\n" + // line 9
			"func c() { exec.LookPath(\"sh\") }\n" +
			"func d(cmd *exec.Cmd) { cmd.Process.Kill() }\n", // line 11
		"internal/api/start.go": "package api\n\nimport " + pty + "\n\nfunc e() { pty.Start(pty.Spec{}) }\n", // line 5
		"internal/api/held.go":  "package api\n\nimport p " + pty + "\n\nvar held = p.StartHeld\n",           // line 5
		"internal/api/lit.go": "package api\n\nimport e \"os/exec\"\n\n" +
			"func n() { c := &e.Cmd{Path: \"/bin/sh\"}; c.Start() }\n" + // line 5
			"func o() { c := new(e.Cmd); c.Path = \"/bin/sh\"; c.Start() }\n" + // line 6
			"func q(c *e.Cmd) e.Cmd { return e.Cmd{} }\n", // line 7: the literal, not the types
		"internal/gitcli/lit.go":      "package gitcli\n\nimport \"os/exec\"\n\nvar _ = &exec.Cmd{}\n",
		"internal/crew/plant.go":      "package crew\n\nimport " + proc + "\n\nfunc f() { proc.Run(nil, proc.Spec{}) }\n",
		"internal/hostagent/plant.go": "package hostagent\n\nimport p " + proc + "\n\nvar serve = p.Serve\n",
		"internal/api/plant_test.go":  "package api\n\nimport \"os/exec\"\n\nfunc g() { exec.Command(\"sh\") }\n",
		"ext/local/native.go": "package local\n\nimport (\n\t" + pty + "\n\t" + proc + "\n)\n\n" +
			"var _, _, _, _ = pty.Start, pty.StartHeld, proc.Run, proc.Serve\n",
		"internal/proc/run.go":         "package proc\n\nimport \"os/exec\"\n\nfunc h() { exec.Command(\"sh\") }\n",
		"internal/gitcli/x/plant.go":   "package x\n\nimport \"os/exec\"\n\nfunc i() { exec.Command(\"git\") }\n",
		"internal/api/testdata/x.go":   "package x\n\nimport \"os/exec\"\n\nfunc j() { exec.Command(\"sh\") }\n",
		".claude/worktrees/w/x.go":     "package x\n\nimport \"os/exec\"\n\nfunc k() { exec.Command(\"sh\") }\n",
		"nested/go.mod":                "module example.com/nested\n",
		"nested/x.go":                  "package x\n\nimport \"os/exec\"\n\nfunc l() { exec.Command(\"sh\") }\n",
		"internal/api/dot.go":          "package api\n\nimport . \"os/exec\"\n\nvar _ = Command\n",                                     // line 3
		"internal/api/signal_other.go": "//go:build windows\n\npackage api\n\nimport \"syscall\"\n\nfunc m() { syscall.Kill(1, 9) }\n", // line 7
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sites, err := processSites(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sites {
		got = append(got, fmt.Sprintf("%s:%d: %s", s.file, s.line, s.call))
		if !strings.Contains(s.String(), "article 12") {
			t.Errorf("a report names no article: %s", s)
		}
	}
	slices.Sort(got)
	want := []string{
		"internal/api/dot.go:3: a dot import of os/exec",
		"internal/api/held.go:5: p.StartHeld (" + module + "/internal/pty)",
		"internal/api/lit.go:5: e.Cmd{} (os/exec)",
		"internal/api/lit.go:6: new(e.Cmd) (os/exec)",
		"internal/api/lit.go:7: e.Cmd{} (os/exec)",
		"internal/api/plant.go:11: Process.Kill",
		"internal/api/plant.go:8: exec.Command (os/exec)",
		"internal/api/plant.go:9: x.CommandContext (os/exec)",
		"internal/api/signal_other.go:7: syscall.Kill (syscall)",
		"internal/api/start.go:5: pty.Start (" + module + "/internal/pty)",
		"internal/crew/plant.go:5: proc.Run (" + module + "/internal/proc)",
		"internal/hostagent/plant.go:5: p.Serve (" + module + "/internal/proc)",
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the walker reported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
