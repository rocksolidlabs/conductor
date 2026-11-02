package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/gitcli/gitclitest"
)

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, err := Run(context.Background(), dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-q")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	run("config", "commit.gpgsign", "false")
	os.MkdirAll(filepath.Join(dir, "internal", "api"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n\none\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "internal", "api", "users.go"), []byte("package api\n\nfunc A() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("gone\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pic.bin"), []byte{0, 1, 2, 3}, 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

// The status lists what changed against HEAD with each file's lines: a
// modified file by git's count, an added file by its own lines, a deleted
// one, an untracked one, a binary one as none; the branch and the base's id.
func TestGetStatusListsChangesWithTheirLines(t *testing.T) {
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n\none\nthree\nfour\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "internal", "api", "users_test.go"), []byte("package api\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"), 0o644)
	os.Remove(filepath.Join(dir, "old.txt"))
	os.WriteFile(filepath.Join(dir, "pic.bin"), []byte{0, 9, 9, 9, 9}, 0o644)
	os.WriteFile(filepath.Join(dir, "new.bin"), []byte{0, 1}, 0o644)
	st, err := GetStatus(context.Background(), filepath.Join(dir, "internal"), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Top != dir && !strings.HasSuffix(st.Top, filepath.Base(dir)) {
		t.Fatalf("top %q", st.Top)
	}
	if st.Branch == "" || st.Base == "" || len(st.Base) < 7 {
		t.Fatalf("branch %q base %q", st.Branch, st.Base)
	}
	got := map[string]Change{}
	for _, c := range st.Changes {
		got[c.Path] = c
	}
	want := map[string]Change{
		"README.md":                  {Path: "README.md", Status: "M", Added: 2, Removed: 1},
		"internal/api/users_test.go": {Path: "internal/api/users_test.go", Status: "?", Added: 5},
		"old.txt":                    {Path: "old.txt", Status: "D", Removed: 1},
		"pic.bin":                    {Path: "pic.bin", Status: "M", Binary: true},
		"new.bin":                    {Path: "new.bin", Status: "?", Binary: true},
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: got %+v want %+v", p, got[p], w)
		}
	}
	if len(st.Changes) != len(want) || st.Added != 7 || st.Removed != 2 || st.Truncated {
		t.Fatalf("status %+v", st)
	}
}

// A clean tree lists nothing; outside a repository it is ErrNotRepo; a base
// that does not resolve leaves Base empty and counts nothing.
func TestGetStatusEdges(t *testing.T) {
	dir := repo(t)
	st, err := GetStatus(context.Background(), dir, "")
	if err != nil || len(st.Changes) != 0 || st.Added != 0 {
		t.Fatalf("clean: %+v %v", st, err)
	}
	if _, err := GetStatus(context.Background(), t.TempDir(), ""); err != ErrNotRepo {
		t.Fatalf("outside a repository: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644)
	st, err = GetStatus(context.Background(), dir, "no-such-base")
	if err != nil || st.Base != "" || len(st.Changes) != 1 || st.Changes[0].Added != 0 {
		t.Fatalf("bad base: %+v %v", st, err)
	}
	if _, err := GetStatus(context.Background(), dir, "--output=x"); err == nil {
		t.Fatal("a base starting with a dash was taken")
	}
}

// A working tree is the directory's own when it is the directory, inside it,
// or the one its nearest .git stands for, a link on the way included; one a
// core.worktree points elsewhere is ErrElsewhere for the status and the
// porcelain alike, beside the directory or above it.
func TestTheStatusIsOfTheDirectorysOwnWorkingTree(t *testing.T) {
	dir := repo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "internal", "api"), link, filepath.Join(link, "internal")} {
		if top, err := TopOf(context.Background(), d); err != nil || filepath.Base(top) != filepath.Base(dir) {
			t.Errorf("%s: %q %v", d, top, err)
		}
	}
	base := t.TempDir()
	inner := filepath.Join(base, "inner")
	if err := os.MkdirAll(filepath.Join(base, "beside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), base, "init", "-q", inner); err != nil {
		t.Fatal(err)
	}
	for _, wt := range []string{filepath.Join(base, "beside"), base} {
		if _, err := Run(context.Background(), inner, "config", "core.worktree", wt); err != nil {
			t.Fatal(err)
		}
		if _, err := GetStatus(context.Background(), inner, ""); err != ErrElsewhere {
			t.Errorf("core.worktree %s: the status: %v", wt, err)
		}
		if _, _, _, err := Porcelain(context.Background(), inner); err != ErrElsewhere {
			t.Errorf("core.worktree %s: the porcelain: %v", wt, err)
		}
	}
}

// An untracked link's lines are not counted from what it points to, nor is a
// FIFO's waited for.
func TestCountLinesReadsRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("1\n2\n3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, _ := countLines(file); n != 3 {
		t.Fatalf("a file: %d", n)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if n, bin := countLines(filepath.Join(dir, "link")); n != 0 || bin {
		t.Fatalf("a link: %d %v", n, bin)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, bin := countLines(fifo); n != 0 || bin {
		t.Fatalf("a FIFO: %d %v", n, bin)
	}
}

// The list stops at MaxChanges and says so.
func TestGetStatusStopsAtTheBound(t *testing.T) {
	dir := repo(t)
	for i := 0; i < MaxChanges+3; i++ {
		os.WriteFile(filepath.Join(dir, "f"+strings.Repeat("0", 4-len(itoa(i)))+itoa(i)+".txt"), []byte("x\n"), 0o644)
	}
	st, err := GetStatus(context.Background(), dir, "")
	if err != nil || len(st.Changes) != MaxChanges || !st.Truncated {
		t.Fatalf("%d changes truncated=%v err=%v", len(st.Changes), st.Truncated, err)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/1000%10)) + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)))
}

