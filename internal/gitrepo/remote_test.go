package gitrepo

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

// A named remote, a sub-folder's and a linked worktree's (read from the
// common directory) are read from the configuration, each answered only as
// the host and the path of a form Remote knows; any other form (the origin
// here, a local path) is no answer; a remote not named, and a folder that
// is no repository, say so. The repository is armed with a program for
// every configuration key and hook git may run (a credential helper, an
// ssh command, the remote's upload-pack among them), and none of them ever
// runs: no git process is started.
func TestRemote(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	gitclitest.Git(t, r.Dir, "remote", "add", "upstream", "git@github.com:octo/conductor.git")
	gitclitest.Git(t, r.Dir, "remote", "set-url", "--add", "upstream", "https://github.com/octo/other.git")
	wt := filepath.Join(filepath.Dir(r.Dir), "linked")
	gitclitest.Git(t, r.Dir, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-q", "--no-checkout", wt)
	// Remotes whose URLs hold user information: made-up values, never a
	// real token.
	const pass, tok = "PLANTEDremotePASS0123", "ghp_PLANTEDremoteTOKEN0123456789abcdWXYZ"
	held := map[string]struct{ set, want string }{
		"withpass":   {"https://mona:" + pass + "@github.com/octo/a.git", "github.com octo/a.git"},
		"withtoken":  {"https://" + tok + "@github.com/octo/b.git", "github.com octo/b.git"},
		"tokenpass":  {"https://x-access-token:" + tok + "@ghe.example.com/octo/c.git", "ghe.example.com octo/c.git"},
		"httppass":   {"http://mona:" + pass + "@ghe.example.com:8080/octo/c.git", "ghe.example.com:8080 octo/c.git"},
		"httptoken":  {"http://" + tok + "@ghe.example.com/octo/c.git", "ghe.example.com octo/c.git"},
		"gitpass":    {"git://mona:" + pass + "@github.com:9418/octo/g.git", "github.com octo/g.git"},
		"gittoken":   {"git://" + tok + "@github.com/octo/g.git", "github.com octo/g.git"},
		"sshpass":    {"ssh://git:" + pass + "@github.com/octo/d.git", "github.com octo/d.git"},
		"sshport":    {"ssh://git:" + pass + "@github.com:2222/octo/d.git", "github.com octo/d.git"},
		"sshsecret":  {"ssh://" + tok + "@github.com/octo/d.git", "github.com octo/d.git"},
		"scp":        {"git@github.com:octo/e.git", "github.com octo/e.git"},
		"scpsecret":  {tok + "@github.com:octo/e.git", "github.com octo/e.git"},
		"scpnouser":  {"github.com:octo/e.git", "github.com octo/e.git"},
		"rewritten":  {"https://rewrite.example.com/octo/f.git", "github.com octo/f.git"},
		"rewritten2": {"git@rewrite2.example.com:octo/f.git", "ghe.example.com octo/f.git"},
	}
	// Forms Remote does not know: a remote helper's (the helper's address
	// holding a password or a token), a second scheme, a password where git
	// reads a path, a query, a local path, a bundle, and an address an
	// insteadOf rule turns into a helper's form.
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
	remote := func(dir, name string) (string, error) {
		host, path, err := Remote(t.Context(), dir, name)
		return strings.TrimSpace(host + " " + path), err
	}
	sub := filepath.Join(r.Dir, "sub", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, name, want string }{
		{r.Dir, "upstream", "github.com octo/conductor.git"},
		{sub, "upstream", "github.com octo/conductor.git"},
		{filepath.Join(r.Dir, ".git"), "upstream", "github.com octo/conductor.git"},
		{wt, "upstream", "github.com octo/conductor.git"},
	} {
		got, err := remote(c.dir, c.name)
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q %v, not %q", c.dir, c.name, got, err, c.want)
		}
	}
	// Each comes back as its host and path alone: no user, password,
	// scheme or ssh port.
	for name, c := range held {
		got, err := remote(r.Dir, name)
		if err != nil || got != c.want || strings.Contains(got, pass) || strings.Contains(got, tok) {
			t.Errorf("%s: %q %v, not %q", name, got, err, c.want)
		}
	}
	// A form Remote does not know is no answer, quoting nothing: the
	// origin, a local path, among them.
	for _, name := range append([]string{"unreadable", "origin"}, slices.Sorted(maps.Keys(unknown))...) {
		if got, err := remote(r.Dir, name); !errors.Is(err, ErrRemoteAddress) || got != "" || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), tok) {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	for _, name := range []string{"", "nope"} {
		if _, err := remote(r.Dir, name); !errors.Is(err, ErrNoRemote) {
			t.Errorf("remote %q: %v", name, err)
		}
	}
	if _, err := remote(t.TempDir(), "origin"); !errors.Is(err, ErrNotRepo) {
		t.Errorf("no repository: %v", err)
	}
	r.NoneFired(t, "reading the remotes")
}

