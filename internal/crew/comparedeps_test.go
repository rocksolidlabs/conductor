package crew

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// synthRun is the run id of a synthetic comparison.
const synthRun = "cmp-0000000000000001"

// synthCand is a candidate of a synthetic comparison: its task, the commit
// it shows, and its attempt's receipts (passing ones when nil).
type synthCand struct {
	task, commit string
	rows         []ledger.CheckRow
	state        string // its task's state; succeeded when ""
}

// passingRows are a receipt per expected check of c (the candidate task's
// checks, then the crew manifest's verify steps), each finished, passed
// and stable, of commit.
func passingRows(c Crew, task, commit string) []ledger.CheckRow {
	var rows []ledger.CheckRow
	add := func(phase string, ch Check, manifest string) {
		rows = append(rows, ledger.CheckRow{RunID: synthRun, TaskID: task, N: 1, ID: ch.ID, K: len(rows) + 1, Phase: phase, Manifest: manifest, Argv: slices.Clone(ch.Argv),
			Subject: commit, State: ledger.CheckFinished, Stable: true, Where: "native", LogSHA256: strings.Repeat("a", 64),
			StartedAt: time.Unix(100, 0), FinishedAt: time.Unix(101, 0)})
	}
	for _, ch := range taskOfCrew(c, task).Checks {
		add(ledger.PhaseCheck, ch, "")
	}
	if c.Manifest != nil && !c.Manifest.File {
		for _, ch := range c.Manifest.Verify {
			add(ledger.PhaseVerify, ch, "crew:1")
		}
	}
	return rows
}

// synthetic is the chain read of a comparison of c at base, one run, each
// candidate's attempt 1 its standing result.
func synthetic(c Crew, base string, cands ...synthCand) chainRead {
	cr := chainRead{chain: []ledger.RunPointer{{ID: synthRun}}, revs: map[string]int64{}, crew: c, launched: ledger.RunRow{ID: synthRun, Base: base},
		attempts: map[string][]ledger.AttemptRow{}, checks: map[string][]ledger.CheckRow{}, trees: map[string][]ledger.TreeRow{}}
	for _, sc := range cands {
		state := cmpOrString(sc.state, ledger.TaskSucceeded)
		tr := ledger.TaskRow{RunID: synthRun, ID: sc.task, State: state, Attempts: 1}
		if state == ledger.TaskSucceeded {
			tr.ResultAttempt, tr.ResultCommit = 1, sc.commit
		}
		cr.tasks = append(cr.tasks, tr)
		attemptState := ledger.AttemptSucceeded
		if state != ledger.TaskSucceeded {
			attemptState = ledger.AttemptFailed
		}
		cr.attempts[synthRun] = append(cr.attempts[synthRun], ledger.AttemptRow{RunID: synthRun, TaskID: sc.task, N: 1, StartCommit: base, ResultCommit: sc.commit, State: attemptState})
		rows := sc.rows
		if rows == nil {
			rows = passingRows(c, sc.task, sc.commit)
		}
		cr.checks[synthRun+"/"+sc.task+"/1"] = rows
	}
	return cr
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// measure is the comparison of cr, on an engine of its own.
func measure(t *testing.T, cr chainRead) Comparison {
	t.Helper()
	return (&Engine{}).measureComparison(t.Context(), cr)
}

// A candidate's dependency changes: go.mod's require lines, single and in
// a block, added, removed and moved; package.json's four maps; a lockfile
// listed only; a go.mod past 1 MiB listed without its modules; a
// candidate changing none.
func TestDependencyChangesAreRead(t *testing.T) {
	dir := newRepo(t)
	gomod := "module example.com/x\n\ngo 1.22\n\nrequire example.com/single v1.0.0 // indirect\n\nrequire (\n\texample.com/kept v1.1.0\n\texample.com/moved v1.2.0\n\texample.com/dropped v0.1.0\n)\n"
	pkg := `{"name": "x", "scripts": {"test": "vitest"}, "dependencies": {"vue": "3.4.0"}, "devDependencies": {"vitest": "1.0.0"}, "peerDependencies": {}, "optionalDependencies": {"fsevents": "2.3.0"}}`
	applyEdits(t, dir, []edit{put("go.mod", gomod), put("web/package.json", pkg), put("web/yarn.lock", "lock 1\n")})
	commitAll(t, dir, "the base")
	base := head(t, dir)
	a := commitAt(t, dir, base, "a",
		put("go.mod", "module example.com/x\n\ngo 1.22\n\nrequire (\n\texample.com/kept v1.1.0\n\texample.com/moved v1.3.0 // a comment\n\texample.com/added v0.0.1\n)\n"),
		put("web/package.json", `{"name": "x", "scripts": {"test": "vitest"}, "dependencies": {"vue": "3.5.0", "pinia": "2.0.0"}, "devDependencies": {}, "peerDependencies": {"react": "18.0.0"}, "optionalDependencies": {"fsevents": "2.3.0"}}`),
		put("web/yarn.lock", "lock 2\n"), put("requirements-dev.txt", "pytest\n"))
	b := commitAt(t, dir, base, "b", put("go.mod", gomod+"// "+strings.Repeat("x", maxDepBlob)+"\n"))
	c := commitAt(t, dir, base, "c", put("README", "changed\n"))
	cmp := measure(t, synthetic(compareCrew(t, dir, 3, false), base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}, synthCand{task: "approach-c", commit: c}))

	da := column(t, cmp, "approach-a").Deps
	if da == nil || !slices.Equal(da.Files, []string{"go.mod", "requirements-dev.txt", "web/package.json", "web/yarn.lock"}) || da.Total != 4 {
		t.Fatalf("approach-a's files: %+v", da)
	}
	want := []ModuleChange{
		{File: "go.mod", Name: "example.com/added", To: "v0.0.1"},
		{File: "go.mod", Name: "example.com/dropped", From: "v0.1.0"},
		{File: "go.mod", Name: "example.com/moved", From: "v1.2.0", To: "v1.3.0"},
		{File: "go.mod", Name: "example.com/single", From: "v1.0.0"},
		{File: "web/package.json", Name: "pinia", To: "2.0.0"},
		{File: "web/package.json", Name: "react", To: "18.0.0"},
		{File: "web/package.json", Name: "vitest", From: "1.0.0"},
		{File: "web/package.json", Name: "vue", From: "3.4.0", To: "3.5.0"},
	}
	if !slices.Equal(da.Modules, want) || da.ModulesTotal != len(want) {
		t.Fatalf("approach-a's modules:\n%+v\nwant\n%+v", da.Modules, want)
	}
	if db := column(t, cmp, "approach-b").Deps; db == nil || !slices.Equal(db.Files, []string{"go.mod"}) || len(db.Modules) != 0 || db.ModulesTotal != 0 {
		t.Fatalf("approach-b's go.mod past 1 MiB: %+v", db)
	}
	if dc := column(t, cmp, "approach-c").Deps; dc != nil {
		t.Fatalf("approach-c changed none: %+v", dc)
	}
	// A package.json whose dependencies alone changed is no build file.
	if vf := column(t, cmp, "approach-a").VerifyFiles; vf != nil {
		t.Fatalf("approach-a's test and build files: %+v", vf)
	}
}