// Show is a file at a revision; a path the revision lacks is an error; a
// dash or a colon in what is asked for is refused, and so is no revision.
func TestShow(t *testing.T) {
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644)
	b, truncated, err := Show(context.Background(), dir, "HEAD", "README.md")
	if err != nil || truncated || string(b) != "# repo\n\none\ntwo\n" {
		t.Fatalf("show: %q %v %v", b, truncated, err)
	}
	if _, _, err := Show(context.Background(), dir, "HEAD", "nope.txt"); err == nil {
		t.Fatal("a missing path showed")
	}
	if _, _, err := Show(context.Background(), dir, "--output=x", "README.md"); err == nil {
		t.Fatal("a dash revision was taken")
	}
	if _, _, err := Show(context.Background(), dir, "HEAD", "-x"); err == nil {
		t.Fatal("a dash path was taken")
	}
	// With no revision, ":0:README.md" would be the index's file, not a revision's.
	if b, _, err := Show(context.Background(), dir, "", "0:README.md"); err == nil {
		t.Fatalf("no revision was taken: %q", b)
	}
	// A revision names one commit: a folder's tree or a range would read the
	// path from elsewhere.
	tree, err := Run(context.Background(), dir, "rev-parse", "HEAD:internal")
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range []string{strings.TrimSpace(tree), "HEAD...HEAD", "HEAD^{tree}", "no-such-rev"} {
		if b, _, err := Show(context.Background(), dir, rev, "api/users.go"); err == nil {
			t.Errorf("revision %q was taken: %q", rev, b)
		}
	}
	head, err := Run(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range []string{strings.TrimSpace(head), "HEAD~0", "main", "HEAD^{commit}"} {
		if rev == "main" {
			if _, err := Run(context.Background(), dir, "rev-parse", "--verify", "--quiet", "main"); err != nil {
				continue // the default branch has another name here
			}
		}
		if b, _, err := Show(context.Background(), dir, rev, "README.md"); err != nil || string(b) != "# repo\n\none\ntwo\n" {
			t.Errorf("revision %q: %q %v", rev, b, err)
		}
	}
	if _, _, err := Show(context.Background(), t.TempDir(), "HEAD", "README.md"); err != ErrNotRepo {
		t.Fatalf("outside a repository: %v", err)
	}
}

