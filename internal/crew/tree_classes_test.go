package crew

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A tree whose .gitignore git cannot read makes check-ignore warn on its
// error stream and exit 1, none ignored: the classes are answered by the
// exit status, the tracked path tracked and no path ignored.
func TestTheClassesAreAnsweredWhateverGitWarns(t *testing.T) {
	repo := newRepo(t)
	wt := linkedTree(t, repo, "build.1")
	rules := filepath.Join(wt, ".gitignore")
	writeFile(t, rules, "new.txt\n")
	writeFile(t, filepath.Join(wt, "new.txt"), "new\n")
	tr, err := OpenTree(repo, wt)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ix := &TreeIndex{Path: filepath.Join(t.TempDir(), "index")}
	read, err := TreeRevision(t.Context(), tr, ix)
	if err != nil {
		t.Fatal(err)
	}
	// The rules become unreadable after the read: git itself warns of
	// them and exits 1 here.
	if err := os.Chmod(rules, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rules, 0o644) })
	cmd := exec.Command("git", "-C", wt, "check-ignore", "-z", "--stdin")
	cmd.Stdin = strings.NewReader("README\x00new.txt\x00")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "warning") {
		t.Skipf("git does not warn of the rules here (a user who reads every file): %v %q", err, stderr.String())
	}
	tracked, ignored, err := TreeClasses(t.Context(), tr, ix, read.Commit, []string{"README", "new.txt"})
	if err != nil || !tracked["README"] || tracked["new.txt"] || len(ignored) != 0 {
		t.Fatalf("the classes: tracked %v, ignored %v, %v", tracked, ignored, err)
	}
}
