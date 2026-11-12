package gitrepo

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-git/go-git/v5/config"
)

// ErrNoRemote says the repository's configuration names no such remote,
// or names it with no URL.
var ErrNoRemote = errors.New("no such remote in the repository")

// ErrUnread says the folder's git data is not what git keeps there: a
// file Remote needs is not a regular file (a pipe, a device, a directory)
// or is past its bound, the repository is past the walk's, or the
// configuration does not parse. It quotes nothing of it.
var ErrUnread = errors.New("the repository's configuration could not be read")

// The bounds of Remote's reads: a pointer file (a .git file, a linked
// worktree's commondir), the configuration, and the folders walked up to
// the repository.
const (
	maxPointer = 4 << 10
	maxConfig  = 1 << 20
	maxWalk    = 64
)

// Remote is the host and the repository's path (owner/name, as the
// address has it, .git included) of the first URL of the remote called
// name in the configuration of the repository dir is in (a linked
// worktree's is its common directory's), read only in a form address
// knows: no user, password, scheme or ssh port. The folder's files are
// read by Remote alone, as git finds them (dir and each folder above it,
// at most maxWalk, for a .git directory or a .git file naming one), each
// opened without waiting, held to a regular file and read to its bound;
// anything else is ErrUnread. The configuration's
// bytes are parsed by go-git's decoder, its url.<base>.insteadOf rules
// applied; no git process is started, so none of the repository's own
// programs (a credential helper, an ssh command, a hook) runs. No
// repository is ErrNotRepo, no such remote ErrNoRemote. The caller
// resolves dir as every folder a route opens is (resolveCwd).
func Remote(ctx context.Context, dir, name string) (host, path string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	common, err := commonDir(filepath.Clean(dir))
	if err != nil {
		return "", "", err
	}
	b, err := readBounded(filepath.Join(common, "config"), maxConfig)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", ErrNoRemote
	}
	if err != nil {
		return "", "", err
	}
	cfg := config.NewConfig()
	if cfg.Unmarshal(b) != nil {
		return "", "", ErrUnread
	}
	rc, ok := cfg.Remotes[name]
	if !ok || name == "" || len(rc.URLs) == 0 || rc.URLs[0] == "" {
		return "", "", ErrNoRemote
	}
	return address(rc.URLs[0])
}

// commonDir is the git directory whose configuration dir's repository
// keeps: the first .git up from dir (a directory, or a file "gitdir:
// <path>"), then the common directory a linked worktree's commondir names.
func commonDir(dir string) (string, error) {
	for range maxWalk {
		dot := filepath.Join(dir, ".git")
		fi, err := os.Stat(dot)
		switch {
		case err == nil && fi.IsDir():
			return common(dot)
		case err == nil:
			b, err := readBounded(dot, maxPointer)
			gitdir, ok := strings.CutPrefix(string(b), "gitdir: ")
			if err != nil || !ok {
				return "", ErrUnread
			}
			return common(within(dir, strings.TrimSpace(strings.SplitN(gitdir, "\n", 2)[0])))
		case !errors.Is(err, os.ErrNotExist):
			return "", ErrUnread
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", ErrNotRepo
		}
		dir = up
	}
	return "", ErrUnread
}

// common is the common directory of the git directory gitdir: the one its
// commondir names, else gitdir itself.
func common(gitdir string) (string, error) {
	b, err := readBounded(filepath.Join(gitdir, "commondir"), maxPointer)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return gitdir, nil
	case err != nil:
		return "", err
	}
	return within(gitdir, strings.TrimSpace(string(b))), nil
}

// within is p, a pointer file's path, as git reads it: absolute, or
// relative to base.
func within(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

// readBounded reads the regular file at path (a link followed, as git
// follows it), opened without waiting, at most n bytes: os.ErrNotExist
// when there is none, ErrUnread for anything else or past n.
func readBounded(path string, n int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrUnread
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, ErrUnread
	}
	b, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil || int64(len(b)) > n {
		return nil, ErrUnread
	}
	return b, nil
}

// ErrRemoteAddress says a remote's URL is in no form Remote answers for;
// it quotes nothing of it.
var ErrRemoteAddress = errors.New("the remote's URL is not an address in a form Conductor reads")

// address is the host and the path of a remote's URL in a form it knows,
// each taken from the parts it parsed: https://host[:port]/path,
// http://host[:port]/path (the port kept with the host: it names the web
// server), git://host[:port]/path, ssh://[user@]host[:port]/path and the
// scp-like [user@]host:path (the user and the port dropped: they name the
// transport, not the repository). Anything else (a remote helper's form, a
// second scheme, a local path, a bundle, a query or a fragment, a part
// outside its form: a host or a user that begins with '-', which ssh would
// read as an option, a path with a "." or ".." segment, a one-letter host
// of the scp-like form, which git on Windows reads as a drive) is
// ErrRemoteAddress.
func address(raw string) (host, path string, err error) {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return scpAddress(raw)
	}
	switch scheme {
	case "https", "http", "git", "ssh":
	default:
		return "", "", ErrRemoteAddress
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!hostInForm(u.Hostname()) || !inForm(u.Port(), digit) && u.Port() != "" || !strings.HasPrefix(u.Path, "/") || !pathInForm(u.Path) ||
		scheme == "ssh" && u.User != nil && !userInForm(u.User.Username()) {
		return "", "", ErrRemoteAddress
	}
	host = u.Hostname()
	if u.Port() != "" && (scheme == "https" || scheme == "http") {
		host += ":" + u.Port()
	}
	return host, strings.TrimPrefix(u.Path, "/"), nil
}

// scpAddress is the scp-like [user@]host:path's host and path: a colon
// before any slash, as git reads it.
func scpAddress(raw string) (host, path string, err error) {
	i := strings.IndexByte(raw, ':')
	if i < 0 || strings.IndexByte(raw[:i], '/') >= 0 {
		return "", "", ErrRemoteAddress
	}
	user, host, named := strings.Cut(raw[:i], "@")
	if !named {
		user, host = "", user
	}
	if named && !userInForm(user) || !hostInForm(host) || len(host) == 1 || !pathInForm(raw[i+1:]) {
		return "", "", ErrRemoteAddress
	}
	return host, raw[i+1:], nil
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