// I4: a candidate that merged a commit of another lane's shares it: listed
// in Shared with its count on both sides; independent candidates share
// none.
func TestSharedCommitsAreFlagged(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	b := commitAt(t, dir, base, "b's work", put("b.txt", "b\n"))
	a1 := commitAt(t, dir, base, "a's work", put("a.txt", "a\n"))
	runGit(t, dir, "-c", "commit.gpgsign=false", "merge", "-q", "--no-edit", b)
	a := head(t, dir)
	if a == a1 {
		t.Fatal("no merge")
	}
	c := commitAt(t, dir, base, "c's work", put("c.txt", "c\n"))
	d := commitAt(t, dir, base, "d's work", put("d.txt", "d\n"))
	cmp := measure(t, synthetic(compareCrew(t, dir, 4, false), base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b},
		synthCand{task: "approach-c", commit: c}, synthCand{task: "approach-d", commit: d}))
	if sa := column(t, cmp, "approach-a").Shared; len(sa) != 1 || sa[0].With != "approach-b" || sa[0].Count != 1 || !slices.Equal(sa[0].Commits, []string{b}) {
		t.Fatalf("approach-a's shared: %+v", sa)
	}
	if sb := column(t, cmp, "approach-b").Shared; len(sb) != 1 || sb[0].With != "approach-a" || sb[0].Count != 1 {
		t.Fatalf("approach-b's shared: %+v", sb)
	}
	for _, task := range []string{"approach-c", "approach-d"} {
		if cc := column(t, cmp, task); len(cc.Shared) != 0 || len(cc.Equivalent) != 0 {
			t.Fatalf("%s: %+v %+v", task, cc.Shared, cc.Equivalent)
		}
	}
	// A shared object is not an equivalent patch as well.
	if eq := column(t, cmp, "approach-a").Equivalent; len(eq) != 0 {
		t.Fatalf("approach-a's equivalent: %+v", eq)
	}
}

