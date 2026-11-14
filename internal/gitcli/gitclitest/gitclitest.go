// Package gitclitest makes repositories for the tests of the git Conductor
// runs: a repository whose own configuration names a program at each place
// git can start one (hooks, the file system monitor, the pager, external
// diff and text conversion, clean, smudge and process filters, the editor,
// credentials, transports, signatures), some of it through include and
// includeIf, each program a script that leaves a mark when it runs. A test
// runs Conductor's git on it, checks the results, and checks that nothing
// left a mark.
package gitclitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Repo is a repository New made.
type Repo struct {
	// Dir is the working tree, its symbolic links resolved.
	Dir string
	// Marks is the directory the scripts leave their marks in.
	Marks string
	// Script is the marking script, as a command for sh: given a name, it
	// leaves that mark and passes its input through.
	Script string
}

// Options choose how New arms the repository.
type Options struct {
	// HooksPath names a directory of hooks in the repository's config
	// (core.hooksPath); without it the hooks are in .git/hooks.
	HooksPath bool
}

// Committed is what New commits, by path; Dirty is what it leaves in the
// working tree afterwards (each tracked file one line longer, and an
// untracked file).
var (
	Committed = map[string]string{
		"README":         "one\ntwo\nthree\n",
		"conv.txt":       "a\n",
		"clean.dat":      "x\n",
		"proc.pdat":      "p\n",
		"inc.idat":       "i\n",
		"empty.edat":     "e\n",
		"branch.bdat":    "b\n",
		"tree.wdat":      "w\n",
		"owner.own":      "o\n",
		".gitattributes": "*.txt diff=conv\n*.dat filter=single\n*.pdat filter=proc\n*.idat filter=inc\n*.edat filter=\n*.bdat filter=onbranch\n*.wdat filter=inworktree\n*.own filter=owner\n",
	}
	Dirty = map[string]string{
		"conv.txt":   "a\nb\n",
		"clean.dat":  "x\ny\n",
		"proc.pdat":  "p\nq\n",
		"inc.idat":   "i\nj\n",
		"empty.edat": "e\nf\n",
		"new.txt":    "n\n",
	}
)

// Hooks are the hooks New installs, each leaving the mark hook-<name>.
var Hooks = []string{"post-checkout", "reference-transaction", "post-index-change", "pre-commit", "post-commit", "post-rewrite", "pre-auto-gc", "fsmonitor-watchman"}

