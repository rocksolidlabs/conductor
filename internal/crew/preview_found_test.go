package crew

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// listing is every path under dir (its .git aside) with its kind and size.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		size := int64(0)
		if info, err := d.Info(); err == nil && !d.IsDir() {
			size = info.Size()
		}
		out = append(out, fmt.Sprintf("%s %v %d", rel, d.Type(), size))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Something found at the preview's path that is not a worktree of the
// run's repository, at its own top, as a directory, is refused naming the
// path, before any step: a link to a detached worktree of the repository
// outside the run's worktrees folder at the commit, a link to the
// attempt's own worktree, an independent clone at the commit, and an
// empty folder while the run's repository is itself at the commit. What
// the path leads to is listed before and after the open: nothing is made
// there, no step's receipt is written, the preview has no row and is not
// held.
func TestAFoundPreviewCheckoutIsAWorktreeOfTheRunsRepository(t *testing.T) {
	cases := []struct {
		name  string
		make  func(t *testing.T, dir, checkout string, ts TaskState) (look string)
		words string
	}{
		{"a link to a worktree outside the worktrees folder", func(t *testing.T, dir, checkout string, ts TaskState) string {
			wt := filepath.Join(t.TempDir(), "wt")
			runGit(t, dir, "worktree", "add", "-q", "--detach", wt, ts.Attempt.Result)
			if err := os.Symlink(wt, checkout); err != nil {
				t.Fatal(err)
			}
			return wt
		}, "is not a directory"},
		{"a link to the attempt's worktree", func(t *testing.T, _, checkout string, ts TaskState) string {
			if err := os.Symlink(ts.Attempt.Worktree, checkout); err != nil {
				t.Fatal(err)
			}
			return ts.Attempt.Worktree
		}, "is not a directory"},
		{"an independent clone", func(t *testing.T, dir, checkout string, ts TaskState) string {
			runGit(t, t.TempDir(), "clone", "-q", "--no-checkout", dir, checkout)
			runGit(t, checkout, "checkout", "-q", "--detach", ts.Attempt.Result)
			return checkout
		}, "is not a worktree of the run's repository"},
		{"an empty folder over the repository at the commit", func(t *testing.T, dir, checkout string, ts TaskState) string {
			runGit(t, dir, "checkout", "-q", "--detach", ts.Attempt.Result)
			if err := os.Mkdir(checkout, 0o755); err != nil {
				t.Fatal(err)
			}
			return checkout
		}, "is not the top of a git work tree"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := newRepo(t)
			e, _, l := managedEngine(t)
			run, ts := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
			checkout := ts.Attempt.Worktree + ".preview"
			look := c.make(t, dir, checkout, ts)
			before, steps := listing(t, look), len(l.stepRows(run.ID, "build", 1))
			_, err := e.OpenPreview(t.Context(), run.ID, "build", 1, "admin")
			if !errors.Is(err, ErrPreviewRefused) || !strings.Contains(err.Error(), checkout+" is there and "+c.words) {
				t.Fatalf("the open: %v, want %q", err, c.words)
			}
			if after := listing(t, look); !slices.Equal(before, after) {
				t.Fatalf("%s changed by the open:\nbefore %q\nafter  %q", look, before, after)
			}
			if rows := l.stepRows(run.ID, "build", 1); len(rows) != steps {
				t.Fatalf("a step of the preview ran: %s", rowWords(rows[steps:]))
			}
			if w := l.writers(run.ID, "build", 1, 1); len(w) != 0 || previewHeld(e, run.ID, "build", 1) {
				t.Fatalf("the refused open wrote %v or is held", w)
			}
		})
	}
}

// No next try of an owed write is armed once the engine has ended: the
// drain comes (its context done, so it returns at once) while the
// preview's end is held before its last write, which the ledger then
// refuses; the preview is let go without a retry's wait.
func TestNoRetryOfAPreviewsEndIsArmedAfterTheDrain(t *testing.T) {
	dir := newRepo(t)
	e, _, l := managedEngine(t)
	waits := previewWaits(e)
	finished := make(chan struct{}, 1)
	e.previewFinished = func() { finished <- struct{}{} }
	run, _ := candidateOf(t, e, l, repoManifestCrew(dir, previewManifest(), work("build", "lead")))
	openPreview(t, e, run.ID)
	l.previewWhen(t, run.ID, "build", 1, 1, "ready", previewReadyRow)
	held, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	e.beforeTreeEnd = func(kind string) {
		if kind == ledger.TreePreview {
			held <- struct{}{}
			<-release
		}
	}
	l.mu.Lock()
	l.failOn = "preview.ended"
	l.mu.Unlock()
	t.Cleanup(func() { takeEnds(l) })
	if err := e.ClosePreview(run.ID, "build", 1, 1); err != nil {
		t.Fatal(err)
	}
	await(t, "the end held before its last write", held)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	e.Drain(done)
	let()
	await(t, "the preview let go", finished)
	for len(waits) > 0 {
		if w := <-waits; w.d == previewRetry {
			t.Fatal("a next try armed after the drain")
		}
	}
}
