package gitcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// manyFiles is a repository whose last commit holds n files besides the
// four repo commits first, and that commit's id.
func manyFiles(t *testing.T, n int) (dir, commit string) {
	t.Helper()
	dir = repo(t)
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := Run(ctx, dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, dir, "commit", "-q", "-m", "many"); err != nil {
		t.Fatal(err)
	}
	out, err := Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(out)
}

// fakeGit puts a git first on PATH that answers a configuration look-up
// with nothing and runs script for every other command.
func fakeGit(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script as git")
	}
	bin := t.TempDir()
	body := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = config ] && exit 1; done\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// RunEach hands records one by one and stops when told: git is ended
// there, though it would write on (and then wait) for ever, and the run
// answers truncated with only the records taken.
func TestRunEachStopsWhenTold(t *testing.T) {
	fakeGit(t, `printf 'one\0two\0three\0four\0'; exec sleep 3600`)
	var got []string
	truncated, err := RunEach(context.Background(), t.TempDir(), nil, Limits{Out: 1 << 20}, func(r []byte) bool {
		got = append(got, string(r))
		return len(got) < 2
	}, "ls-tree", "-z")
	if err != nil || !truncated || strings.Join(got, ",") != "one,two" {
		t.Fatalf("stopped after two: %v %v %q", truncated, err, got)
	}
}

// A real git's listing through RunEach, read whole and then stopped part
// way: each record handed is a copy the caller kept.
func TestRunEachHandsEveryRecord(t *testing.T) {
	dir, commit := manyFiles(t, 3000)
	var err error
	var truncated bool
	var all []string
	truncated, err = RunEach(context.Background(), dir, nil, Limits{Out: 64 << 20}, func(r []byte) bool {
		all = append(all, string(r))
		return true
	}, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil || truncated || len(all) != 3004 || !strings.Contains(strings.Join(all, "|"), "\tf00000.txt|") || !strings.Contains(strings.Join(all, "|"), "\tf02999.txt|") {
		t.Fatalf("the whole listing: %v %v %d", truncated, err, len(all))
	}
	var some []string
	truncated, err = RunEach(context.Background(), dir, nil, Limits{Out: 64 << 20}, func(r []byte) bool {
		some = append(some, string(r))
		return len(some) < 10
	}, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil || !truncated || len(some) != 10 || some[9] != all[9] {
		t.Fatalf("ten records: %v %v %d", truncated, err, len(some))
	}
}

// RunEach reads no more than its bound: past lim.Out bytes git is ended,
// the records whole within the bound are handed and the one it cuts is
// not; a listing within the bound comes whole.
func TestRunEachStopsAtItsByteBound(t *testing.T) {
	dir, commit := manyFiles(t, 2000)
	const bound = 1000
	held := 0
	truncated, err := RunEach(context.Background(), dir, nil, Limits{Out: bound}, func(r []byte) bool {
		held += len(r) + 1
		return true
	}, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil || !truncated || held == 0 || held > bound {
		t.Fatalf("past the bound: truncated %v, %v, %d bytes handed", truncated, err, held)
	}
	n := 0
	truncated, err = RunEach(context.Background(), dir, nil, Limits{Out: 1 << 20}, func([]byte) bool { n++; return true }, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil || truncated || n != 2004 {
		t.Fatalf("within the bound: %v %v %d", truncated, err, n)
	}
	// A context ended before the run starts none.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunEach(ctx, dir, nil, Limits{Out: 1 << 20}, func([]byte) bool { return true }, "ls-tree", commit); err == nil {
		t.Fatal("an ended context: no error")
	}
}

// A git that writes past 4 KiB of standard error is kept to its first
// 4 KiB: the error that says why it failed holds no more.
func TestGitStderrBounded(t *testing.T) {
	fakeGit(t, `i=0; printf 'fatal: '>&2; while [ $i -lt 400 ]; do printf 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' >&2; i=$((i+1)); done; echo >&2; exit 2`)
	_, err := RunEach(context.Background(), t.TempDir(), nil, Limits{Out: 1 << 10}, func([]byte) bool { return true }, "ls-tree", "-z")
	if err == nil || !strings.Contains(err.Error(), "fatal") && !strings.Contains(err.Error(), "xxxx") || len(err.Error()) > maxBoundedStderr+64 {
		t.Fatalf("the error: %d bytes, %.80v", len(fmt.Sprint(err)), err)
	}
	if ExitStatus(err) != 2 {
		t.Fatalf("the exit status: %d", ExitStatus(err))
	}
}
