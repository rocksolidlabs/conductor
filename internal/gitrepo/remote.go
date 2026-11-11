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
// common directory's), as its address, without the user information git
// may keep in it (address). It is read by go-git: no git process is
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

// ErrRemoteAddress says a remote's URL could not be read as an address;
// it quotes nothing of it.
var ErrRemoteAddress = errors.New("the remote's URL cannot be read as an address")

// address is a remote's URL as an address, without the user information
// git may keep in it: an http or https URL loses it whole, an ssh URL keeps
// the user's name alone. The scp-like form (user@host:path) holds a
// user's name at most and stays as it is, and so does a path.
func address(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrRemoteAddress
	}
	if u.User == nil {
		return raw, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "ssh", "git+ssh", "ssh+git":
		u.User = url.User(u.User.Username())
	default:
		u.User = nil
	}
	return u.String(), nil
}
