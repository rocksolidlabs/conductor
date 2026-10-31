package crew

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// testSpecPNG is the content of the attachment testPackage hands over,
// which testFiles serves.
var testSpecPNG = []byte("\x89PNG\r\n\x1a\nthe spec")

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fileStore stands in for Dispatch's store of a launch's attachments
// (UsePackageFiles): the content of each by its SHA-256, and what it does
// wrong when a test asks it to.
type fileStore struct {
	mu    sync.Mutex
	files map[string][]byte
	// fail, when set, is every open's error; short serves that many bytes
	// of each file fewer; wrong serves other bytes of the same size;
	// broken fails the read after the first byte.
	fail   error
	short  int
	wrong  bool
	broken error
	opened []string // launch/sha256 of each open, in order
}

func newFileStore(contents ...[]byte) *fileStore {
	s := &fileStore{files: map[string][]byte{}}
	for _, c := range contents {
		s.files[sha256Hex(c)] = c
	}
	return s
}

func (s *fileStore) open(_ context.Context, launch, sum string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, launch+"/"+sum)
	if s.fail != nil {
		return nil, s.fail
	}
	b, ok := s.files[sum]
	if !ok {
		return nil, fs.ErrNotExist
	}
	b = slices.Clone(b)
	switch {
	case s.short > 0:
		b = b[:max(0, len(b)-s.short)]
	case s.wrong:
		for i := range b {
			b[i] ^= 0xff
		}
	case s.broken != nil:
		return io.NopCloser(io.MultiReader(bytes.NewReader(b[:1]), errReader{s.broken})), nil
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *fileStore) opens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.opened)
}