// I4: a cherry-pick shares no object; git cherry names the commit whose
// patch one of the other lane's carries. A candidate past 200 commits is
// not checked, and says so.
func TestACherryPickIsFlagged(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	b := commitAt(t, dir, base, "b's fix", put("fix.txt", "the fix\n"))
	commitAt(t, dir, base, "a's work", put("a.txt", "a\n"))
	runGit(t, dir, "-c", "commit.gpgsign=false", "cherry-pick", b)
	a := head(t, dir)
	runGit(t, dir, "checkout", "-q", "--detach", base)
	for i := range maxCherryCommits + 1 {
		runGit(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("c %d", i))
	}
	c := head(t, dir)
	cmp := measure(t, synthetic(compareCrew(t, dir, 3, false), base, synthCand{task: "approach-a", commit: a}, synthCand{task: "approach-b", commit: b}, synthCand{task: "approach-c", commit: c}))
	ca := column(t, cmp, "approach-a")
	if len(ca.Shared) != 0 {
		t.Fatalf("a cherry-pick shares no object: %+v", ca.Shared)
	}
	var withB, withC *SharedCommits
	for i := range ca.Equivalent {
		switch ca.Equivalent[i].With {
		case "approach-b":
			withB = &ca.Equivalent[i]
		case "approach-c":
			withC = &ca.Equivalent[i]
		}
	}
	if withB == nil || withB.Count != 1 || !slices.Equal(withB.Commits, []string{a}) || withB.Note != cherryWords {
		t.Fatalf("approach-a's equivalent with approach-b: %+v", ca.Equivalent)
	}
	if withC == nil || withC.Note != cherryTooMany || withC.Count != 0 {
		t.Fatalf("approach-a against a candidate past 200 commits: %+v", ca.Equivalent)
	}
	if cc := column(t, cmp, "approach-c"); !slices.ContainsFunc(cc.Equivalent, func(sc SharedCommits) bool { return sc.With == "approach-a" && sc.Note == cherryTooMany }) {
		t.Fatalf("approach-c's equivalent: %+v", cc.Equivalent)
	}
}

// fakeGit answers a measure's git calls from what a test gives: by the
// subcommand and its last argument.
type fakeGit map[string]string