// A folder's git data that is not what git keeps there is not read, and
// the read returns: the configuration, the .git file of a linked worktree
// and its commondir each made a named pipe, a link to /dev/zero, a
// directory, and a regular file one byte past its bound, are ErrUnread;
// a link to a regular file is read as git reads it. Everything is made
// under the test's own folder. The test's own wait only bounds a hang.
func TestRemoteUnreadable(t *testing.T) {
	const origin = "[remote \"origin\"]\n\turl = git@github.com:octo/app.git\n"
	// repo is a repository at base/repo with the configuration origin, and
	// a linked worktree of it at base/wt: its .git file and its commondir.
	repo := func(t *testing.T) (main, wt, config, dotgit, commondir string) {
		base := t.TempDir()
		main, wt = filepath.Join(base, "repo"), filepath.Join(base, "wt")
		gitdir := filepath.Join(main, ".git", "worktrees", "wt")
		for _, d := range []string{filepath.Join(main, ".git", "objects"), filepath.Join(main, ".git", "refs", "heads"), gitdir, wt} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		config, dotgit, commondir = filepath.Join(main, ".git", "config"), filepath.Join(wt, ".git"), filepath.Join(gitdir, "commondir")
		for p, b := range map[string]string{filepath.Join(main, ".git", "HEAD"): "ref: refs/heads/main\n", config: origin,
			dotgit: "gitdir: " + gitdir + "\n", commondir: "../..\n", filepath.Join(gitdir, "HEAD"): "ref: refs/heads/wt\n"} {
			if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return main, wt, config, dotgit, commondir
	}
	main, wt, _, _, _ := repo(t)
	for _, dir := range []string{main, wt} {
		if host, path, err := call(t, dir, nil); err != nil || host != "github.com" || path != "octo/app.git" {
			t.Fatalf("%s as git keeps it: %q %q %v", dir, host, path, err)
		}
	}
	replace := map[string]func(t *testing.T, p string){
		"a named pipe": func(t *testing.T, p string) {
			if err := syscall.Mkfifo(p, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a link to /dev/zero": func(t *testing.T, p string) {
			if err := os.Symlink("/dev/zero", p); err != nil {
				t.Fatal(err)
			}
		},
		"a directory": func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"past its bound": func(t *testing.T, p string) {
			n := maxPointer
			if filepath.Base(p) == "config" {
				n = maxConfig
			}
			// A valid start, then one byte more than the bound in all.
			start := origin
			if filepath.Base(p) != "config" {
				start = "../.."
				if filepath.Base(p) == ".git" {
					start = "gitdir: " + filepath.Join(filepath.Dir(filepath.Dir(p)), "repo", ".git", "worktrees", "wt")
				}
			}
			if err := os.WriteFile(p, []byte(start+strings.Repeat("\n", n+1-len(start))), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for what, put := range replace {
		for _, at := range []string{"config", ".git", "commondir"} {
			t.Run(at+" "+what, func(t *testing.T) {
				main, wt, config, dotgit, commondir := repo(t)
				p, dir := map[string]string{"config": config, ".git": dotgit, "commondir": commondir}[at], wt
				if at == "config" {
					dir = main
				}
				if what == "a directory" && at == ".git" {
					t.Skip("a .git directory is a repository's own")
				}
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				put(t, p)
				if _, _, err := call(t, dir, func() { release(p) }); !errors.Is(err, ErrUnread) {
					t.Fatalf("%s as %s: %v, not ErrUnread", at, what, err)
				}
			})
		}
	}
	// A link to a regular file is read as git reads it.
	main, _, config, _, _ := repo(t)
	elsewhere := filepath.Join(t.TempDir(), "config")
	if err := os.Rename(config, elsewhere); err != nil || os.Symlink(elsewhere, config) != nil {
		t.Fatal(err)
	}
	if host, path, err := call(t, main, nil); err != nil || host != "github.com" || path != "octo/app.git" {
		t.Fatalf("a linked configuration: %q %q %v", host, path, err)
	}
}

// call is Remote(dir, "origin") with the test's own wait above it, which
// only bounds a hang: past it, unhold (when given) lets a held read go,
// and the test fails.
func call(t *testing.T, dir string, unhold func()) (host, path string, err error) {
	t.Helper()
	type answer struct {
		host, path string
		err        error
	}
	done := make(chan answer, 1)
	go func() {
		h, p, e := Remote(context.Background(), dir, "origin")
		done <- answer{h, p, e}
	}()
	select {
	case a := <-done:
		return a.host, a.path, a.err
	case <-time.After(30 * time.Second):
	}
	for unhold != nil {
		unhold()
		select {
		case <-done:
			unhold = nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("the read of %s is held", dir)
	return "", "", nil
}

// release opens a named pipe for writing without waiting, which lets a
// reader held in its open go.
func release(p string) {
	if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		f.Close()
	}
}

// Every address form git takes that the table can name: each is answered
// as its host and path (want, "host path"), or refused with
// ErrRemoteAddress (want ""), quoting nothing of what it was given. The
// made-up password and token never come back, also not as an ssh or scp
// user's name, which is dropped.
func TestRemoteAddressForms(t *testing.T) {
	const pass, tok = "PLANTEDremotePASS0123", "ghp_PLANTEDremoteTOKEN0123456789abcdWXYZ"
	for _, c := range []struct{ raw, want string }{
		// The scp-like form, with and without a user, .git and none, a
		// trailing slash, a home-relative path, a token as the user.
		{"git@github.com:octo/r.git", "github.com octo/r.git"},
		{"github.com:octo/r.git", "github.com octo/r.git"},
		{"git@github.com:octo/r", "github.com octo/r"},
		{"git@github.com:octo/r/", "github.com octo/r/"},
		{"git@github.com:~octo/r.git", "github.com ~octo/r.git"},
		{tok + "@github.com:octo/r.git", "github.com octo/r.git"},
		// https and http with a port, a user, a password, a token as the user.
		{"https://github.com/octo/r.git", "github.com octo/r.git"},
		{"https://github.com/octo/r", "github.com octo/r"},
		{"https://github.com/octo/r/", "github.com octo/r/"},
		{"https://github.com:8443/octo/r.git", "github.com:8443 octo/r.git"},
		{"https://github.com:/octo/r.git", "github.com octo/r.git"},
		{"https://mona@github.com/octo/r.git", "github.com octo/r.git"},
		{"https://mona:" + pass + "@github.com/octo/r.git", "github.com octo/r.git"},
		{"https://" + tok + "@github.com/octo/r.git", "github.com octo/r.git"},
		{"https://" + tok + ":x-oauth-basic@github.com/octo/r.git", "github.com octo/r.git"},
		{"http://ghe.example.com:8080/octo/r.git", "ghe.example.com:8080 octo/r.git"},
		{"http://mona:" + pass + "@ghe.example.com/octo/r.git", "ghe.example.com octo/r.git"},
		// An escaped byte inside the forms is the path it names.
		{"https://github.com/octo/r%2Egit", "github.com octo/r.git"},
		// ssh with and without a user, with a port, a password, a token as
		// the user: the host and the path alone.
		{"ssh://git@github.com/octo/r.git", "github.com octo/r.git"},
		{"ssh://github.com/octo/r.git", "github.com octo/r.git"},
		{"ssh://git@github.com:2222/octo/r.git", "github.com octo/r.git"},
		{"ssh://git:" + pass + "@github.com/octo/r.git", "github.com octo/r.git"},
		{"ssh://" + tok + "@github.com/octo/r.git", "github.com octo/r.git"},
		{"git://github.com/octo/r.git", "github.com octo/r.git"},
		{"git://" + tok + "@github.com:9418/octo/r.git", "github.com octo/r.git"},

		// A query, a fragment, an empty query.
		{"https://github.com/octo/r.git?access_token=" + tok, ""},
		{"https://github.com/octo/r.git#" + tok, ""},
		{"https://github.com/octo/r.git?", ""},
		{"git@github.com:octo/r.git?x=" + tok, ""},
		// A "." or ".." segment, written or escaped.
		{"https://github.com/octo/../r.git", ""},
		{"https://github.com/octo/./r.git", ""},
		{"https://github.com/octo/%2e%2e/r.git", ""},
		{"git@github.com:octo/../r.git", ""},
		{"git@github.com:../r.git", ""},
		// Local paths, a file URL, a bundle, a Windows drive.
		{"/srv/" + pass + "/r.git", ""},
		{"./r.git", ""},
		{"../r.git", ""},
		{"r.git", ""},
		{"~/r.git", ""},
		{"file:///srv/r.git", ""},
		{"/srv/r.bundle", ""},
		{"C:/repos/r.git", ""},
		{`C:\repos\r.git`, ""},
		// A remote helper's form, a second scheme, another scheme, a scheme
		// in upper case.
		{"https::https://mona:" + pass + "@github.com/octo/r.git", ""},
		{"ext::ssh -oProxyCommand=x github.com", ""},
		{"git+ssh://git@github.com/octo/r.git", ""},
		{"ssh+git://git@github.com/octo/r.git", ""},
		{"ftp://github.com/octo/r.git", ""},
		{"HTTPS://github.com/octo/r.git", ""},
		// A host or a user that ssh would read as an option.
		{"ssh://-oProxyCommand/r.git", ""},
		{"-oProxyCommand:r.git", ""},
		{"ssh://-u@github.com/octo/r.git", ""},
		{"-u@github.com:octo/r.git", ""},
		// A password where git reads a host, a port not a number, a bracketed
		// address, a host past ASCII, no host, no path.
		{"mona:" + pass + "@github.com:octo/r.git", ""},
		{"https://github.com:port/r.git", ""},
		{"https://[::1]/octo/r.git", ""},
		{"https://gïthub.com/octo/r.git", ""},
		{"https://", ""},
		{"https://github.com", ""},
		{"git@github.com:", ""},
		// Control characters and line breaks anywhere.
		{"https://github.com/octo/r.git\n", ""},
		{"https://github.com/octo/r.git\r\n[core]", ""},
		{"https://github.com/oc\tto/r.git", ""},
		{"https://github.com/octo\x00/r.git", ""},
		{"git@github.com:octo/r.git\n", ""},
		{"git@git\nhub.com:octo/r.git", ""},
		// Bytes outside a path's form: a colon, a space, an at sign, a
		// percent sign that escapes nothing in it.
		{"git@github.com:octo/r.git:extra", ""},
		{"https://github.com/octo/my r.git", ""},
		{"https://github.com/octo/r@x.git", ""},
		{"", ""},
	} {
		host, path, err := address(c.raw)
		got := strings.TrimSpace(host + " " + path)
		if c.want == "" {
			if !errors.Is(err, ErrRemoteAddress) || got != "" || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), tok) {
				t.Errorf("%q: %q %v, not refused", c.raw, got, err)
			}
			continue
		}
		if err != nil || got != c.want || strings.Contains(got, pass) || strings.Contains(got, tok) {
			t.Errorf("%q: %q %v, not %q", c.raw, got, err, c.want)
		}
	}
}
