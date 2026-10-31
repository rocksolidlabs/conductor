package cli

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// fakeConductor is a conductor that answers `crews --ids` with ids, for the
// scripts to run as the command typed (words[1] / COMP_WORDS[0]).
func fakeConductor(t *testing.T, ids ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "conductor")
	body := "#!/bin/sh\n[ \"$1\" = crews ] && [ \"$2\" = --ids ] && cat <<'IDS'\n" + strings.Join(ids, "\n") + "\nIDS\nexit 0\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// runCompletionWith runs `conductor completion args...` and returns what it printed.
func runCompletionWith(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	return runWith(t, func(_ context.Context, a []string, o, e io.Writer) (int, error) { return runCompletion(a, o, e) }, args...)
}

// scriptFile writes the completion script for shell to a file.
func scriptFile(t *testing.T, shell string) string {
	t.Helper()
	code, stdout, stderr, err := runCompletionWith(t, shell)
	if code != 0 || err != nil {
		t.Fatalf("completion %s: exit %d %v\n%s", shell, code, err, stderr)
	}
	p := filepath.Join(t.TempDir(), "conductor."+shell)
	if err := os.WriteFile(p, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// complete runs the script under the real shell (skipped when absent) with
// the completion system stubbed, completing the last of words, and returns
// the candidates, one per line. dir is the working directory, for file
// completion.
func complete(t *testing.T, shell, script, dir string, words ...string) []string {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s is not installed", shell)
	}
	var cmd *exec.Cmd
	switch shell {
	case "zsh":
		cmd = exec.Command("zsh", "-f", "-c", `compdef() { :; }; _files() { print -r -- "<files>" }; compadd() { local a; for a; do [[ $a == -- ]] || print -r -- "$a"; done }; source "$1"; shift; words=("$@"); CURRENT=${#words}; _conductor`, "zsh", script)
	case "bash":
		cmd = exec.Command("bash", "--noprofile", "--norc", "-c", `source "$1"; shift; COMP_WORDS=("$@"); COMP_CWORD=$(( ${#COMP_WORDS[@]} - 1 )); _conductor; (( ${#COMPREPLY[@]} )) && printf '%s\n' "${COMPREPLY[@]}"; true`, "bash", script)
	}
	cmd.Args = append(cmd.Args, words...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// Both scripts parse under their shell.
func TestCompletionScriptsParse(t *testing.T) {
	for _, sh := range []string{"zsh", "bash"} {
		t.Run(sh, func(t *testing.T) {
			if _, err := exec.LookPath(sh); err != nil {
				t.Skipf("%s is not installed", sh)
			}
			if out, err := exec.Command(sh, "-n", scriptFile(t, sh)).CombinedOutput(); err != nil {
				t.Fatalf("%s -n: %v\n%s", sh, err, out)
			}
		})
	}
}

// The scripts complete subcommands, flags, flag values, positional words
// and, for up, the crew ids the typed conductor prints.
func TestCompletionCompletes(t *testing.T) {
	for _, sh := range []string{"zsh", "bash"} {
		t.Run(sh, func(t *testing.T) {
			script := scriptFile(t, sh)
			bin := fakeConductor(t, "alpha", "beta")
			dir := t.TempDir()
			for _, name := range []string{"x.json", "my config.json"} {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			has := func(words []string, want ...string) bool {
				for _, w := range want {
					if !slices.Contains(words, w) {
						return false
					}
				}
				return true
			}
			if got := complete(t, sh, script, dir, bin, ""); !has(got, "serve", "host", "notify", "up", "crews", "hooks", "skill", "completion", "version") {
				t.Fatalf("subcommands: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "up", ""); !slices.Equal(got, []string{"alpha", "beta"}) {
				t.Fatalf("crew ids: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "up", "--"); !has(got, "--server", "--token", "--open") {
				t.Fatalf("up flags: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "up", "--server", ""); len(got) != 0 {
				t.Fatalf("a free value completes nothing: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "serve", "--log-level", ""); !slices.Equal(got, []string{"debug", "info", "warn", "error"}) {
				t.Fatalf("log level: %v", got)
			}
			// A file name with a space is one candidate, not two words.
			got := complete(t, sh, script, dir, bin, "serve", "--config", "")
			if (sh == "zsh" && !slices.Equal(got, []string{"<files>"})) || (sh == "bash" && !slices.Equal(slices.Sorted(slices.Values(got)), []string{"my config.json", "x.json"})) {
				t.Fatalf("files: %q", got)
			}
			if got := complete(t, sh, script, dir, bin, "hooks", ""); !slices.Equal(got, []string{"install", "status"}) {
				t.Fatalf("hooks words: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "hooks", "install", ""); !has(got, "all", "claude", "codex") {
				t.Fatalf("adapters: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "completion", ""); !slices.Equal(got, []string{"zsh", "bash", "install"}) {
				t.Fatalf("completion words: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "dispatch", ""); !slices.Equal(got, []string{"projects", "tickets", "show", "create", "edit", "comment", "accept", "link"}) {
				t.Fatalf("dispatch words: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "dispatch", "--status", ""); !slices.Equal(got, dispatch.Statuses) {
				t.Fatalf("dispatch statuses: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "task", "ticket", ""); !slices.Equal(got, []string{"comment"}) {
				t.Fatalf("task ticket words: %v", got)
			}
			if got := complete(t, sh, script, dir, bin, "notify", "--"); !has(got, "--claude-hook", "--goose-hook", "--quiet") {
				t.Fatalf("notify flags: %v", got)
			}
			if sh == "bash" {
				if got := complete(t, sh, script, dir, bin, "up", "a"); !slices.Equal(got, []string{"alpha"}) {
					t.Fatalf("bash filters by the typed prefix: %v", got)
				}
			}
		})
	}
}

// Review Focus 2: whatever `crews --ids` prints reaches the candidates as
// data. A line holding shell syntax is never run, a line not shaped like a
// crew id is dropped by the script too (an older or foreign conductor may
// print it), and a typed prefix with pattern characters is compared as text.
// The directory holds a file, so that a candidate globbed against it shows.
func TestCompletionNeverRunsWhatTheServerSends(t *testing.T) {
	for _, sh := range []string{"zsh", "bash"} {
		t.Run(sh, func(t *testing.T) {
			script := scriptFile(t, sh)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "x.json"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "ran")
			bin := fakeConductor(t, "alpha", "$(touch "+marker+")", "`touch "+marker+"`", "a b", "*", "-flag", "Beta", "ok;id", "a\x1b[31m")
			got := complete(t, sh, script, dir, bin, "up", "")
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("an id was run as a command: %q", got)
			}
			if !slices.Equal(got, []string{"alpha"}) {
				t.Fatalf("candidates: %q", got)
			}
			if sh == "bash" {
				for _, typed := range []string{"*", "a*", "[a]"} {
					if got := complete(t, sh, script, dir, bin, "up", typed); len(got) != 0 {
						t.Fatalf("a typed %q is text, not a pattern: %q", typed, got)
					}
				}
			}
		})
	}
}

// The pattern the scripts apply to `crews --ids` is crew.ValidID's.
func TestCompletionIDShapeIsValidID(t *testing.T) {
	re := regexp.MustCompile(idShape)
	for _, id := range []string{"alpha", "a", "0", "beta-2", "a--b", "a-", strings.Repeat("a", 64), strings.Repeat("a", 65), "", "-a", "Alpha", "a b", "a_b", "a.b", "ä", "a\n", "$(x)", "*"} {
		if re.MatchString(id) != crew.ValidID(id) {
			t.Errorf("%q: idShape %v, crew.ValidID %v", id, re.MatchString(id), crew.ValidID(id))
		}
	}
}

// The scripts know every flag the commands define, and no other: the table
// in completionSpec is checked against each command's -h.
func TestCompletionSpecMatchesEveryFlag(t *testing.T) {
	ctx := t.Context()
	helps := map[string][][]string{
		"serve": {{"serve", "-h"}}, "switchyard": {{"switchyard", "-h"}}, "host": {{"host", "-h"}}, "notify": {{"notify", "-h"}}, "up": {{"up", "-h"}}, "crews": {{"crews", "-h"}},
		"hooks": {{"hooks", "install", "-h"}, {"hooks", "status", "-h"}}, "completion": {{"completion", "install", "-h"}},
		"skill": {{"skill", "-h"}}, "crew": {{"crew", "create", "-h"}}, "mcp": {{"mcp", "-h"}}, "task": {{"task", "context", "-h"}, {"task", "result", "-h"}, {"task", "send", "-h"}, {"task", "inbox", "-h"}, {"task", "ack", "-h"}, {"task", "blocked", "-h"}, {"task", "checkpoint", "-h"}, {"task", "ticket", "-h"}},
		"dispatch": {{"dispatch", "projects", "-h"}, {"dispatch", "tickets", "-h"}, {"dispatch", "show", "-h"}, {"dispatch", "create", "-h"}, {"dispatch", "edit", "-h"}, {"dispatch", "comment", "-h"}, {"dispatch", "accept", "-h"}, {"dispatch", "link", "-h"}},
	}
	flagLine := regexp.MustCompile(`(?m)^  -([a-z][a-z0-9-]*)`)
	for _, c := range completionSpec() {
		want := map[string]bool{}
		for _, args := range helps[c.name] {
			var stderr bytes.Buffer
			if code, err := Run(ctx, args, strings.NewReader(""), io.Discard, &stderr); code != 0 || err != nil {
				t.Fatalf("%v: exit %d %v", args, code, err)
			}
			for _, m := range flagLine.FindAllStringSubmatch(stderr.String(), -1) {
				want["--"+m[1]] = true
			}
		}
		got := map[string]bool{}
		for _, f := range c.flags {
			got[f.name] = true
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: the table has %v, the command %v", c.name, slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
		}
	}
}

// conductor completion prints a script for zsh or bash and refuses the rest.
func TestCompletionUsage(t *testing.T) {
	if code, out, _, err := runCompletionWith(t, "zsh"); code != 0 || err != nil || !strings.HasPrefix(out, "#compdef conductor\n") || !strings.Contains(out, "compdef _conductor conductor") {
		t.Fatalf("zsh: %d %v\n%s", code, err, out)
	}
	if code, out, _, err := runCompletionWith(t, "bash"); code != 0 || err != nil || !strings.Contains(out, "complete -F _conductor conductor") {
		t.Fatalf("bash: %d %v\n%s", code, err, out)
	}
	for _, args := range [][]string{{}, {"fish"}, {"zsh", "extra"}} {
		if code, _, _, err := runCompletionWith(t, args...); code != 2 || err == nil {
			t.Fatalf("%v: %d %v", args, code, err)
		}
	}
}

// completion install appends one marked line to the shell's rc file, once;
// the shell comes from $SHELL unless --shell says; a missing file is made.
func TestCompletionInstall(t *testing.T) {
	clearConductorEnv(t)
	home := os.Getenv("HOME")
	t.Setenv("SHELL", "/bin/zsh")
	install := func(args ...string) (int, string, string, error) {
		return runCompletionWith(t, append([]string{"install"}, args...)...)
	}
	rc := filepath.Join(home, ".zshrc")
	code, out, _, err := install()
	// For zsh it says the line needs compinit before it.
	if code != 0 || err != nil || !strings.Contains(out, rc) || !strings.Contains(out, "compinit") {
		t.Fatalf("first: %d %v\n%s", code, err, out)
	}
	want := completionLine("zsh") + "\n"
	if b, _ := os.ReadFile(rc); string(b) != want {
		t.Fatalf("rc:\n%s", b)
	}
	if fi, _ := os.Stat(rc); fi.Mode().Perm() != 0o600 {
		t.Fatalf("a new rc file is %v", fi.Mode().Perm())
	}
	code, out, _, err = install()
	if code != 0 || err != nil || !strings.Contains(out, "nothing to change") {
		t.Fatalf("second: %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(rc); string(b) != want {
		t.Fatalf("rc changed on the second install:\n%s", b)
	}
	// A file without a final newline gets one before the line; --shell and --rc override the defaults.
	custom := filepath.Join(home, "rc")
	if err := os.WriteFile(custom, []byte("alias l='ls'"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _, err := install("--shell", "bash", "--rc", custom); code != 0 || err != nil || strings.Contains(out, "compinit") {
		t.Fatalf("custom: %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(custom); string(b) != "alias l='ls'\n"+completionLine("bash")+"\n" {
		t.Fatalf("custom rc:\n%s", b)
	}
	t.Setenv("SHELL", "/usr/bin/fish")
	if code, _, _, err := install(); code != 2 || err == nil {
		t.Fatalf("fish: %d %v", code, err)
	}
	// Without a home, it says which flag names the file.
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("HOME", "")
	if code, _, _, err := install(); code != 1 || err == nil || !strings.Contains(err.Error(), "--rc") || strings.Contains(err.Error(), "--home") {
		t.Fatalf("no home: %d %v", code, err)
	}
}

// Only the installed line itself, trimmed, counts as installed: a comment of
// the user's that mentions the mark does not.
func TestCompletionInstallMatchesTheWholeLine(t *testing.T) {
	clearConductorEnv(t)
	home := os.Getenv("HOME")
	own := "# conductor completion: maybe later\nalias c=conductor # conductor completion\n"
	rc := filepath.Join(home, "rc")
	if err := os.WriteFile(rc, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _, err := runCompletionWith(t, "install", "--shell", "zsh", "--rc", rc); code != 0 || err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("install: %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(rc); string(b) != own+completionLine("zsh")+"\n" {
		t.Fatalf("rc:\n%s", b)
	}
	indented := filepath.Join(home, "indented")
	if err := os.WriteFile(indented, []byte("  "+completionLine("bash")+" \t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _, err := runCompletionWith(t, "install", "--shell", "bash", "--rc", indented); code != 0 || err != nil || !strings.Contains(out, "nothing to change") {
		t.Fatalf("indented: %d %v\n%s", code, err, out)
	}
}

// rootFile returns a regular file root owns, and skips the test where there
// is none, or where the tests run as root (whose own files root owns).
func rootFile(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("needs a user other than root")
	}
	for _, p := range []string{"/etc/passwd", "/etc/hosts"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			if owner, ok := agents.FileOwner(fi); ok && owner == 0 {
				return p
			}
		}
	}
	t.Skip("no regular file owned by root")
	return ""
}

// rootDir returns a directory root owns that this user may make files in (the
// sticky /tmp), and skips the test where there is none.
func rootDir(t *testing.T) string {
	t.Helper()
	for _, d := range []string{os.TempDir(), "/tmp"} {
		// Resolved first: on macOS /tmp is a link to /private/tmp, and the
		// install names the directory as the system reaches it.
		if real, err := filepath.EvalSymlinks(d); err == nil {
			d = real
		}
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		if owner, ok := agents.FileOwner(fi); ok && owner == 0 {
			if f, err := os.CreateTemp(d, "conductor-probe-*"); err == nil {
				f.Close()
				os.Remove(f.Name())
				return d
			}
		}
	}
	t.Skip("no directory of root's to make a file in")
	return ""
}

// tempName returns a name for a new entry in dir, removed when the test ends.
func tempName(t *testing.T, dir string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "conductor-rc-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

// Under sudo, a link the user made to a file of root's is refused, naming the
// link, and the file is untouched: root (geteuid 0 through the seam) owns the
// directory, /tmp, and the file the link leads to, so only the link's own
// owner tells. CheckOwner, which follows the link, passes it.
func TestCompletionInstallRefusesAnotherUsersLink(t *testing.T) {
	clearConductorEnv(t)
	target := rootFile(t)
	dir := rootDir(t)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	link := tempName(t, dir)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agents.SetEUIDForTest(0))
	if err := agents.CheckOwner(link); err != nil {
		t.Fatalf("CheckOwner judges root's file, which root owns: %v", err)
	}
	code, out, _, err := runCompletionWith(t, "install", "--shell", "zsh", "--rc", link)
	if code != 1 || err == nil || !strings.Contains(err.Error(), link+", a link,") || !strings.Contains(err.Error(), "nothing written") || out != "" {
		t.Fatalf("exit %d %v\n%s", code, err, out)
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(after, before) {
		t.Fatalf("%s changed", target)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the link: %v %v", fi, err)
	}
}

// A link of one's own to a file of root's (the user is not root) is refused,
// saying where the link leads, and the file is untouched.
func TestCompletionInstallRefusesAnOwnLinkToRootsFile(t *testing.T) {
	clearConductorEnv(t)
	target := rootFile(t)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	code, out, _, err := runCompletionWith(t, "install", "--shell", "zsh", "--rc", link)
	if code != 1 || err == nil || !strings.Contains(err.Error(), link+" leads to a file of ") || !strings.Contains(err.Error(), "nothing written") || strings.Contains(err.Error(), "sudo") || out != "" {
		t.Fatalf("exit %d %v\n%s", code, err, out)
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(after, before) {
		t.Fatalf("%s changed", target)
	}
}

// A file of one's own in a directory of another user's is refused, naming
// the directory, and left as it was: who owns the directory may swap the
// file for a link between the check and the write.
func TestCompletionInstallRefusesAnotherUsersDirectory(t *testing.T) {
	clearConductorEnv(t)
	if os.Geteuid() == 0 {
		t.Skip("needs a user other than root")
	}
	dir := rootDir(t)
	rc := tempName(t, dir)
	const own = "alias l='ls'\n"
	if err := os.WriteFile(rc, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _, err := runCompletionWith(t, "install", "--shell", "bash", "--rc", rc)
	if code != 1 || err == nil || !strings.Contains(err.Error(), dir+" belongs to") || !strings.Contains(err.Error(), "nothing written") || out != "" {
		t.Fatalf("exit %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(rc); string(b) != own {
		t.Fatalf("rc:\n%s", b)
	}
}

// A refused install leaves the file as it was: the rc file belongs to
// another user (through the seam), in a home that passes as the process's own.
func TestCompletionInstallLeavesAnotherUsersFileAlone(t *testing.T) {
	clearConductorEnv(t)
	home := os.Getenv("HOME")
	rc := filepath.Join(home, ".bashrc")
	const own = "export EDITOR=vi\n"
	if err := os.WriteFile(rc, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(rc); err != nil {
		t.Fatal(err)
	} else if _, ok := agents.FileOwner(fi); !ok {
		t.Skip("this platform does not report who owns a file")
	}
	t.Cleanup(agents.SetEUIDForTest(os.Geteuid() + 1))
	code, out, _, err := runCompletionWith(t, "install", "--shell", "bash")
	if code != 1 || err == nil || !strings.Contains(err.Error(), rc+" belongs to") || !strings.Contains(err.Error(), "nothing written") || out != "" {
		t.Fatalf("exit %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(rc); string(b) != own {
		t.Fatalf("rc:\n%s", b)
	}
}

// The owned case: a link of one's own to a file of one's own (dotfiles kept
// elsewhere) gets the line in the file it leads to, whose mode is kept; the
// link stays a link.
func TestCompletionInstallThroughAnOwnLink(t *testing.T) {
	clearConductorEnv(t)
	home := os.Getenv("HOME")
	target := filepath.Join(home, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("setopt nobeep\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if code, out, _, err := runCompletionWith(t, "install", "--shell", "zsh"); code != 0 || err != nil || !strings.Contains(out, "wrote") {
		t.Fatalf("exit %d %v\n%s", code, err, out)
	}
	if b, _ := os.ReadFile(target); string(b) != "setopt nobeep\n"+completionLine("zsh")+"\n" {
		t.Fatalf("target:\n%s", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("the link: %v %v", fi, err)
	}
}
