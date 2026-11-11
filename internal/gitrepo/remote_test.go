package gitrepo

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

// A named remote, a sub-folder's and a linked worktree's (read from the
// common directory) are read from the configuration, each answered only in
// a form RemoteURL knows, rebuilt from its parts; any other form (the
// origin here, a local path) is no answer; a remote not named, and a
// folder that is no repository, say so. The repository is armed with a
// program for every configuration key and hook git may run (a credential
// helper, an ssh command, the remote's upload-pack among them), and none
// of them ever runs: no git process is started.
func TestRemoteURL(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	gitclitest.Git(t, r.Dir, "remote", "add", "upstream", "git@github.com:octo/conductor.git")
	gitclitest.Git(t, r.Dir, "remote", "set-url", "--add", "upstream", "https://github.com/octo/other.git")
	wt := filepath.Join(filepath.Dir(r.Dir), "linked")
	gitclitest.Git(t, r.Dir, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-q", "--no-checkout", wt)
	// Remotes whose URLs hold user information: made-up values, never a
	// real token.
	const pass, tok = "PLANTEDremotePASS0123", "ghp_PLANTEDremoteTOKEN0123456789abcdWXYZ"
	held := map[string]struct{ set, want string }{
		"withpass":   {"https://mona:" + pass + "@github.com/octo/a.git", "https://github.com/octo/a.git"},
		"withtoken":  {"https://" + tok + "@github.com/octo/b.git", "https://github.com/octo/b.git"},
		"tokenpass":  {"https://x-access-token:" + tok + "@ghe.example.com/octo/c.git", "https://ghe.example.com/octo/c.git"},
		"httppass":   {"http://mona:" + pass + "@ghe.example.com:8080/octo/c.git", "http://ghe.example.com:8080/octo/c.git"},
		"httptoken":  {"http://" + tok + "@ghe.example.com/octo/c.git", "http://ghe.example.com/octo/c.git"},
		"gitpass":    {"git://mona:" + pass + "@github.com:9418/octo/g.git", "git://github.com:9418/octo/g.git"},
		"gittoken":   {"git://" + tok + "@github.com/octo/g.git", "git://github.com/octo/g.git"},
		"sshpass":    {"ssh://git:" + pass + "@github.com/octo/d.git", "ssh://git@github.com/octo/d.git"},
		"sshport":    {"ssh://git:" + pass + "@github.com:2222/octo/d.git", "ssh://git@github.com:2222/octo/d.git"},
		"scp":        {"git@github.com:octo/e.git", "git@github.com:octo/e.git"},
		"scpnouser":  {"github.com:octo/e.git", "github.com:octo/e.git"},
		"rewritten":  {"https://rewrite.example.com/octo/f.git", "https://github.com/octo/f.git"},
		"rewritten2": {"git@rewrite2.example.com:octo/f.git", "https://ghe.example.com/octo/f.git"},
	}
	// Forms RemoteURL does not know: a remote helper's (the helper's
	// address holding a password or a token), a second scheme, a password
	// where git reads a path, a query, a local path, a bundle, and an
	// address an insteadOf rule turns into a helper's form.
	unknown := map[string]string{
		"helper":     "https::https://mona:" + pass + "@github.com/octo/r.git",
		"helpertok":  "https::https://" + tok + "@github.com/octo/r.git",
		"helperhttp": "http::http://x-access-token:" + tok + "@ghe.example.com/octo/r.git",
		"helperbare": "https::" + tok + "@github.com/octo/r.git",
		"twoschemes": "git+ssh://git:" + pass + "@github.com/octo/r.git",
		"scppass":    "mona:" + pass + "@github.com:octo/r.git",
		"query":      "https://github.com/octo/r.git?access_token=" + tok,
		"fragment":   "https://github.com/octo/r.git#" + tok,
		"file":       "file:///srv/" + pass + "/r.git",
		"path":       "/srv/" + pass + "/r.git",
		"bundle":     "/srv/r.bundle",
		"rewhelper":  "https://rewrite3.example.com/octo/r.git",
	}
	for name, c := range held {
		gitclitest.Git(t, r.Dir, "remote", "add", name, c.set)
	}
	for name, u := range unknown {
		gitclitest.Git(t, r.Dir, "remote", "add", name, u)
	}
	gitclitest.Git(t, r.Dir, "remote", "add", "unreadable", "https://mona:"+pass+"@github.com:port/x.git")
	// The rewrite rules git keeps (url.<base>.insteadOf), applied as the
	// configuration is read: to an address holding a token, to another
	// form, and to a remote helper's.
	gitclitest.Git(t, r.Dir, "config", "url.https://x-access-token:"+tok+"@github.com/.insteadOf", "https://rewrite.example.com/")
	gitclitest.Git(t, r.Dir, "config", "url.https://mona:"+pass+"@ghe.example.com/.insteadOf", "git@rewrite2.example.com:")
	gitclitest.Git(t, r.Dir, "config", "url.https::https://mona:"+pass+"@github.com/.insteadOf", "https://rewrite3.example.com/")
	// What the setup's own git ran is not the reads': its marks go.
	if err := os.RemoveAll(r.Marks); err != nil || os.Mkdir(r.Marks, 0o755) != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, name, want string }{
		{r.Dir, "upstream", "git@github.com:octo/conductor.git"},
		{filepath.Join(r.Dir, ".git"), "upstream", "git@github.com:octo/conductor.git"},
		{wt, "upstream", "git@github.com:octo/conductor.git"},
	} {
		got, err := RemoteURL(t.Context(), c.dir, c.name)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q %v, not %q", c.dir, c.name, got, err, c.want)
		}
	}
	// Each comes back rebuilt from its parts, without the user information
	// git may keep in it (an ssh URL keeps the user's name).
	for name, c := range held {
		got, err := RemoteURL(t.Context(), r.Dir, name)
		if err != nil || got != c.want || strings.Contains(got, pass) || strings.Contains(got, tok) {
			t.Errorf("%s: %q %v, not %q", name, got, err, c.want)
		}
	}
	// A form RemoteURL does not know is no answer, quoting nothing: the
	// origin, a local path, among them.
	for _, name := range append([]string{"unreadable", "origin"}, slices.Sorted(maps.Keys(unknown))...) {
		if got, err := RemoteURL(t.Context(), r.Dir, name); !errors.Is(err, ErrRemoteAddress) || got != "" || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), tok) {
			t.Errorf("%s: %q %v", name, got, err)
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
