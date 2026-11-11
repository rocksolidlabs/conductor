package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	// Remotes whose URLs hold user information: made-up values, never a
	// real token.
	const pass, tok = "PLANTEDremotePASS0123", "ghp_PLANTEDremoteTOKEN0123456789abcdWXYZ"
	held := map[string]struct{ set, want string }{
		"withpass":  {"https://mona:" + pass + "@github.com/octo/a.git", "https://github.com/octo/a.git"},
		"withtoken": {"https://" + tok + "@github.com/octo/b.git", "https://github.com/octo/b.git"},
		"tokenpass": {"https://x-access-token:" + tok + "@ghe.example.com/octo/c.git", "https://ghe.example.com/octo/c.git"},
		"sshpass":   {"ssh://git:" + pass + "@github.com/octo/d.git", "ssh://git@github.com/octo/d.git"},
		"scp":       {"git@github.com:octo/e.git", "git@github.com:octo/e.git"},
	}
	for name, c := range held {
		gitclitest.Git(t, r.Dir, "remote", "add", name, c.set)
	}
	gitclitest.Git(t, r.Dir, "remote", "add", "unreadable", "https://mona:"+pass+"@github.com:port/x.git")
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
	// Each comes back as its address, without the user information git
	// may keep in it (an ssh URL keeps the user's name).
	for name, c := range held {
		got, err := RemoteURL(t.Context(), r.Dir, name)
		if err != nil || got != c.want || strings.Contains(got, pass) || strings.Contains(got, tok) {
			t.Errorf("%s: %q %v, not %q", name, got, err, c.want)
		}
	}
	if got, err := RemoteURL(t.Context(), r.Dir, "unreadable"); err == nil || strings.Contains(got+err.Error(), pass) {
		t.Errorf("an unreadable address: %q %v", got, err)
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
