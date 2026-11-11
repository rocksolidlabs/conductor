package gitrepo

import (
	"context"
	"errors"

	"github.com/go-git/go-git/v5"
)

// ErrNoRemote says the repository's configuration names no such remote,
// or names it with no URL.
var ErrNoRemote = errors.New("no such remote in the repository")

// RemoteURL is the first URL of the remote called name in the
// configuration of the repository dir is in (a linked worktree's is its
// common directory's), read by go-git: no git process is started, no
// transport opened and nothing fetched, so none of the repository's own
// programs (a credential helper, an ssh command, a hook) runs. The
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
	return rc.URLs[0], nil
}
