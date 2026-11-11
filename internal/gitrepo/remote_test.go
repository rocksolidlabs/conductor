package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

// A repository's origin, a named remote, a sub-folder's and a linked
// worktree's (read from the common directory) are read from the
// configuration; a remote not named, and a folder that is no repository,
// say so. The repository is armed with a program for every configuration
// key and hook git may run (a credential helper, an ssh command, the
// remote's upload-pack among them), and none of them ever runs: no git
// process is started.
func TestRemoteURL(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	origin := filepath.Join(filepath.Dir(r.Dir), "remote")
	gitclitest.Git(t, r.Dir, "remote", "add", "upstream", "git@github.com:octo/conductor.git")
	gitclitest.Git(t, r.Dir, "remote", "set-url", "--add", "upstream", "https://github.com/octo/other.git")
	wt := filepath.Join(filepath.Dir(r.Dir), "linked")
	gitclitest.Git(t, r.Dir, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-q", "--no-checkout", wt)
	// What the setup's own git ran is not the reads': its marks go.
	if err := os.RemoveAll(r.Marks); err != nil || os.Mkdir(r.Marks, 0o755) != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, name, want string }{
		{r.Dir, "origin", origin},
		{filepath.Join(r.Dir, ".git"), "origin", origin},
		{r.Dir, "upstream", "git@github.com:octo/conductor.git"},
		{wt, "origin", origin},
		{wt, "upstream", "git@github.com:octo/conductor.git"},
	} {
		got, err := RemoteURL(t.Context(), c.dir, c.name)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q %v, not %q", c.dir, c.name, got, err, c.want)
		}
	}
	for _, name := range []string{"", "nope"} {
		if _, err := RemoteURL(t.Context(), r.Dir, name); !errors.Is(err, ErrNoRemote) {
			t.Errorf("remote %q: %v", name, err)
		}
	}
	if _, err := RemoteURL(t.Context(), t.TempDir(), "origin"); !errors.Is(err, ErrNotRepo) {
		t.Errorf("no repository: %v", err)
	}
	r.NoneFired(t, "reading the remotes")
}