// Porcelain is the status without the line counts: the top and each change
// with its letter, from a directory inside the tree; outside one, an error.
func TestPorcelain(t *testing.T) {
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0o644)
	os.Remove(filepath.Join(dir, "old.txt"))
	os.WriteFile(filepath.Join(dir, "internal", "api", "new.go"), []byte("package api\n"), 0o644)
	top, changes, truncated, err := Porcelain(context.Background(), filepath.Join(dir, "internal"))
	if err != nil || truncated {
		t.Fatalf("%v %v", err, truncated)
	}
	if top != dir && !strings.HasSuffix(top, filepath.Base(dir)) {
		t.Fatalf("top %q", top)
	}
	got := map[string]string{}
	for _, c := range changes {
		if c.Added != 0 || c.Removed != 0 {
			t.Fatalf("no line counts expected: %+v", c)
		}
		got[c.Path] = c.Status
	}
	if got["README.md"] != "M" || got["old.txt"] != "D" || got["internal/api/new.go"] != "?" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if _, _, _, err := Porcelain(context.Background(), t.TempDir()); err == nil {
		t.Fatal("outside a repository must fail")
	}
}

// The git Conductor runs starts no program the repository's own
// configuration names (gitclitest arms one with a marking script at every
// such place, some through include and includeIf, a filter with the empty
// name; hooks in .git/hooks and through core.hooksPath), and still reads it
// right: the status with its lines, the porcelain status, a file at a
// revision. Nothing fetches from the repository's remote either.
func TestGitStartsNoProgramTheRepositoryNames(t *testing.T) {
	for _, hooksPath := range []bool{false, true} {
		r := gitclitest.New(t, gitclitest.Options{HooksPath: hooksPath})
		ctx := context.Background()
		st, err := GetStatus(ctx, r.Dir, "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]Change{}
		for _, c := range st.Changes {
			got[c.Path] = c
		}
		want := map[string]Change{}
		for path := range gitclitest.Dirty {
			status := "M"
			if _, ok := gitclitest.Committed[path]; !ok {
				status = "?"
			}
			want[path] = Change{Path: path, Status: status, Added: 1}
		}
		for p, w := range want {
			if got[p] != w {
				t.Errorf("hooksPath %v: %s: got %+v want %+v", hooksPath, p, got[p], w)
			}
		}
		if len(st.Changes) != len(want) || st.Branch != "main" || st.Base == "" || st.Added != len(want) || st.Removed != 0 {
			t.Fatalf("hooksPath %v: status %+v", hooksPath, st)
		}
		r.NoneFired(t, "the status")
		if _, changes, _, err := Porcelain(ctx, r.Dir); err != nil || len(changes) != len(want) {
			t.Fatalf("porcelain: %+v %v", changes, err)
		}
		r.NoneFired(t, "the porcelain status")
		for path, body := range gitclitest.Committed {
			if b, _, err := Show(ctx, r.Dir, "HEAD", path); err != nil || string(b) != body {
				t.Fatalf("show %s: %q %v", path, b, err)
			}
		}
		r.NoneFired(t, "show")
		// The repository allows the file protocol; the environment allows none.
		if out, err := Run(ctx, r.Dir, "ls-remote", "origin"); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("ls-remote: %q %v", out, err)
		}
		if _, err := Run(ctx, r.Dir, "fetch", "origin"); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("fetch: %v", err)
		}
		r.NoneFired(t, "a transport")
	}
}