func (f fakeGit) run(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	key := args[0] + " " + args[len(args)-1]
	if out, ok := f[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("git %s: no answer for %q", args[0], key)
}

// Each list one past its bound is cut there, with its total.
func TestEveryListIsCutAtItsBound(t *testing.T) {
	var stats []FileStat
	for i := range maxDiffList + 1 {
		stats = append(stats, FileStat{Path: fmt.Sprintf("dir%d/f_test.go", i), Status: "A", Added: 1})
	}
	if ds := sizeOf(stats); len(ds.List) != maxDiffList || ds.Total != maxDiffList+1 || ds.Files != maxDiffList+1 {
		t.Fatalf("the size: %d listed of %d", len(ds.List), ds.Total)
	}
	fl, err := verifyFilesOf(t.Context(), fakeGit{}.run, "/repo", "base", "commit", stats[:maxVerifyFiles+1])
	if err != nil || len(fl.Files) != maxVerifyFiles || fl.Total != maxVerifyFiles+1 {
		t.Fatalf("the test files: %+v %v", fl, err)
	}
	var locks []FileStat
	for i := range maxDepFiles + 1 {
		locks = append(locks, FileStat{Path: fmt.Sprintf("p%d/yarn.lock", i), Status: "M"})
	}
	var mods strings.Builder
	mods.WriteString("module x\n\nrequire (\n")
	for i := range maxDepModules + 1 {
		fmt.Fprintf(&mods, "\texample.com/m%02d v1.0.0\n", i)
	}
	mods.WriteString(")\n")
	// The base has no go.mod; the commit's is the one above.
	git := fakeGit{
		"ls-tree :(literal)go.mod":                          "100644 blob 1111111111111111111111111111111111111111\tgo.mod\x00",
		"cat-file 1111111111111111111111111111111111111111": mods.String(),
	}
	baseGit := func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		if args[0] == "ls-tree" && slices.Contains(args, "base") {
			return "", nil
		}
		if args[0] == "cat-file" && args[1] == "-s" {
			return fmt.Sprintf("%d\n", mods.Len()), nil
		}
		return git.run(ctx, dir, limit, args...)
	}
	dc, err := depsOf(t.Context(), baseGit, "/repo", "base", "commit", append(locks, FileStat{Path: "go.mod", Status: "A"}))
	if err != nil || len(dc.Files) != maxDepFiles || dc.Total != maxDepFiles+2 || len(dc.Modules) != maxDepModules || dc.ModulesTotal != maxDepModules+1 {
		t.Fatalf("the dependencies: %d files of %d, %d modules of %d, %v", len(dc.Files), dc.Total, len(dc.Modules), dc.ModulesTotal, err)
	}
	// Shared: eleven commits in common, ten listed with the count.
	var common []string
	for i := range maxSharedList + 1 {
		common = append(common, fmt.Sprintf("%040d", i))
	}
	revs := strings.Join(common, "\n") + "\n"
	shared := func(ctx context.Context, dir string, limit int, args ...string) (string, error) {
		switch args[0] {
		case "rev-list":
			return revs, nil
		case "cherry":
			return "", nil
		}
		return "", fmt.Errorf("git %s", args[0])
	}
	m := &measured{cands: map[string]candidateMeasure{"approach-a": {}, "approach-b": {}}}
	sharedOf(t.Context(), shared, "/repo", "base", []ComparedCandidate{{Task: "approach-a", Commit: "a"}, {Task: "approach-b", Commit: "b"}}, m)
	if sa := m.cands["approach-a"].shared; len(sa) != 1 || sa[0].Count != maxSharedList+1 || len(sa[0].Commits) != maxSharedList {
		t.Fatalf("shared: %+v", sa)
	}
	// Past 10,000 commits on a side: no count and no list, the words.
	var past []string
	for i := range maxSharedCount + 1 {
		past = append(past, fmt.Sprintf("%040d", i))
	}
	revs = strings.Join(past, "\n")
	m = &measured{cands: map[string]candidateMeasure{"approach-a": {}, "approach-b": {}}}
	sharedOf(t.Context(), shared, "/repo", "base", []ComparedCandidate{{Task: "approach-a", Commit: "a"}, {Task: "approach-b", Commit: "b"}}, m)
	if sa := m.cands["approach-a"].shared; len(sa) != 1 || sa[0].Note != moreThanShared || sa[0].Count != 0 || len(sa[0].Commits) != 0 {
		t.Fatalf("past 10,000: %+v", sa)
	}
	if eq := m.cands["approach-a"].equivalent; len(eq) != 1 || eq[0].Note != cherryTooMany {
		t.Fatalf("past 200 commits: %+v", eq)
	}
}

// The test and build files are known by their names, package.json by its
// scripts.
func TestTestAndBuildFilesAreKnownByName(t *testing.T) {
	for p, want := range map[string]string{
		"internal/cache/bench_test.go": FileTest, "test_x.py": FileTest, "pkg/x_test.py": FileTest, "web/a.test.ts": FileTest,
		"web/e2e/a.spec.ts": FileTest, "conftest.py": FileTest, "web/__tests__/x.js": FileTest, "internal/x/testdata/in.txt": FileTest,
		"tests/fixture.json": FileTest, "Makefile": FileBuild, "sub/GNUmakefile": FileBuild, "rules.mk": FileBuild, "justfile": FileBuild,
		"Taskfile.yml": FileBuild, "tox.ini": FileBuild, "noxfile.py": FileBuild, "pytest.ini": FileBuild, "setup.cfg": FileBuild,
		"pyproject.toml": FileBuild, "web/vitest.config.ts": FileBuild, "jest.config.js": FileBuild, "playwright.config.mjs": FileBuild,
		"go.work": FileBuild, "internal/cache/cache.go": "", "README.md": "", "attest.go": "", "contest/x.go": "",
	} {
		got := ""
		switch {
		case isTestFile(p):
			got = FileTest
		case isBuildFile(p):
			got = FileBuild
		}
		if got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	for p, want := range map[string]bool{"go.mod": true, "a/b/go.sum": true, "requirements.txt": true, "requirements-dev.txt": true, "web/pnpm-lock.yaml": true,
		"build.gradle.kts": true, "requirements.in": false, "my-requirements.txt": false, "go.mod.bak": false} {
		if isDependencyFile(p) != want {
			t.Errorf("dependency file %s: %v", p, !want)
		}
	}
}