// set changes what the store does wrong, under its lock.
func (s *fileStore) set(fn func(s *fileStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// testFiles is the store every readyEngine reads attachments from: it
// holds testPackage's.
var testFiles = newFileStore(testSpecPNG)

// testNotes is the content of fullPackage's second attachment.
var testNotes = []byte("notes: the loop is in login.go\n")

// fullPackage is a launch package of run runID at base with every part
// given: the ticket's fields, the plan revision's approval with a note,
// yolo, references, two attachments (testSpecPNG and testNotes),
// constraints, decisions and two comments.
func fullPackage(runID, base string) Package {
	at := time.Date(2026, 10, 30, 9, 15, 0, 0, time.UTC)
	return Package{
		Version: PackageVersion,
		Ticket: PackageTicket{ID: testTicketID, Key: "CND-12", Rev: 7, Title: "Fix the login loop", Description: "The login page loops back to itself.", Acceptance: "- It logs in.\n- It stays logged in.",
			Status: "in_progress", Priority: 3, Horizon: "now", Owner: "Nate", Assignees: []string{"Alice", "Bob"}, Labels: []string{"bug", "auth"},
			Milestone: &PackageMilestone{Name: "R1", Target: "2026-11-15"}, Parent: &PackageTicketRef{Key: "CND-3", Title: "Accounts", Status: "open"},
			Blockers: []PackageTicketRef{{Key: "CND-9", Title: "Session store", Status: "open"}}, BlockersTotal: 3, Intent: strings.Repeat("1", 64), Criteria: strings.Repeat("2", 64)},
		Plan: PackagePlan{ID: testPlanID, N: 2, Digest: strings.Repeat("d", 64), ApprovedAt: at, ApprovedBy: "Nate", Note: "Small and safe.\nShip it.",
			Crew: PackageCrew{ID: "api-sweep", Name: "API sweep"}, Base: base, Yolo: true, Agents: []PackageAgent{{ID: "claude", SHA256: strings.Repeat("e", 64)}}},
		References: []PackageReference{{Path: "web/login.go", Start: 12, End: 30, Note: "the redirect", Object: strings.Repeat("a", 40), Kind: "blob"}, {Path: "docs", Object: strings.Repeat("b", 40), Kind: "tree"}},
		Attachments: []PackageAttachment{
			{ID: "a1", Name: "Spec.png", File: "01-Spec.png", Size: int64(len(testSpecPNG)), SHA256: sha256Hex(testSpecPNG), ContentType: "image/png"},
			{ID: "a2", Name: "my notes.txt", File: "02-my_notes.txt", Size: int64(len(testNotes)), SHA256: sha256Hex(testNotes), ContentType: "text/plain"},
		},
		Comments: []PackageComment{
			{ID: "c1", N: 1, Author: "nate", By: "Nate", At: at, Body: "It started after the cookie change.", SHA256: strings.Repeat("3", 64)},
			{ID: "c2", N: 4, Author: "alice", At: at.Add(time.Hour), Body: "Safari only.", SHA256: strings.Repeat("4", 64)},
		},
		Constraints: "Do not touch the session store.",
		Decisions:   []PackageDecision{{ID: "ship", Question: "Ship behind a flag?", Kind: "approval", Before: []string{"build"}}},
		Launch:      PackageLaunch{ID: testLaunchID, Run: runID, Label: "CND-12 Fix the login loop", At: at.Add(2 * time.Hour), By: "Nate"},
	}
}

// encodePackage is p as Dispatch hands it over, with a planned launch of it.
func encodePackage(t *testing.T, p Package) Planned {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return Planned{RunID: p.Launch.Run, Base: p.Plan.Base, Digest: packageDigest(b), Package: b}
}

// packageFiles are an attempt's package files as its worktree holds them:
// ticket.md, package.json and each attachment by its name there.
type packageFiles struct {
	Ticket, Package string
	Attachments     map[string]string
}

func readPackageFiles(t *testing.T, ts TaskState) packageFiles {
	t.Helper()
	dir := filepath.Join(ts.Attempt.Worktree, taskDir)
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("attempt %d: %v", ts.Attempt.N, err)
		}
		return string(b)
	}
	f := packageFiles{Ticket: read(ticketFile), Package: read(packageFileName), Attachments: map[string]string{}}
	entries, err := os.ReadDir(filepath.Join(dir, attachmentsDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	for _, en := range entries {
		f.Attachments[en.Name()] = read(filepath.Join(attachmentsDir, en.Name()))
	}
	return f
}

// wantPackageFiles is what an attempt of a run launched with p is given.
func wantPackageFiles(t *testing.T, p Planned, contents map[string][]byte) packageFiles {
	t.Helper()
	rp, err := readPackage(p.Package)
	if err != nil {
		t.Fatal(err)
	}
	f := packageFiles{Ticket: ticketMarkdown(rp.p), Package: string(p.Package), Attachments: map[string]string{}}
	for _, a := range rp.p.Attachments {
		f.Attachments[a.File] = string(contents[a.SHA256])
	}
	return f
}

// ledgerAttempt waits for the ledger to hold the run's attempt n of the
// task in a closed state, and returns its row.
func ledgerAttempt(t *testing.T, ld *ledger.Ledger, runID, taskID string, n int, state string) ledger.AttemptRow {
	t.Helper()
	var row ledger.AttemptRow
	waitTrue(t, fmt.Sprintf("attempt %d of %s %s in the ledger", n, taskID, state), func() bool {
		rows, err := ld.Attempts(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range rows {
			if a.TaskID == taskID && a.N == n && a.State == state {
				row = a
				return true
			}
		}
		return false
	})
	return row
}

// failedPayload is the run's attempt.failed event of the task's attempt
// n, from the ledger.
func failedPayload(t *testing.T, ld *ledger.Ledger, runID, taskID string, n int) map[string]any {
	t.Helper()
	evs, err := ld.Events(context.Background(), runID, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind != "attempt.failed" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p["task"] == taskID && p["attempt"] == float64(n) {
			return p
		}
	}
	t.Fatalf("no attempt.failed of %s attempt %d", taskID, n)
	return nil
}

// Each part of ticket.md is there when the package has it, and left out
// when it does not: the ticket's key and title, the revision it stood at
// and the plan revision with its approval and note, its fields, the
// description, the acceptance criteria, the constraints, the decisions,
// the references with their objects, the attachments beside the names they
// are written as, and the comments.
func TestTicketMarkdown(t *testing.T) {
	p := fullPackage("api-sweep-0000000a", strings.Repeat("b", 40))
	p.Ticket.Title = "Fix the login\nloop"
	md := ticketMarkdown(p)
	for _, want := range []string{
		"# CND-12: Fix the login loop\n",
		"as it stood at its revision 7, when plan revision 2 launched this run (api-sweep-0000000a, 2026-10-30T11:15:00Z, by Nate)",
		"a change made to the ticket since is a person's to decide",
		"Plan revision 2 was approved by Nate at 2026-10-30T09:15:00Z; it runs the crew \"API sweep\" at the commit " + strings.Repeat("b", 40) + ", its agents without their permission prompts.",
		"> Small and safe.\n> Ship it.",
		"- Status: in_progress\n", "- Priority: high\n", "- Horizon: now\n", "- Owner: Nate\n", "- Assignees: Alice, Bob\n", "- Labels: bug, auth\n",
		"- Milestone: R1 (target 2026-11-15)\n", "- Parent: CND-3 \"Accounts\" (open)\n", "- Blocked by: CND-9 \"Session store\" (open); 2 more\n",
		"## Description\n\nThe login page loops back to itself.\n",
		"## Acceptance criteria\n\n- It logs in.\n- It stays logged in.\n",
		"## Constraints\n\nDo not touch the session store.\n",
		"## Decisions a person makes\n\n- ship (approval): Ship behind a flag? Before: build.\n",
		"- `web/login.go` lines 12 to 30 (blob " + strings.Repeat("a", 40) + "): the redirect\n",
		"- `docs` (tree " + strings.Repeat("b", 40) + ")\n",
		"- \"Spec.png\": `.conductor/attachments/01-Spec.png`, " + fmt.Sprint(len(testSpecPNG)) + " bytes, image/png, SHA-256 " + sha256Hex(testSpecPNG) + "\n",
		"- \"my notes.txt\": `.conductor/attachments/02-my_notes.txt`",
		"### Comment 1 by Nate, 2026-10-30T09:15:00Z\n\nIt started after the cookie change.\n",
		"### Comment 4 by alice, 2026-10-30T10:15:00Z\n\nSafari only.\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("ticket.md has no %q:\n%s", want, md)
		}
	}

	bare := Package{Version: PackageVersion, Ticket: PackageTicket{ID: testTicketID, Key: "CND-1", Title: "Bare"},
		Plan: PackagePlan{ID: testPlanID, N: 1, Crew: PackageCrew{Name: "c"}, Base: strings.Repeat("b", 40)}, Launch: PackageLaunch{ID: testLaunchID, Run: "c-00000001"}}
	md = ticketMarkdown(bare)
	if !strings.HasPrefix(md, "# CND-1: Bare\n\n") || !strings.Contains(md, "Plan revision 1; it runs the crew \"c\"") {
		t.Fatalf("a bare ticket.md:\n%s", md)
	}
	for _, absent := range []string{"##", "- Status", "- Priority", "- Owner", "- Labels", "- Milestone", "- Parent", "- Blocked by", "- Assignees", "- Horizon", "permission prompts", "note", "approved by", "\n\n\n"} {
		if strings.Contains(md, absent) {
			t.Errorf("a bare ticket.md has %q:\n%s", absent, md)
		}
	}
}

// Every attempt of a run launched from a ticket, a review's too, finds
// ticket.md, package.json byte for byte as launched and each attachment
// with its content's hash beside its task files before its session
// starts, and task.json names them; none of them is in the result. A
// package file there already is refused, never written over, and is the
// task files' failure, not the package's.
func TestTheAttemptGetsThePackageFiles(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	e, ld := sqlEngine(t)
	store := newFileStore(testSpecPNG, testNotes)
	e.UsePackageFiles(store.open)
	c := repoCrew(dir, work("build", "lead"), review("check", "critic", "build"))
	p := encodePackage(t, fullPackage("api-sweep-0000000a", base))
	run := launchPlanned(t, e, c, p)
	want := wantPackageFiles(t, p, store.files)
	if len(want.Attachments) != 2 {
		t.Fatalf("the package's attachments: %v", want.Attachments)
	}

	b := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	if got := readPackageFiles(t, b); !reflect.DeepEqual(got, want) {
		t.Fatalf("the build's package files:\n%+v\nwant\n%+v", got, want)
	}
	for name, content := range readPackageFiles(t, b).Attachments {
		if want := store.files[sha256Hex([]byte(content))]; string(want) != content {
			t.Fatalf("the attachment %s is not the store's content", name)
		}
	}
	md, tf := taskFileOf(t, b)
	wantTicket := &TaskTicket{Key: "CND-12", Rev: 7, Plan: testPlanID, PlanN: 2, Digest: p.Digest,
		Files: []string{".conductor/ticket.md", ".conductor/package.json", ".conductor/attachments/01-Spec.png", ".conductor/attachments/02-my_notes.txt"}}
	if !reflect.DeepEqual(tf.Ticket, wantTicket) {
		t.Fatalf("task.json's ticket: %+v", tf.Ticket)
	}
	if !strings.Contains(md, "## Your ticket\n\nThis run works for the ticket CND-12, \"Fix the login loop\", as it stood at its revision 7, when plan revision 2 launched the run.") ||
		!strings.Contains(md, "its 2 attachments are in `.conductor/attachments/`") || !strings.Contains(md, "`task_blocked`") {
		t.Fatalf("task.md's ticket section:\n%s", md)
	}
	if i, j := strings.Index(md, "## Instructions"), strings.Index(md, "## Your ticket"); i < 0 || j < i {
		t.Fatalf("the ticket section is not after the instructions:\n%s", md)
	}
	if opens := store.opens(); len(opens) != 2 || opens[0] != testLaunchID+"/"+sha256Hex(testSpecPNG) {
		t.Fatalf("the store's opens: %v", opens)
	}

	commitResult(t, e, b, "built.txt", "b\n", "built")
	built := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1)
	if files := runGit(t, dir, "ls-tree", "-r", "--name-only", built.Result.Commit); strings.Contains(files, taskDir) {
		t.Fatalf("the result holds the package's files:\n%s", files)
	}
	r := waitAttempt(t, e, run.ID, "check", ledger.AttemptRunning)
	if got := readPackageFiles(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("the review's package files:\n%+v\nwant\n%+v", got, want)
	}
	if _, tf := taskFileOf(t, r); !reflect.DeepEqual(tf.Ticket, wantTicket) {
		t.Fatalf("the review's task.json ticket: %+v", tf.Ticket)
	}
	stopRun(t, e, run.ID)
	if rows, _ := ld.Attempts(t.Context(), run.ID); len(rows) != 2 {
		t.Fatalf("attempts: %+v", rows)
	}

	// A file there already, in a worktree of the attempt's: refused, not
	// written over, and no package error.
	rp, err := readPackage(p.Package)
	if err != nil {
		t.Fatal(err)
	}
	for _, there := range []string{ticketFile, packageFileName, attachmentsDir} {
		wt := t.TempDir()
		if err := os.MkdirAll(filepath.Join(wt, taskDir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(wt, taskDir, there), "mine\n")
		s := newFileStore(testSpecPNG, testNotes)
		err := writeTaskFiles(t.Context(), wt, "", []byte("md"), []byte("{}"), nil, rp, s.open)
		var pe *packageError
		if err == nil || !strings.Contains(err.Error(), "exists in the worktree already") || errors.As(err, &pe) {
			t.Fatalf("%s there already: %v", there, err)
		}
		if b, _ := os.ReadFile(filepath.Join(wt, taskDir, there)); string(b) != "mine\n" {
			t.Fatalf("%s was written over: %q", there, b)
		}
		if len(s.opens()) != 0 {
			t.Fatalf("%s there already: the store was opened", there)
		}
	}
}

// An attachment is written under the name the package derived, never its
// display name, whatever that is; a package whose file is not of the
// derived shape, or names one twice as a folding file system sees them, is
// refused before anything is opened or written.
func TestTheAttemptWritesTheDerivedNames(t *testing.T) {
	base := strings.Repeat("b", 40)
	evil, con := []byte("evil"), []byte("con")
	p := fullPackage("api-sweep-0000000a", base)
	p.Attachments = []PackageAttachment{
		{ID: "a1", Name: "../../outside.png", File: "01-.._.._outside.png", Size: int64(len(evil)), SHA256: sha256Hex(evil)},
		{ID: "a2", Name: "CON", File: "02-CON", Size: int64(len(con)), SHA256: sha256Hex(con)},
	}
	rp, err := readPackage(encodePackage(t, p).Package)
	if err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	s := newFileStore(evil, con)
	if err := writeTaskFiles(t.Context(), wt, "", []byte("md"), []byte("{}"), nil, rp, s.open); err != nil {
		t.Fatal(err)
	}
	var written []string
	if err := filepath.WalkDir(wt, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(wt, path)
			written = append(written, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(written)
	if want := []string{".conductor/.gitignore", ".conductor/attachments/01-.._.._outside.png", ".conductor/attachments/02-CON", ".conductor/package.json", ".conductor/task.json", ".conductor/task.md", ".conductor/ticket.md"}; !slices.Equal(written, want) {
		t.Fatalf("written: %v", written)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(wt), "outside.png")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the display name was written: %v", err)
	}

	for _, files := range [][]string{{"../01-x.png"}, {"x.png"}, {"01-a."}, {"1-a"}, {"01-a/b"}, {"01-" + strings.Repeat("a", 65)}, {"01-Spec.png", "01-spec.png"}} {
		bad := *rp
		bad.p.Attachments = nil
		for i, f := range files {
			bad.p.Attachments = append(bad.p.Attachments, PackageAttachment{ID: fmt.Sprint(i), Name: "n", File: f, Size: 1, SHA256: sha256Hex([]byte("x"))})
		}
		wt := t.TempDir()
		s := newFileStore([]byte("x"))
		err := writeTaskFiles(t.Context(), wt, "", []byte("md"), []byte("{}"), nil, &bad, s.open)
		var pe *packageError
		if !errors.As(err, &pe) {
			t.Fatalf("the files %q: %v", files, err)
		}
		if len(s.opens()) != 0 {
			t.Fatalf("the files %q: the store was opened", files)
		}
		if _, err := os.Lstat(filepath.Join(wt, taskDir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the files %q: something was written: %v", files, err)
		}
	}
}

// An attempt of a run launched from a ticket asks the launcher for its
// member's agent's approved selection, read from the package's plan
// revision, and a continuation's attempts do too; a run no ticket
// launched asks for none. A launcher that refuses it ErrAgentChanged fails
// the attempt at "agent", uncounted, no session started, and pauses the
// run with the words; continued and unpaused, the next attempt is told
// why, has one more attempt, and starts. A planned crew whose member's
// agent the package approves no selection of is refused at the launch.
func TestAnAgentNotTheApprovedOnePausesUncounted(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	e, ld := sqlEngine(t)
	fl := e.launcher.(*fakeLauncher)
	c := repoCrew(dir, work("build", "lead"))
	fl.mu.Lock()
	fl.fail["lead · build #1"] = ErrAgentChanged
	fl.mu.Unlock()
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e, c, p)
	row := ledgerAttempt(t, ld, run.ID, "build", 1, ledger.AttemptFailed)
	words := "the agent claude is not the one plan revision 2 approved: its catalog entry changed since"
	if row.Stage != "agent" || row.Error != words || row.SessionID != "" {
		t.Fatalf("attempt 1: %+v", row)
	}
	if fp := failedPayload(t, ld, run.ID, "build", 1); fp["uncounted"] != true || fp["stage"] != "agent" {
		t.Fatalf("attempt.failed: %v", fp)
	}
	why := "attempt 1 could not start its agent: " + words
	waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
	waitTrue(t, "the pause in the ledger", func() bool { r, _, _ := ld.Run(t.Context(), run.ID); return r.Paused && r.PausedWhy == why })
	if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskReady || ts.Reason != why {
		t.Fatalf("the task: %+v", ts)
	}
	fl.mu.Lock()
	calls := slices.Clone(fl.calls)
	fl.mu.Unlock()
	if len(calls) != 1 || calls[0].agentSHA256 != strings.Repeat("e", 64) {
		t.Fatalf("the launches: %+v", calls)
	}

	// Stopped while paused and continued: the continuation passes over the
	// uncounted attempt as the run did; unpaused, its attempt 2 starts,
	// told why attempt 1 did not, with one attempt more.
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !next.Paused {
		t.Fatalf("the continuation is not paused: %+v", next)
	}
	if err := e.PauseRun(t.Context(), next.ID, false); err != nil {
		t.Fatal(err)
	}
	a2 := waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	md, tf := taskFileOf(t, a2)
	if a2.Attempt.N != 2 || tf.MaxAttempts != maxAttemptsPerTask+1 || tf.Unusable == nil || tf.Unusable.Attempt != 1 || tf.Unusable.Stage != "agent" || tf.Unusable.Error != words {
		t.Fatalf("attempt 2: n %d, task.json %+v", a2.Attempt.N, tf)
	}
	if !strings.Contains(md, "Attempt 1 could not start: it failed at agent: "+words) || strings.Contains(md, "could not be made usable") {
		t.Fatalf("attempt 2's task.md:\n%s", md)
	}
	fl.mu.Lock()
	calls = slices.Clone(fl.calls)
	fl.mu.Unlock()
	if len(calls) != 2 || calls[1].agentSHA256 != strings.Repeat("e", 64) || calls[1].ref.RunID != next.ID {
		t.Fatalf("the continuation's launch: %+v", calls)
	}
	stopRun(t, e, next.ID)

	// A launcher's own words are kept.
	own := fmt.Errorf("the agent claude's command changed since plan revision 2: %w", ErrAgentChanged)
	fl.mu.Lock()
	fl.fail["lead · build #1"] = own
	fl.mu.Unlock()
	mine := launchPlanned(t, e, c, planned(t, "api-sweep-0000000b", base))
	if row := ledgerAttempt(t, ld, mine.ID, "build", 1, ledger.AttemptFailed); row.Stage != "agent" || row.Error != own.Error() {
		t.Fatalf("a launcher's own words: %+v", row)
	}
	stopRun(t, e, mine.ID)

	// A run no ticket launched asks for no selection.
	fl.mu.Lock()
	delete(fl.fail, "lead · build #1")
	fl.mu.Unlock()
	plain := launchManaged(t, e, c)
	pa := waitAttempt(t, e, plain.ID, "build", ledger.AttemptRunning)
	fl.mu.Lock()
	last := fl.calls[len(fl.calls)-1]
	fl.mu.Unlock()
	if last.ref.RunID != plain.ID || last.agentSHA256 != "" {
		t.Fatalf("a run no ticket launched: %+v", last)
	}
	if _, tf := taskFileOf(t, pa); tf.Ticket != nil {
		t.Fatalf("a run no ticket launched names a ticket: %+v", tf.Ticket)
	}
	if _, err := os.Stat(filepath.Join(pa.Attempt.Worktree, taskDir, ticketFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a run no ticket launched has a ticket.md: %v", err)
	}
	stopRun(t, e, plain.ID)

	// A member whose agent the package approves nothing of.
	other := repoCrew(dir, work("build", "lead"))
	other.Members[0].AgentID = "codex"
	if _, release, err := e.LaunchPlanned(t.Context(), other, "", planned(t, "api-sweep-0000000c", base)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "approves no selection") {
		release()
		t.Fatalf("an agent the package does not approve: %v", err)
	} else {
		release()
	}
}

// An attachment that cannot be handed to the attempt, read short, longer
// than the package says, of another content, failing as it is read, or
// that the store cannot open, fails the attempt at "package", uncounted,
// with no session started and no file left of it, and pauses the run with
// why; the store fixed and the run unpaused, the next attempt gets the
// files.
func TestAPackageFileThatDoesNotMatchPausesUncounted(t *testing.T) {
	n := len(testSpecPNG)
	for _, tc := range []struct {
		name  string
		wrong func(s *fileStore)
		words string
	}{
		{"a short read", func(s *fileStore) { s.short = 2 }, fmt.Sprintf("the attachment \"Spec.png\" is %d bytes, not the %d its package names", n-2, n)},
		{"a longer content", func(s *fileStore) { s.files[sha256Hex(testSpecPNG)] = append(slices.Clone(testSpecPNG), 'x') }, fmt.Sprintf("the attachment \"Spec.png\" is more than %d bytes, not the %d its package names", n, n)},
		{"a wrong hash", func(s *fileStore) { s.wrong = true }, "the attachment \"Spec.png\"'s SHA-256 is "},
		{"a failing read", func(s *fileStore) { s.broken = errors.New("disk gone") }, "the attachment \"Spec.png\" could not be read whole: disk gone"},
		{"the store's error", func(s *fileStore) { s.fail = errors.New("the store is closed") }, "the attachment \"Spec.png\" cannot be read from the ticket's store: the store is closed"},
		{"no store", nil, "Conductor has no store to read the package's 1 attachments from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			base := head(t, dir)
			e, ld := sqlEngine(t)
			fl := e.launcher.(*fakeLauncher)
			store := newFileStore(testSpecPNG)
			if tc.wrong != nil {
				store.set(tc.wrong)
				e.UsePackageFiles(store.open)
			} else {
				e.UsePackageFiles(nil)
			}
			c := repoCrew(dir, work("build", "lead"))
			p := planned(t, "api-sweep-0000000a", base)
			run := launchPlanned(t, e, c, p)
			row := ledgerAttempt(t, ld, run.ID, "build", 1, ledger.AttemptFailed)
			if row.Stage != "package" || !strings.Contains(row.Error, tc.words) || row.SessionID != "" {
				t.Fatalf("attempt 1: %+v", row)
			}
			if fp := failedPayload(t, ld, run.ID, "build", 1); fp["uncounted"] != true || fp["stage"] != "package" {
				t.Fatalf("attempt.failed: %v", fp)
			}
			why := "attempt 1 could not be handed its ticket's package: " + row.Error
			waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
			if got := fl.launched(); len(got) != 0 {
				t.Fatalf("sessions started: %v", got)
			}
			wt := filepath.Join(dir, worktreesDir, run.ID, "build.1")
			if _, err := os.Stat(filepath.Join(wt, taskDir, attachmentsDir, "01-Spec.png")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the attachment's file is left: %v", err)
			}

			good := newFileStore(testSpecPNG)
			e.UsePackageFiles(good.open)
			if err := e.PauseRun(t.Context(), run.ID, false); err != nil {
				t.Fatal(err)
			}
			a2 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			if got, want := readPackageFiles(t, a2), wantPackageFiles(t, p, good.files); a2.Attempt.N != 2 || !reflect.DeepEqual(got, want) {
				t.Fatalf("attempt %d's files:\n%+v\nwant\n%+v", a2.Attempt.N, got, want)
			}
			if md, tf := taskFileOf(t, a2); tf.MaxAttempts != maxAttemptsPerTask+1 || tf.Unusable == nil || tf.Unusable.Stage != "package" || !strings.Contains(md, "Attempt 1 could not start: it failed at package") {
				t.Fatalf("attempt 2's task files: %+v\n%s", tf, md)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A retry of a task that failed, its attempts spent, gets the package as
// it was launched, byte for byte the first attempt's.
func TestARetryGetsTheLaunchTimePackage(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	e, _ := sqlEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "fails", Argv: []string{"false"}}}
	p := planned(t, "api-sweep-0000000a", base)
	run := launchPlanned(t, e, repoCrew(dir, build), p)
	var first packageFiles
	for n := 1; n <= maxAttemptsPerTask; n++ {
		a := waitTaskWhere(t, e, run.ID, "build", fmt.Sprintf("attempt %d running", n), func(ts TaskState) bool {
			return ts.Attempt != nil && ts.Attempt.N == n && ts.Attempt.State == ledger.AttemptRunning
		})
		if n == 1 {
			first = readPackageFiles(t, a)
		}
		commitResult(t, e, a, fmt.Sprintf("try%d.txt", n), "x\n", "try")
	}
	waitTask(t, e, run.ID, "build", ledger.TaskFailed, maxAttemptsPerTask)
	if err := e.RetryTask(t.Context(), run.ID, "build"); err != nil {
		t.Fatal(err)
	}
	retry := waitTaskWhere(t, e, run.ID, "build", "the retry running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == maxAttemptsPerTask+1 && ts.Attempt.State == ledger.AttemptRunning
	})
	if got := readPackageFiles(t, retry); !reflect.DeepEqual(got, first) || !reflect.DeepEqual(first, wantPackageFiles(t, p, testFiles.files)) {
		t.Fatalf("the retry's files:\n%+v\nthe first attempt's\n%+v", got, first)
	}
	stopRun(t, e, run.ID)
}

// A continuation's first attempt gets the source run's package files byte
// for byte, and its task.json names the same ticket.
func TestAContinuationsAttemptGetsThePackageFiles(t *testing.T) {
	dir := newRepo(t)
	base := head(t, dir)
	e, _ := sqlEngine(t)
	store := newFileStore(testSpecPNG, testNotes)
	e.UsePackageFiles(store.open)
	p := encodePackage(t, fullPackage("api-sweep-0000000a", base))
	run := launchPlanned(t, e, repoCrew(dir, work("build", "lead")), p)
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	src := readPackageFiles(t, a1)
	_, srcTF := taskFileOf(t, a1)
	stopRun(t, e, run.ID)
	next, err := e.ContinueRun(t.Context(), run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	a := waitAttempt(t, e, next.ID, "build", ledger.AttemptRunning)
	if a.Attempt.N != 2 {
		t.Fatalf("the continuation's attempt: %+v", a.Attempt)
	}
	if got := readPackageFiles(t, a); !reflect.DeepEqual(got, src) || !reflect.DeepEqual(src, wantPackageFiles(t, p, store.files)) {
		t.Fatalf("the continuation's files:\n%+v\nthe source's\n%+v", got, src)
	}
	if _, tf := taskFileOf(t, a); !reflect.DeepEqual(tf.Ticket, srcTF.Ticket) || tf.Ticket == nil || tf.Run != next.ID {
		t.Fatalf("the continuation's task.json ticket: %+v, the source's %+v", tf.Ticket, srcTF.Ticket)
	}
	stopRun(t, e, next.ID)
}

// A package that cannot be handed over, or an agent that is not the
// approved one, at a checkpoint the agent saved is no fault of the
// checkpoint's tree: attempt 1 saves C and its session ends, attempt 2 at
// C fails at "package" or "agent", uncounted, its attempt.failed naming no
// checkpoint, the task's checkpoint kept, and the run paused with the
// package's or the agent's words (not the checkpoint's). Fixed and
// unpaused, attempt 3 starts at C with the checkpoint in its task file,
// in the run and in a continuation made while it was paused, which reads
// the same from the rows.
func TestAPackageOrAgentFailureAtACheckpointKeepsIt(t *testing.T) {
	for _, stage := range []string{"package", "agent"} {
		for _, continued := range []bool{false, true} {
			name := stage + map[bool]string{false: ", in the run", true: ", after a continuation"}[continued]
			t.Run(name, func(t *testing.T) {
				dir := newRepo(t)
				base := head(t, dir)
				e, ld := sqlEngine(t)
				fl := e.launcher.(*fakeLauncher)
				store := newFileStore(testSpecPNG)
				e.UsePackageFiles(store.open)
				run := launchPlanned(t, e, repoCrew(dir, work("build", "lead")), planned(t, "api-sweep-0000000a", base))
				a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
				// Attempt 2 is the one that cannot be handed its package or
				// start its agent.
				var prefix string
				switch stage {
				case "package":
					store.set(func(s *fileStore) { s.fail = errors.New("the store is closed") })
					prefix = "attempt 2 could not be handed its ticket's package: "
				case "agent":
					fl.mu.Lock()
					fl.fail["lead · build #2"] = ErrAgentChanged
					fl.mu.Unlock()
					prefix = "attempt 2 could not start its agent: "
				}
				cp := checkpointAndEnd(t, e, fl, a1)
				row2 := ledgerAttempt(t, ld, run.ID, "build", 2, ledger.AttemptFailed)
				if row2.StartCommit != cp || row2.Stage != stage || row2.SessionID != "" {
					t.Fatalf("attempt 2: %+v", row2)
				}
				if fp := failedPayload(t, ld, run.ID, "build", 2); fp["uncounted"] != true || fp["stage"] != stage || fp["checkpoint"] != nil {
					t.Fatalf("attempt.failed: %v", fp)
				}
				why := prefix + row2.Error
				waitTrue(t, "the run paused", func() bool { r, _ := e.Get(run.ID); return r.Paused && r.PausedWhy == why })
				if ts := taskOf(t, e, run.ID, "build"); ts.State != ledger.TaskReady || ts.Reason != why {
					t.Fatalf("the task: %+v", ts)
				}
				keptAt := func(runID string) (string, *unusable) {
					e.mu.Lock()
					defer e.mu.Unlock()
					task := e.runs[runID].task("build")
					var commit string
					if task.checkpoint != nil {
						commit = task.checkpoint.commit
					}
					var u *unusable
					if task.unusable != nil {
						c := *task.unusable
						u = &c
					}
					return commit, u
				}
				if got, u := keptAt(run.ID); got != cp || u == nil || u.n != 2 || u.checkpoint != "" || u.next != cp {
					t.Fatalf("the task's checkpoint %q (want %q), unusable %+v", got, cp, u)
				}

				runID := run.ID
				if continued {
					stopRun(t, e, run.ID)
					next, err := e.ContinueRun(t.Context(), run.ID, "")
					if err != nil {
						t.Fatal(err)
					}
					if !next.Paused || next.PausedWhy != why {
						t.Fatalf("the continuation's pause: %v %q", next.Paused, next.PausedWhy)
					}
					if got, u := keptAt(next.ID); got != cp || u == nil || u.n != 2 || u.checkpoint != "" || u.next != cp {
						t.Fatalf("the continuation's checkpoint %q (want %q), unusable %+v", got, cp, u)
					}
					runID = next.ID
				}
				store.set(func(s *fileStore) { s.fail = nil })
				fl.mu.Lock()
				delete(fl.fail, "lead · build #2")
				fl.mu.Unlock()
				pauseRun(t, e, runID, false)
				a3 := attemptN(t, e, runID, "build", 3)
				if a3.Attempt.Start != cp {
					t.Fatalf("attempt 3 starts at %s, not at the checkpoint %s", short(a3.Attempt.Start), short(cp))
				}
				md, f := taskFileOf(t, a3)
				if f.Checkpoint == nil || f.Checkpoint.Commit != cp || f.Unusable == nil || f.Unusable.Attempt != 2 || f.Unusable.Stage != stage ||
					(f.Previous != nil && f.Previous.Checkpoint != "") || !strings.Contains(md, "## Where the last attempt got to") ||
					!strings.Contains(md, "Attempt 2 could not start: it failed at "+stage) || strings.Contains(md, "could not be made usable") {
					t.Fatalf("attempt 3's task file: checkpoint %+v previous %+v unusable %+v from %q\n%s", f.Checkpoint, f.Previous, f.Unusable, f.StartFrom, md)
				}
				stopRun(t, e, runID)
			})
		}
	}
}
