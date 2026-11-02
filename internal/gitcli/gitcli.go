// Package gitcli runs the git binary for the server and conductor host: the
// working directory's status with each file's added and removed lines, a
// file at a revision, and the helper the crew engine's worktrees use. Every
// git Conductor runs goes through Run (run.go): argv arrays in the C
// locale, nothing a shell string, and no program the repository's own
// configuration names. go-git (the round 12 decision) is for what it does
// without hashing the working tree, the log and a commit's diffs; status
// stays here.
package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrNotRepo is a directory outside any git working tree.
var ErrNotRepo = errors.New("not a git repository")

// ErrElsewhere is a directory whose working tree, as git reports it, is not
// its own (ownTree): a repository's core.worktree points it elsewhere.
var ErrElsewhere = errors.New("this repository's working tree is outside the folder (core.worktree): its changes are not listed here")

// Bounds of a status: how many changed files are listed, and how much of a
// file is read to count an untracked file's lines or to show a revision.
const (
	MaxChanges   = 500
	MaxShowBytes = 1 << 20
)

// Change is one changed file of a status: its path relative to the working
// tree's top, its status letter (M modified, A added, D deleted, R renamed,
// ? untracked), its added and removed lines against the base, and whether
// git counts it as binary.
type Change struct {
	Path    string
	Status  string
	Added   int
	Removed int
	Binary  bool
}

// Status is a working directory's git status against a base revision.
type Status struct {
	// Top is the working tree's top; Branch the branch checked out (HEAD
	// when detached); Base the base revision's short id, empty when the
	// base cannot be resolved (a repository without a commit yet).
	Top, Branch, Base string
	Changes           []Change
	// Added and Removed sum the changes listed.
	Added, Removed int
	// Truncated says the list stopped at MaxChanges.
	Truncated bool
}

// Message picks the line of git's standard error that says why it failed:
// the first "fatal:" or "error:" line, else the last line. Newer gits print
// progress first ("Preparing worktree (new branch …)"), so the first line
// is not it.
func Message(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	last := ""
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "fatal:") || strings.HasPrefix(l, "error:") {
			return l
		}
		last = l
	}
	return last
}

// GetStatus is dir's working tree status against base (HEAD when empty):
// every changed and untracked file with its status letter and its added and
// removed lines (an untracked file's lines are counted from the file itself,
// a binary one as none, a link as none), at most MaxChanges of them, paths
// relative to the tree's top. ErrNotRepo outside a repository, ErrElsewhere
// for a working tree that is not dir's own (ownTree); a base that does not
// resolve leaves Base empty and the counts at zero.
func GetStatus(ctx context.Context, dir, base string) (Status, error) {
	if base == "" {
		base = "HEAD"
	}
	if strings.HasPrefix(base, "-") {
		return Status{}, fmt.Errorf("git: invalid base %q", base)
	}
	top, err := TopOf(ctx, dir)
	if err != nil {
		return Status{}, err
	}
	st := Status{Top: top}
	if out, err := Run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		st.Branch = strings.TrimSpace(out)
	}
	if out, err := Run(ctx, dir, "rev-parse", "--short", "--verify", "--quiet", base+"^{commit}"); err == nil {
		st.Base = strings.TrimSpace(out)
	}
	raw, err := Run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	counts := map[string][2]int{}
	binary := map[string]bool{}
	if st.Base != "" {
		if out, err := Run(ctx, dir, "diff", "--numstat", "-z", "--no-renames", base, "--"); err == nil {
			for _, rec := range strings.Split(out, "\x00") {
				parts := strings.SplitN(rec, "\t", 3)
				if len(parts) != 3 {
					continue
				}
				if parts[0] == "-" {
					binary[parts[2]] = true
					continue
				}
				a, _ := strconv.Atoi(parts[0])
				r, _ := strconv.Atoi(parts[1])
				counts[parts[2]] = [2]int{a, r}
			}
		}
	}
	changes, truncated := parsePorcelain(raw)
	st.Truncated = truncated
	for _, c := range changes {
		c.Binary = binary[c.Path]
		if n, ok := counts[c.Path]; ok {
			c.Added, c.Removed = n[0], n[1]
		} else if c.Status == "?" || c.Status == "A" {
			c.Added, c.Binary = countLines(filepath.Join(st.Top, filepath.FromSlash(c.Path)))
		}
		st.Changes = append(st.Changes, c)
		st.Added += c.Added
		st.Removed += c.Removed
	}
	return st, nil
}

// ChangedSince is the paths of the files the commits of dir's HEAD since
// base changed (git diff --name-only -z base...HEAD: against the merge
// base of the two, so a base that moved on adds nothing), relative to the
// working tree's top, at most MaxChanges of them, truncated past that.
// What the working tree changed and has not committed is GetStatus's.
// ErrNotRepo outside a repository; a base that does not resolve is git's
// error.
func ChangedSince(ctx context.Context, dir, base string) (paths []string, truncated bool, err error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, false, fmt.Errorf("git: invalid base %q", base)
	}
	out, err := Run(ctx, dir, "diff", "--name-only", "-z", "--no-renames", base+"...HEAD", "--")
	if err != nil {
		return nil, false, err
	}
	for p := range strings.SplitSeq(out, "\x00") {
		if p == "" {
			continue
		}
		if len(paths) == MaxChanges {
			return paths, true, nil
		}
		paths = append(paths, p)
	}
	return paths, false, nil
}

