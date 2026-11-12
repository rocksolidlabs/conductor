package gitrepo

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5"
)

// ErrNoRemote says the repository's configuration names no such remote,
// or names it with no URL.
var ErrNoRemote = errors.New("no such remote in the repository")

// RemoteURL is the first URL of the remote called name in the
// configuration of the repository dir is in (a linked worktree's is its
// common directory's), answered only in a form it knows, rebuilt from its
// parts (address). It is read by go-git: no git process is
// started, no transport opened and nothing fetched, so none of the
// repository's own programs (a credential helper, an ssh command, a hook)
// runs. The
// caller resolves dir as every folder a route opens is (resolveCwd).
func RemoteURL(ctx context.Context, dir, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return "", ErrNotRepo
		}
		return "", err
	}
	cfg, err := r.Config()
	if err != nil {
		return "", err
	}
	rc, ok := cfg.Remotes[name]
	if !ok || name == "" || len(rc.URLs) == 0 || rc.URLs[0] == "" {
		return "", ErrNoRemote
	}
	return address(rc.URLs[0])
}

// ErrRemoteAddress says a remote's URL is in no form RemoteURL answers
// for; it quotes nothing of it.
var ErrRemoteAddress = errors.New("the remote's URL is not an address in a form Conductor reads")

// address is a remote's URL in a form it knows, built from the parts it
// parsed, never the text it was given with something cut out:
// https://host[:port]/path, http://host[:port]/path,
// git://host[:port]/path, ssh://[user@]host[:port]/path (an ssh user's
// name alone kept) and the scp-like [user@]host:path. Anything else (a
// remote helper's form, a second scheme, a local path, a bundle, a query
// or a fragment, a part outside its form: a host or a user that begins
// with '-', which ssh would read as an option, a path with a "." or ".."
// segment, a one-letter host of the scp-like form, which git on Windows
// reads as a drive) is ErrRemoteAddress.
func address(raw string) (string, error) {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return scpAddress(raw)
	}
	switch scheme {
	case "https", "http", "git", "ssh":
	default:
		return "", ErrRemoteAddress
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!hostInForm(u.Hostname()) || !inForm(u.Port(), digit) && u.Port() != "" || !strings.HasPrefix(u.Path, "/") || !pathInForm(u.Path) {
		return "", ErrRemoteAddress
	}
	out := url.URL{Scheme: scheme, Host: u.Hostname(), Path: u.Path}
	if u.Port() != "" {
		out.Host += ":" + u.Port()
	}
	if scheme == "ssh" && u.User != nil {
		if !userInForm(u.User.Username()) {
			return "", ErrRemoteAddress
		}
		out.User = url.User(u.User.Username())
	}
	return out.String(), nil
}

// scpAddress is the scp-like [user@]host:path, rebuilt from its parts: a
// colon before any slash, as git reads it.
func scpAddress(raw string) (string, error) {
	i := strings.IndexByte(raw, ':')
	if i < 0 || strings.IndexByte(raw[:i], '/') >= 0 {
		return "", ErrRemoteAddress
	}
	user, host, named := strings.Cut(raw[:i], "@")
	if !named {
		user, host = "", user
	}
	if named && !userInForm(user) || !hostInForm(host) || len(host) == 1 || !pathInForm(raw[i+1:]) {
		return "", ErrRemoteAddress
	}
	if named {
		host = user + "@" + host
	}
	return host + ":" + raw[i+1:], nil
}

// The forms of an address's parts: a host's name, a port's digits, a
// user's name and a repository's path.
func hostByte(c byte) bool { return c == '.' || c == '-' || alnum(c) }
func digit(c byte) bool    { return c >= '0' && c <= '9' }
func userByte(c byte) bool { return c == '.' || c == '-' || c == '_' || alnum(c) }
func pathByte(c byte) bool { return c == '/' || c == '~' || userByte(c) }
func alnum(c byte) bool    { return digit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// hostInForm and userInForm: the part's form, not beginning with '-';
// pathInForm: the path's form, with no "." or ".." segment.
func hostInForm(h string) bool { return inForm(h, hostByte) && h[0] != '-' }
func userInForm(u string) bool { return inForm(u, userByte) && u[0] != '-' }
func pathInForm(p string) bool {
	if !inForm(p, pathByte) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// inForm reports s is 1 to 1024 bytes, each in its part's form.
func inForm(s string, ok func(byte) bool) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}