// No filter runs, not even one the person's global configuration defines
// (a filter such as Git LFS's reads the repository's configuration for
// programs of its own): not for a status, not for a checkout, which writes
// the file as the repository stores it.
func TestNoFilterRunsNotEvenAGlobalOne(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	ctx := context.Background()
	global := "[filter \"owner\"]\n\tclean = " + r.Script + " owner-clean\n\tsmudge = " + r.Script + " owner-smudge\n\trequired = true\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.Dir, "owner.own"), []byte("o\np\n"), 0o644)
	st, err := GetStatus(ctx, r.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var own Change
	for _, c := range st.Changes {
		if c.Path == "owner.own" {
			own = c
		}
	}
	if own.Status != "M" || own.Added != 1 {
		t.Fatalf("owner.own: %+v", own)
	}
	r.NoneFired(t, "the status")
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, r.Dir, wt, "side", "HEAD"); err != nil {
		t.Fatal(err)
	}
	r.NoneFired(t, "worktree add")
	if b, err := os.ReadFile(filepath.Join(wt, "owner.own")); err != nil || string(b) != "o\n" {
		t.Fatalf("owner.own in the worktree: %q %v", b, err)
	}
}

// AddWorktree checks the new worktree out with the configuration that
// applies there (a filter included only on its branch or only in a linked
// worktree is off too) and the files as committed; a worktree add that
// checks out in one command is refused, as are arguments that read as
// options.
func TestAddWorktreeChecksOutInTheNewWorktree(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	ctx := context.Background()
	wt := filepath.Join(r.Dir, ".conductor", "worktrees", "run", "lead")
	if err := AddWorktree(ctx, r.Dir, wt, "crew/run/lead", "HEAD"); err != nil {
		t.Fatal(err)
	}
	r.NoneFired(t, "worktree add")
	for path, body := range gitclitest.Committed {
		if b, err := os.ReadFile(filepath.Join(wt, path)); err != nil || string(b) != body {
			t.Fatalf("%s in the worktree: %q %v", path, b, err)
		}
	}
	if out, err := Run(ctx, wt, "status", "--porcelain"); err != nil || out != "" {
		t.Fatalf("the new worktree's status: %q %v", out, err)
	}
	if branch, err := Run(ctx, wt, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || strings.TrimSpace(branch) != "crew/run/lead" {
		t.Fatalf("branch %q %v", branch, err)
	}
	r.NoneFired(t, "the new worktree's status")
	if _, err := Run(ctx, r.Dir, "worktree", "add", "-b", "one", filepath.Join(t.TempDir(), "one"), "HEAD"); err == nil {
		t.Fatal("a worktree add that checks out ran")
	}
	if err := AddWorktree(ctx, r.Dir, filepath.Join(t.TempDir(), "x"), "--orphan", "HEAD"); err == nil {
		t.Fatal("a branch that reads as an option was taken")
	}
}

// A repository whose shared configuration sets core.worktree (with
// extensions.worktreeConfig, which makes linked worktrees honour it) does
// not send the new worktree's checkout to that directory: the original
// checkout's uncommitted work stays, and the new worktree holds the commit.
func TestAddWorktreeChecksOutWhereItWasMadeWhateverCoreWorktreeSays(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	ctx := context.Background()
	gitclitest.Git(t, r.Dir, "config", "core.repositoryformatversion", "1")
	gitclitest.Git(t, r.Dir, "config", "extensions.worktreeConfig", "true")
	gitclitest.Git(t, r.Dir, "config", "core.worktree", r.Dir)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, r.Dir, wt, "side", "HEAD"); err != nil {
		t.Fatal(err)
	}
	for path, body := range gitclitest.Dirty {
		if b, err := os.ReadFile(filepath.Join(r.Dir, path)); err != nil || string(b) != body {
			t.Fatalf("%s in the original checkout: %q %v", path, b, err)
		}
	}
	for path, body := range gitclitest.Committed {
		if b, err := os.ReadFile(filepath.Join(wt, path)); err != nil || string(b) != body {
			t.Fatalf("%s in the new worktree: %q %v", path, b, err)
		}
	}
	r.NoneFired(t, "worktree add")
}