// parsePorcelain reads `git status --porcelain=v1 -z` output: each changed
// or untracked file with its status letter (M, A, D, R, ?), at most
// MaxChanges of them, truncated past that.
func parsePorcelain(raw string) (changes []Change, truncated bool) {
	tokens := strings.Split(raw, "\x00")
	for i := 0; i < len(tokens); i++ {
		rec := tokens[i]
		if len(rec) < 4 {
			continue
		}
		x, y, path := rec[0], rec[1], rec[3:]
		if x == 'R' || x == 'C' {
			// The record the rename came from follows as its own entry.
			i++
		}
		status := "M"
		switch {
		case x == '?' || y == '?':
			status = "?"
		case x == 'D' || y == 'D':
			status = "D"
		case x == 'R' || y == 'R':
			status = "R"
		case x == 'A' || y == 'A':
			status = "A"
		}
		if len(changes) == MaxChanges {
			return changes, true
		}
		changes = append(changes, Change{Path: path, Status: status})
	}
	return changes, false
}

// Porcelain is dir's working tree status without the line counts: the
// tree's top and its changed and untracked files with their status letters
// (at most MaxChanges, truncated past that), paths relative to the top.
// Two git runs; ErrNotRepo outside a repository, ErrElsewhere for a working
// tree that is not dir's own. Files seen by git (the session's look after
// an agent's tool call) read it.
func Porcelain(ctx context.Context, dir string) (top string, changes []Change, truncated bool, err error) {
	t, err := TopOf(ctx, dir)
	if err != nil {
		return "", nil, false, err
	}
	raw, err := Run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", nil, false, err
	}
	changes, truncated = parsePorcelain(raw)
	return t, changes, truncated, nil
}

// TopOf is the top of dir's working tree as git reports it (rev-parse
// --show-toplevel), once it is dir's own (ownTree): ErrNotRepo outside a
// repository, ErrElsewhere when it is not.
func TopOf(ctx context.Context, dir string) (string, error) {
	out, err := Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(out)
	if !ownTree(dir, top) {
		return "", ErrElsewhere
	}
	return top, nil
}

// ownTree reports whether top, the working tree git reports for dir, is
// dir's own: dir itself or a directory inside it, or the nearest directory
// at or above dir that holds a .git (a directory, a file or a link), where
// git finds the working tree when no setting points it elsewhere, so that
// a folder inside a repository has the repository's. A working tree a
// repository's core.worktree puts anywhere else (beside the folder, above
// it but not where its .git is) is not: listing it would list files from
// outside the folder. Directories are compared as files (os.SameFile), so
// a link on the way or another spelling of one is the same directory.
func ownTree(dir, top string) bool {
	topID, err := os.Stat(top)
	if err != nil || !topID.IsDir() {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	dirID, err := os.Stat(real)
	if err != nil {
		return false
	}
	topReal, err := filepath.EvalSymlinks(top)
	if err != nil {
		return false
	}
	for p := topReal; ; p = filepath.Dir(p) {
		if fi, err := os.Stat(p); err == nil && os.SameFile(fi, dirID) {
			return true
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	for p := real; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			fi, err := os.Stat(p)
			return err == nil && os.SameFile(fi, topID)
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

// countLines counts a file's lines for an untracked file's added count, up
// to MaxShowBytes of it; a file with a NUL in its first bytes is binary and
// counts none, and so does anything but a regular file: a link is not
// followed to what it points to, which may be outside the working tree.
func countLines(path string) (int, bool) {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	buf := make([]byte, MaxShowBytes)
	n, _ := f.Read(buf)
	buf = buf[:n]
	head := buf
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, true
	}
	if n == 0 {
		return 0, false
	}
	lines := bytes.Count(buf, []byte{'\n'})
	if buf[n-1] != '\n' {
		lines++
	}
	return lines, false
}

// isObjectID reports whether s is an object id as rev-parse prints one: 40
// (SHA-1) or 64 (SHA-256) lowercase hex digits.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Show is the content of path (relative to the working tree's top) at rev,
// a revision that names one commit, at most MaxShowBytes of it (truncated
// says so); ErrNotRepo outside a repository, an error for a revision that
// is not a commit (a tree's id, a range) and for a path the commit does not
// have.
func Show(ctx context.Context, dir, rev, path string) (content []byte, truncated bool, err error) {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.HasPrefix(path, "-") || strings.Contains(rev, ":") {
		return nil, false, fmt.Errorf("git show: invalid revision or path")
	}
	// rev names one commit, and path is read from its top: a tree's id (a
	// folder of a commit) or a range would read path from elsewhere.
	commit, err := Run(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if errors.Is(err, ErrNotRepo) {
		return nil, false, err
	}
	commit = strings.TrimSpace(commit)
	if err != nil || !isObjectID(commit) {
		return nil, false, fmt.Errorf("git show: %q is not a commit", rev)
	}
	out, err := Run(ctx, dir, "show", commit+":"+filepath.ToSlash(path))
	if err != nil {
		return nil, false, err
	}
	b := []byte(out)
	if len(b) > MaxShowBytes {
		return b[:MaxShowBytes], true, nil
	}
	return b, false, nil
}