// Git runs git -C dir args the way a test sets a repository up: not
// through Conductor's runner, without the system configuration, as a test
// identity, its automatic maintenance off, so that no git of it (from Git
// 2.5x on, a commit's detached maintenance run) outlives the call. A
// failure fails the test.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// New makes a repository on the branch main in a temporary directory,
// commits Committed, arms it (below), then writes Dirty. It skips the test
// when git is not installed, and sets HOME to a temporary directory so that
// no configuration of the person running the tests applies. Armed, the
// repository's own configuration names
// a marking script as core.fsmonitor, diff.conv.textconv and the filter
// with the empty name (from a file include.path names), as the clean,
// smudge and process filters single, proc and inc (inc from a file an
// includeIf gitdir: names), onbranch (included on a crew/ branch only) and
// inworktree (included in a linked worktree only), as core.pager and the pager of
// each command Conductor runs, as diff.external and diff.conv.command, as
// core.editor and sequence.editor, as credential.helper and core.askPass,
// as core.sshCommand, as the upload-pack and receive-pack of the remote
// origin (another repository, its protocols allowed), and as gpg.program
// with log.showSignature on; every hook of Hooks is a marking script. The
// filter owner, which *.own files name, is defined nowhere: a test defines
// it where it wants it (HOME is the global configuration's).
func New(t testing.TB, opts Options) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Repo{Dir: filepath.Join(base, "repo"), Marks: filepath.Join(base, "marks")}
	bin := filepath.Join(base, "bin")
	for _, d := range []string{r.Dir, r.Marks, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	Git(t, r.Dir, "init", "-q", "--initial-branch=main")
	for path, body := range Committed {
		write(t, filepath.Join(r.Dir, path), body, 0o644)
	}
	Git(t, r.Dir, "add", "-A")
	Git(t, r.Dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")

	// The marking script: leaves the mark its first argument names, then
	// passes its input through, so that as a filter it changes nothing.
	mark := filepath.Join(bin, "mark")
	write(t, mark, "#!/bin/sh\ntouch "+shellQuote(r.Marks)+"/\"$1\"\nexec cat\n", 0o755)
	r.Script = shellQuote(mark)
	cmd := func(name string) string { return r.Script + " " + name }

	hooks := filepath.Join(r.Dir, ".git", "hooks")
	if opts.HooksPath {
		hooks = filepath.Join(base, "hooks")
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range Hooks {
		write(t, filepath.Join(hooks, h), "#!/bin/sh\ntouch "+shellQuote(r.Marks)+"/hook-"+h+"\ncat >/dev/null\n", 0o755)
	}

	remote := filepath.Join(base, "remote")
	Git(t, base, "init", "-q", "--bare", remote)

	filter := func(name, mark string) string {
		return "[filter \"" + name + "\"]\n\tclean = " + cfgQuote(cmd(mark+"-clean")) + "\n\tsmudge = " + cfgQuote(cmd(mark+"-smudge")) + "\n\trequired = true\n"
	}
	included := filepath.Join(base, "included.cfg")
	write(t, included, "[core]\n\tfsmonitor = "+cfgQuote(cmd("fsmonitor"))+"\n[diff \"conv\"]\n\ttextconv = "+cfgQuote(cmd("textconv"))+"\n"+filter("", "empty"), 0o644)
	conditional := filepath.Join(base, "conditional.cfg")
	write(t, conditional, filter("inc", "inc"), 0o644)
	onBranch := filepath.Join(base, "onbranch.cfg")
	write(t, onBranch, filter("onbranch", "onbranch"), 0o644)
	inWorktree := filepath.Join(base, "inworktree.cfg")
	write(t, inWorktree, filter("inworktree", "inworktree"), 0o644)

	set := [][2]string{
		{"core.pager", cmd("pager")},
		{"pager.status", cmd("pager-status")},
		{"pager.diff", cmd("pager-diff")},
		{"pager.show", cmd("pager-show")},
		{"pager.log", cmd("pager-log")},
		{"diff.external", cmd("external-diff")},
		{"diff.conv.command", cmd("diff-command")},
		{"filter.single.clean", cmd("clean")},
		{"filter.single.smudge", cmd("smudge")},
		{"filter.single.required", "true"},
		{"filter.proc.process", cmd("process")},
		{"core.editor", cmd("editor")},
		{"sequence.editor", cmd("sequence-editor")},
		{"credential.helper", "!" + cmd("credential")},
		{"core.askPass", cmd("askpass")},
		{"core.sshCommand", cmd("ssh")},
		{"remote.origin.url", remote},
		{"remote.origin.uploadpack", cmd("upload-pack")},
		{"remote.origin.receivepack", cmd("receive-pack")},
		{"protocol.allow", "always"},
		{"protocol.file.allow", "always"},
		{"gpg.program", cmd("gpg")},
		{"log.showSignature", "true"},
		{"include.path", included},
		{"includeIf.gitdir:" + r.Dir + "/.path", conditional},
		{"includeIf.onbranch:crew/**.path", onBranch},
		{"includeIf.gitdir:" + r.Dir + "/.git/worktrees/**.path", inWorktree},
	}
	if opts.HooksPath {
		set = append(set, [2]string{"core.hooksPath", hooks})
	}
	for _, kv := range set {
		Git(t, r.Dir, "config", kv[0], kv[1])
	}
	for path, body := range Dirty {
		write(t, filepath.Join(r.Dir, path), body, 0o644)
	}
	return r
}

// Fired lists the marks left so far: what ran, sorted.
func (r *Repo) Fired(t testing.TB) []string {
	t.Helper()
	entries, err := os.ReadDir(r.Marks)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// NoneFired fails the test when anything left a mark, naming what and when.
func (r *Repo) NoneFired(t testing.TB, when string) {
	t.Helper()
	if fired := r.Fired(t); len(fired) > 0 {
		t.Fatalf("%s: the repository's programs ran: %s", when, strings.Join(fired, ", "))
	}
}

func write(t testing.TB, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// shellQuote quotes s for sh, which runs the configured commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cfgQuote quotes s as a value of a git configuration file.
func cfgQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