// filterNames reads the names a configuration gives a filter program,
// wherever it is defined, whatever their case or dots, the empty name too,
// each once; a configuration's conditional includes count as they apply in
// the directory asked about.
func TestFilterNames(t *testing.T) {
	r := gitclitest.New(t, gitclitest.Options{})
	names := func(dir string) []string {
		t.Helper()
		n, err := filterNames(context.Background(), []string{"-C", dir})
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(n)
		return n
	}
	// The machine's system config (a CI runner's names Git LFS's filter) is
	// listed too, as it should be; git reads it whatever the environment, so
	// its names are expected beside the test's own.
	system := names(t.TempDir())
	with := func(want ...string) []string {
		all := slices.Concat(want, system)
		slices.Sort(all)
		return slices.Compact(all)
	}
	global := "[filter \"owner\"]\n\tclean = x\n[filter \"Dotted.Name\"]\n\tsmudge = y\n\trequired = true\n[filter \"none\"]\n\trequired = true\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := names(r.Dir), with("", "Dotted.Name", "inc", "owner", "proc", "single"); !slices.Equal(got, want) {
		t.Fatalf("on main: %q, want %q", got, want)
	}
	// A linked worktree on a crew/ branch: both conditional includes apply there.
	wt := filepath.Join(r.Dir, ".conductor", "wt")
	gitclitest.Git(t, r.Dir, "worktree", "add", "-q", "--no-checkout", "-b", "crew/x/y", wt, "HEAD")
	if got, want := names(wt), with("", "Dotted.Name", "inc", "inworktree", "onbranch", "owner", "proc", "single"); !slices.Equal(got, want) {
		t.Fatalf("in a crew worktree: %q, want %q", got, want)
	}
	if got := names(t.TempDir()); !slices.Equal(got, with("Dotted.Name", "owner")) {
		t.Fatalf("outside a repository: %q", got)
	}
	// The empty name goes through -c: a status turns it off and runs.
	if _, err := Run(context.Background(), r.Dir, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	// A name -c cannot carry is refused, not passed over.
	gitclitest.Git(t, r.Dir, "config", "filter.a=b.clean", "x")
	if _, err := Run(context.Background(), r.Dir, "status", "--porcelain"); err == nil || !strings.Contains(err.Error(), "cannot be turned off") {
		t.Fatalf("a filter named with =: %v", err)
	}
}

// ChangedSince lists what HEAD's commits since a base changed: committed
// and only committed, against the merge base, with a nested path relative
// to the top; a base that moved on adds nothing of its own.
func TestChangedSince(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		out, err := Run(ctx, dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	base := run("rev-parse", "HEAD")
	if paths, truncated, err := ChangedSince(ctx, dir, base); err != nil || len(paths) != 0 || truncated {
		t.Fatalf("nothing since the base: %v %v %v", paths, truncated, err)
	}
	run("checkout", "-q", "-b", "work")
	os.WriteFile(filepath.Join(dir, "internal", "api", "users.go"), []byte("package api\n\nfunc B() {}\n"), 0o644)
	run("commit", "-q", "-am", "work")
	// Not committed: not listed.
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("dirty\n"), 0o644)
	// The base moves on with a file of its own.
	run("stash", "-q")
	run("checkout", "-q", "-")
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("moved on\n"), 0o644)
	run("commit", "-q", "-am", "main moves")
	moved := run("rev-parse", "HEAD")
	run("checkout", "-q", "work")
	run("stash", "pop", "-q")
	for _, b := range []string{base, moved} {
		paths, truncated, err := ChangedSince(ctx, filepath.Join(dir, "internal"), b)
		if err != nil || truncated || !slices.Equal(paths, []string{"internal/api/users.go"}) {
			t.Fatalf("since %s: %v %v %v", b, paths, truncated, err)
		}
	}
	if _, _, err := ChangedSince(ctx, dir, "-x"); err == nil {
		t.Fatal("a base that is a flag")
	}
	if _, _, err := ChangedSince(ctx, t.TempDir(), base); err == nil {
		t.Fatal("outside a repository")
	}
}
