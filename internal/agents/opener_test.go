package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The opener is one path with no space or colon, 0700, that hands its one
// argument to conductor open as one argument: a URL with what a shell
// would read is passed as it is, never evaluated.
func TestOpenerHandsItsArgumentToConductorOpen(t *testing.T) {
	dir := t.TempDir()
	// A conductor that writes what it was given, one argument a line.
	got := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "it's conductor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > "+shellQuote(got)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(dir, "hooks")
	p, err := Opener(hooks, bin)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(hooks, openerName) || !OpenerPathOK(p) {
		t.Fatalf("the opener %q", p)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("its mode: %v %v", fi, err)
	}
	const link = "https://example.test/a?b=$(touch /tmp/x)&c=`id`;d|e"
	if out, err := exec.Command(p, link).CombinedOutput(); err != nil {
		t.Fatalf("the opener: %v %s", err, out)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != "open\n--\n"+link+"\n" {
		t.Fatalf("conductor was given %q: %v", b, err)
	}
	// Written again for the same binary, it is left as it is.
	again, err := Opener(hooks, bin)
	if err != nil || again != p {
		t.Fatalf("again: %q %v", again, err)
	}
}

// A hooks dir whose path BROWSER cannot hold (a space, as in macOS's
// Application Support) puts the opener in a directory of the user's own
// under the temporary directory; one that is another's or open is refused.
func TestOpenerAvoidsAPathWithASpace(t *testing.T) {
	tmp := t.TempDir()
	own := filepath.Join(tmp, "conductor-me")
	old := openerTempDir
	openerTempDir = func() string { return own }
	t.Cleanup(func() { openerTempDir = old })
	hooks := filepath.Join(tmp, "Application Support", "hooks")
	p, err := Opener(hooks, "/usr/local/bin/conductor")
	if err != nil || p != filepath.Join(own, openerName) {
		t.Fatalf("%q %v", p, err)
	}
	if fi, err := os.Lstat(own); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the directory: %v %v", fi, err)
	}
	if err := os.Chmod(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Opener(hooks, "/usr/local/bin/conductor"); err == nil {
		t.Fatal("a directory open to others was taken")
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(tmp, link); err != nil {
		t.Fatal(err)
	}
	openerTempDir = func() string { return link }
	if _, err := Opener(hooks, "/usr/local/bin/conductor"); err == nil {
		t.Fatal("a link was taken for the directory")
	}
	if _, err := Opener(t.TempDir(), "relative/conductor"); err == nil {
		t.Fatal("a relative binary was taken")
	}
}

func TestOpenerPathOK(t *testing.T) {
	for _, p := range []string{"/home/me/.conductor/hooks/open-url", "/tmp/conductor-1000/open-url"} {
		if !OpenerPathOK(p) {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range []string{"relative/open-url", "/Users/me/Library/Application Support/x", "/a:b/open-url", "/a%s/open-url", "/a\tb", "/a'b", "/a\nb"} {
		if OpenerPathOK(p) {
			t.Errorf("%q taken", p)
		}
	}
}

// BROWSER is added only where the environment has none: the person's own wins.
func TestWithOpenerKeepsAnAgentsOwnBrowser(t *testing.T) {
	env := []string{"HOME=/h", "PATH=/bin"}
	if got := WithOpener(env, "/o"); strings.Join(got, ",") != "HOME=/h,PATH=/bin,BROWSER=/o" {
		t.Fatalf("%q", got)
	}
	for _, own := range []string{"BROWSER=/mine", "BROWSER="} {
		mine := append([]string{own}, env...)
		if got := WithOpener(mine, "/o"); strings.Join(got, ",") != strings.Join(mine, ",") {
			t.Fatalf("%q", got)
		}
	}
}
