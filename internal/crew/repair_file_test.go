package crew

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// A person's repair in the task file, and the size a request may be
// (docs/plans/review-repair.md, RV1 PR-2b).

// updateChangeRequestFixture rewrites testdata/change-request-payload.json
// from repairBytes as it is: `go test ./internal/crew -run
// TestFitChangeRequestAtThePayload -update-change-request-fixture`. The
// web's count of a review draft reads the same file.
var updateChangeRequestFixture = flag.Bool("update-change-request-fixture", false, "rewrite testdata/change-request-payload.json from repairBytes")

// personsRepairFiles are the person's repair goldens: a request at its
// candidate, every kind and intent of change, a check's naming the receipt
// the attempt before failed at; and one of a candidate refused before its
// checks.
func personsRepairFiles() map[string]TaskFile {
	base := goldenTaskFiles()["repair"]
	exit := 1
	cr := &ChangeRequest{Review: "4f1c2d3e5a6b7c8d", By: "Nate", Via: "web", Base: strings.Repeat("9", 40), Changes: []RequestedChange{
		{ID: "a1b2c3d4e5f60718", Kind: "line", Intent: "change", Severity: "major", Path: "parse.go", Side: "candidate", From: 14, To: 16,
			Lines: []string{"func Parse(s string) *Tree {", "\treturn parse(s, 0)", "}"}, Message: "An empty input panics here.", Resolution: "Return an error for an empty input."},
		{ID: "b2c3d4e5f6071829", Kind: "line", Intent: "change", Severity: "nit", Path: "parse.go", Side: "base", From: 3, To: 3, Lines: []string{"// Parse parses `s`."}, Message: "Keep this comment."},
		{ID: "c3d4e5f60718293a", Kind: "check", Intent: "change", Severity: "blocker", Message: "The test fails on an empty input.",
			Check: &RequestedCheck{Receipt: "r-0123456789ab/build/2/check/test/1", Run: "r-0123456789ab", Task: "build", Attempt: 2, Phase: ledger.PhaseCheck, ID: "test", K: 1,
				Subject: strings.Repeat("b", 40), Exit: &exit, Stable: true, LogSHA256: strings.Repeat("f", 64), LogBytes: 120, Tail: "--- FAIL: TestEmpty\npanic: index out of range\n", Class: "code", Chosen: "code", Quote: "panic: index out of range"}},
		{ID: "d4e5f60718293a4b", Kind: "file", Intent: "question", Path: "grammar.md", Side: "candidate", Message: "Is the grammar final?"},
		{ID: "e5f60718293a4b5c", Kind: "general", Intent: "note", Message: "The lexer lands next week."},
	}}
	repair := base
	repair.Attempt, repair.MaxAttempts = 3, maxAttemptsPerTask
	repair.Start, repair.StartFrom = strings.Repeat("b", 40), "the commit Nate asked changes of, in review 4f1c2d3e5a6b7c8d"
	repair.Repair = &TaskRepair{Review: cr.Review, Attempt: 2, Commit: strings.Repeat("b", 40), Summary: "Two gaps in the parser, and a failing test.", Request: cr}
	repair.Previous = &TaskPrevious{Attempt: 2, State: ledger.AttemptFailed, Stage: "check test", Error: "test exited 1",
		Check: &TaskCheck{Receipt: "r-0123456789ab/build/2/check/test/1", Run: "r-0123456789ab", Task: "build", Attempt: 2, Phase: ledger.PhaseCheck, ID: "test", K: 1, Argv: []string{"go", "test", "./..."},
			Subject: strings.Repeat("b", 40), Exit: 1, Stable: true, Tail: "--- FAIL: TestEmpty\n", LogSHA256: strings.Repeat("f", 64), LogBytes: 120, Log: ".conductor/previous/check-test.log"}}

	refused := base
	refused.Attempt, refused.MaxAttempts = 4, maxAttemptsPerTask+1
	refused.Start = strings.Repeat("a", 40)
	refused.StartFrom = "where attempt 3 started: its result, which Nate asked changes of, was refused before its checks"
	refused.Repair = &TaskRepair{Review: cr.Review, Attempt: 3, Commit: strings.Repeat("c", 40), Start: strings.Repeat("a", 40), Request: &ChangeRequest{Review: cr.Review, By: "Nate", Changes: cr.Changes[:1]}}
	refused.Previous = &TaskPrevious{Attempt: 3, State: ledger.AttemptFailed, Stage: "setup deps", Error: "deps exited 1", Result: strings.Repeat("c", 40), Branch: "crew/r-0123456789ab/build.3"}
	return map[string]TaskFile{"person-repair": repair, "person-repair-refused": refused}
}

// A person's request in the repair attempt's task file, against the
// goldens: task.md's "Findings to address" (who asked, the commit and
// the review, the base, where the attempt starts, the summary, the changes,
// questions and notes, each with its id, severity, location, lines, words
// and the resolution asked, a check's receipt and log, and how to say what
// was addressed), and task.json's repair with its request.
func TestPersonsRepairInTheTaskFile(t *testing.T) {
	dir := filepath.Join("testdata", "taskfile")
	for name, f := range personsRepairFiles() {
		md, js, more, err := fitTaskFile(f)
		if err != nil || more != nil {
			t.Fatalf("%s: cut %v, %v", name, more != nil, err)
		}
		got := map[string][]byte{name + ".md": md, name + ".json": js}
		for file, b := range got {
			if *updateTaskFileGoldens {
				if err := os.WriteFile(filepath.Join(dir, file), b, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if !bytes.Equal(b, want) {
				t.Errorf("%s differs from its golden:\n got: %q\nwant: %q", file, b, want)
			}
		}
		var kept TaskFile
		if err := json.Unmarshal(js, &kept); err != nil || !reflect.DeepEqual(kept.Repair.Request.Changes, f.Repair.Request.Changes) {
			t.Fatalf("%s: task.json's request: %+v %v", name, kept.Repair, err)
		}
	}
}

// worstChanges is n changes asked for at every bound fitTaskFile may cut:
// each with its 12 lines of 200 bytes, a resolution and words of 1,000
// characters, on a path of its own.
func worstChanges(n int) []RequestedChange {
	var out []RequestedChange
	for i := range n {
		out = append(out, RequestedChange{ID: fmt.Sprintf("c%02d", i), Kind: "line", Intent: "change", Severity: "minor", Path: fmt.Sprintf("p%02d.go", i), Side: "candidate", From: i + 1, To: i + 12,
			Lines: slices.Repeat([]string{strings.Repeat("l", maxAnchorLine)}, maxAnchorLines), Message: strings.Repeat("m", maxFindingText), Resolution: strings.Repeat("r", maxFindingText)})
	}
	return out
}

// Cut to fit: a person's changes lose their lines first, then their
// resolutions, then their words past 200 characters, each from the last,
// into task-more.json by id; their ids, kinds, intents, severities, paths,
// sides and lines' numbers stay, and so does the summary.
func TestPersonsRepairCutToFit(t *testing.T) {
	f := goldenTaskFiles()["repair"]
	f.Instructions = strings.Repeat("i", 24<<10)
	cr := &ChangeRequest{Review: "rv", By: "Nate", Changes: worstChanges(maxFindings)}
	f.Repair = &TaskRepair{Review: "rv", Attempt: 2, Commit: strings.Repeat("b", 40), Summary: strings.Repeat("s", maxSummary), Request: cr}
	md, js, more, err := fitTaskFile(f)
	if err != nil || more == nil || len(md) > maxTaskFile || len(js) > maxTaskFile {
		t.Fatalf("task.md %d, task.json %d bytes, cut %v: %v", len(md), len(js), more != nil, err)
	}
	var kept TaskFile
	var cut TaskMore
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(more, &cut); err != nil {
		t.Fatal(err)
	}
	at := func(prefix string) int {
		return slices.IndexFunc(kept.Cut, func(s string) bool { return strings.HasPrefix(s, prefix) })
	}
	lines, res, words := at("the lines of "), at("the resolutions asked of "), at("the words past 200 characters of ")
	if lines < 0 || res < lines || words < res || kept.Cut[lines] != "the lines of 50 of the requested changes" {
		t.Fatalf("the cuts: %q", kept.Cut)
	}
	if kept.Repair.Summary != f.Repair.Summary || slices.Contains(kept.Cut, "the review's summary") {
		t.Fatal("a person's summary was cut")
	}
	for i, c := range kept.Repair.Request.Changes {
		w := cr.Changes[i]
		if c.ID != w.ID || c.Kind != w.Kind || c.Intent != w.Intent || c.Severity != w.Severity || c.Path != w.Path || c.Side != w.Side || c.From != w.From || c.To != w.To || len(c.Lines) != 0 {
			t.Fatalf("change %d kept as %+v", i, c)
		}
	}
	if len(cut.Changes) != maxFindings || cut.Changes[0].ID != "c00" || !reflect.DeepEqual(cut.Changes[0].Lines, cr.Changes[0].Lines) {
		t.Fatalf("task-more.json's changes: %d, first %+v", len(cut.Changes), cut.Changes[0].ID)
	}
	// What the cut kept of the words, and where the rest is, said in task.md.
	for i, c := range kept.Repair.Request.Changes {
		if c.Message != cr.Changes[i].Message && (c.Message != strings.Repeat("m", maxCutMessage)+"…" || cut.Changes[i].Message != cr.Changes[i].Message) {
			t.Fatalf("change %d's words: %d kept, %d cut", i, len(c.Message), len(cut.Changes[i].Message))
		}
		if c.Resolution != "" && words >= 0 {
			t.Fatalf("change %d kept its resolution while words were cut", i)
		}
	}
	if !strings.Contains(string(md), "Its lines are in the file named under \"Cut to fit\".") || !strings.Contains(string(md), "- `c49` (minor), `p49.go:50–61`, the candidate's side: ") {
		t.Fatalf("task.md:\n%s", md[:4000])
	}
	// The caller's request is as it was.
	if len(cr.Changes[49].Lines) != maxAnchorLines || cr.Changes[49].Resolution == "" {
		t.Fatal("fitTaskFile cut the caller's request")
	}
}

// changeRequestFixture is testdata/change-request-payload.json: requests
// with their size as the run keeps them (repairBytes) and whether they
// fit ledger.MaxPayload, the repair's placeholders named so another
// implementation can measure the same.
type changeRequestFixture struct {
	Note       string `json:"note"`
	MaxPayload int    `json:"maxPayload"`
	Measured   struct {
		Review  string `json:"review"`
		Attempt int    `json:"attempt"`
		Commit  string `json:"commit"`
		Start   string `json:"start"`
	} `json:"measured"`
	Cases []changeRequestCase `json:"cases"`
}

type changeRequestCase struct {
	Name    string        `json:"name"`
	Request ChangeRequest `json:"request"`
	Bytes   int           `json:"bytes"`
	Fits    bool          `json:"fits"`
}

// edgeRequest is a request of n general changes whose words and
// resolutions bring it to exactly want bytes as the run keeps it.
func edgeRequest(t *testing.T, want int) ChangeRequest {
	t.Helper()
	cr := ChangeRequest{Review: "rv", By: "Nate", Changes: []RequestedChange{}}
	for i := range maxFindings {
		cr.Changes = append(cr.Changes, RequestedChange{ID: fmt.Sprintf("c%02d", i), Kind: "general", Intent: "change", Severity: "nit", Message: "a", Resolution: "a"})
	}
	n, err := repairBytes(cr)
	if err != nil {
		t.Fatal(err)
	}
	left := want - n
	for i := range cr.Changes {
		for _, s := range []*string{&cr.Changes[i].Message, &cr.Changes[i].Resolution} {
			k := min(left, maxFindingText-1)
			*s += strings.Repeat("a", k)
			left -= k
		}
	}
	if n, _ := repairBytes(cr); n != want || left != 0 {
		t.Fatalf("the edge request is %d bytes, not %d", n, want)
	}
	return cr
}

// The first measure, the repair as the run keeps it against
// ledger.MaxPayload, at its edge and past it, escapes counted, against
// the fixture the web reads too.
func TestFitChangeRequestAtThePayload(t *testing.T) {
	path := filepath.Join("testdata", "change-request-payload.json")
	var fx changeRequestFixture
	if *updateChangeRequestFixture {
		fx.Note = "Generated by internal/crew's TestFitChangeRequestAtThePayload: a person's change request as Yardmaster's task row keeps its repair (Go's JSON: <, >, &, U+2028 and U+2029 six bytes each), with the review id, the attempt, the commit and the start at the measured placeholders."
		fx.MaxPayload = ledger.MaxPayload
		rp := measuredRepair(ChangeRequest{})
		fx.Measured.Review, fx.Measured.Attempt, fx.Measured.Commit, fx.Measured.Start = rp.review, rp.attempt, rp.commit, rp.start
		small := personsRequest("4f1c2d3e5a6b7c8d")
		escaped := personsRequest("4f1c2d3e5a6b7c8d")
		escaped.Summary = "<&>   é\n\t\"q\""
		escaped.Changes[0].Lines = []string{"if a < b && c > d {", " "}
		fx.Cases = []changeRequestCase{{Name: "small", Request: small}, {Name: "escaped", Request: escaped},
			{Name: "at the edge", Request: edgeRequest(t, ledger.MaxPayload)}, {Name: "one byte past", Request: edgeRequest(t, ledger.MaxPayload+1)}}
		for i := range fx.Cases {
			n, err := repairBytes(fx.Cases[i].Request)
			if err != nil {
				t.Fatal(err)
			}
			fx.Cases[i].Bytes, fx.Cases[i].Fits = n, n <= ledger.MaxPayload
		}
		b, err := encodeTaskJSON(fx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fx = changeRequestFixture{}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if rp := measuredRepair(ChangeRequest{}); fx.MaxPayload != ledger.MaxPayload || fx.Measured.Review != rp.review || fx.Measured.Attempt != rp.attempt || fx.Measured.Commit != rp.commit || fx.Measured.Start != rp.start {
		t.Fatalf("the fixture's bounds: %+v", fx)
	}
	edge := 0
	for _, tc := range fx.Cases {
		n, err := repairBytes(tc.Request)
		if err != nil || n != tc.Bytes {
			t.Fatalf("%s: %d bytes, the fixture says %d (%v)", tc.Name, n, tc.Bytes, err)
		}
		err = FitChangeRequest(repoCrew(t.TempDir(), work("build", "lead")), nil, "build", tc.Request)
		switch {
		case tc.Fits && err != nil:
			t.Fatalf("%s (%d bytes) refused: %v", tc.Name, n, err)
		case !tc.Fits && (!errors.Is(err, ErrChangeRequestTooLarge) || !strings.Contains(err.Error(), "as the run keeps them")):
			t.Fatalf("%s (%d bytes): %v", tc.Name, n, err)
		}
		if n == ledger.MaxPayload || n == ledger.MaxPayload+1 {
			edge++
		}
	}
	if edge != 2 || len(fx.Cases) != 4 {
		t.Fatalf("the fixture has %d cases, %d at the edge", len(fx.Cases), edge)
	}
}

// clefs is n bytes of a four-byte character, which JSON keeps as it is.
func clefs(n int) string { return strings.Repeat("\U0001D11E", n/4) }

// pathChanges is n file changes, each on a path of 1,024 bytes of its own.
func pathChanges(n int) []RequestedChange {
	var out []RequestedChange
	for i := range n {
		out = append(out, RequestedChange{ID: fmt.Sprintf("p%02d", i), Kind: "file", Intent: "change", Severity: "minor", Path: fmt.Sprintf("%02d", i) + strings.Repeat("p", maxFindingPath-2), Side: "candidate", Message: "Split it."})
	}
	return out
}

// textCrew is a crew in dir whose task build has the goal, its member's
// prompt and its instructions given.
func textCrew(t *testing.T, dir, goal, prompt, instructions string) Crew {
	t.Helper()
	build := work("build", "lead")
	build.Instructions = instructions
	c := repoCrew(dir, build)
	c.Goal, c.Members[0].Prompt = goal, prompt
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

// edgeOf is the number of pathChanges at which fits first refuses, from 1
// up: the request a change more than the largest accepted.
func edgeOf(t *testing.T, fits func(n int) error) int {
	t.Helper()
	for n := 1; n <= maxFindings; n++ {
		if err := fits(n); err != nil {
			if !errors.Is(err, ErrChangeRequestTooLarge) || !strings.Contains(err.Error(), "the task file would be") {
				t.Fatalf("%d changes: %v", n, err)
			}
			return n
		}
	}
	t.Fatal("no edge within the request's bound")
	return 0
}

// repairWithin launches c, asks for the changes cr of build's result, and
// checks the repair attempt's task.md and task.json are written within
// maxTaskFile each; with planned, a run launched from a ticket, whose
// package's files are beside them.
func repairWithin(t *testing.T, e *Engine, c Crew, planned *Planned, cr ChangeRequest) {
	t.Helper()
	var run Run
	if planned != nil {
		run = launchPlanned(t, e, c, *planned)
	} else {
		run = launchManaged(t, e, c)
	}
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, cr); err != nil || rc.Status != ledger.CommandDone {
		t.Fatalf("Request changes of %d changes: %+v %v", len(cr.Changes), rc, err)
	}
	b2 := attemptN(t, e, run.ID, "build", 2)
	for _, name := range []string{"task.md", "task.json"} {
		fi, err := os.Stat(filepath.Join(b2.Attempt.Worktree, taskDir, name))
		if err != nil || fi.Size() > maxTaskFile {
			t.Fatalf("%s: %v bytes, %v", name, fi.Size(), err)
		}
	}
	if _, f := taskFileOf(t, b2); f.Repair == nil || f.Repair.Request == nil || len(f.Repair.Request.Changes) != len(cr.Changes) {
		t.Fatalf("the repair attempt's request: %+v", f.Repair)
	}
	if planned != nil {
		if _, err := os.Stat(filepath.Join(b2.Attempt.Worktree, taskDir, ticketFile)); err != nil {
			t.Fatalf("the package's files: %v", err)
		}
	}
	stopRun(t, e, run.ID)
}

// The second measure counts the task's own text: thirty paths of 1,024
// bytes, which a task file never cuts, beside 40 KiB of instructions are
// refused though their repair is well under the ledger's bound; at the
// edge a request is accepted, and its attempt's task files are written
// within 64 KiB each.
func TestFitChangeRequestWithTheTasksText(t *testing.T) {
	dir := newRepo(t)
	instructions := clefs(maxInstructions * 4)
	c := textCrew(t, dir, "g", clefs(maxTaskText-taskTextBytes("g", "", instructions)), instructions)
	if n := taskTextBytes(c.Goal, c.Members[0].Prompt, c.Tasks[0].Instructions); n < maxTaskText-8 {
		t.Fatalf("the task's text is %d bytes", n)
	}
	cr := ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(30)}
	if n, _ := repairBytes(cr); n >= ledger.MaxPayload {
		t.Fatalf("the repair is %d bytes", n)
	}
	err := FitChangeRequest(c, nil, "build", cr)
	if !errors.Is(err, ErrChangeRequestTooLarge) || !strings.Contains(err.Error(), "with build's own instructions the task file would be") || !strings.Contains(err.Error(), "shorten the notes or their paths") {
		t.Fatalf("thirty paths beside 40 KiB: %v", err)
	}
	n := edgeOf(t, func(n int) error {
		return FitChangeRequest(c, nil, "build", ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n)})
	})
	if n < 2 || n > 30 {
		t.Fatalf("the edge is at %d changes", n)
	}
	e, _, _ := managedEngine(t)
	repairWithin(t, e, c, nil, ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n - 1)})
	// The engine measures too: past the edge, refused with no receipt.
	run := launchManaged(t, e, c)
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	if rc, err := requestChanges(e, run.ID, "rq1", "build", 1, c1, ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n)}); !errors.Is(err, ErrChangeRequestTooLarge) || rc.CommandID != "" {
		t.Fatalf("past the edge: %+v %v", rc, err)
	}
	stopRun(t, e, run.ID)
}

// A request's summary (never cut, up to 4,000 characters, each six bytes
// as JSON at its worst) and a launch package (its title at its cut, its
// attachments' paths at their bound) are counted: near the limit the same
// changes are accepted without them and refused with them, and every
// request accepted writes its repair attempt's task files within 64 KiB
// each, the package's files beside them.
func TestFitChangeRequestCountsTheSummaryAndThePackage(t *testing.T) {
	dir := newRepo(t)
	c := textCrew(t, dir, "g", "", clefs(16<<10))
	runID := "api-sweep-0000000b"
	p := fullPackage(runID, head(t, dir))
	p.Ticket.Title = strings.Repeat("<", 300)
	p.Decisions = nil
	var contents [][]byte
	p.Attachments = nil
	for i := range maxPackageAttachments {
		b := []byte(fmt.Sprintf("attachment %d\n", i))
		contents = append(contents, b)
		p.Attachments = append(p.Attachments, PackageAttachment{ID: fmt.Sprintf("a%d", i), Name: "n", File: fmt.Sprintf("%02d-%s", i+1, strings.Repeat("a", 64)), Size: int64(len(b)), SHA256: sha256Hex(b), ContentType: "text/plain"})
	}
	// U+2028 is six bytes in task.json as in the ledger; "<" six in the
	// ledger alone.
	summary := strings.Repeat(" ", maxSummary)
	with := func(n int) ChangeRequest {
		return ChangeRequest{Review: "rv", By: "Nate", Summary: summary, Changes: pathChanges(n)}
	}
	without := func(n int) ChangeRequest { return ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n)} }
	n := edgeOf(t, func(n int) error { return FitChangeRequest(c, &p, "build", with(n)) })
	if n < 2 {
		t.Fatalf("the edge is at %d changes", n)
	}
	if err := FitChangeRequest(c, nil, "build", without(n)); err != nil {
		t.Fatalf("%d changes with no summary and no package: %v", n, err)
	}
	if err := FitChangeRequest(c, &p, "build", without(n)); err != nil {
		t.Fatalf("%d changes with the package and no summary: %v", n, err)
	}
	e, _ := sqlEngine(t)
	e.UsePackageFiles(newFileStore(contents...).open)
	planned := encodePackage(t, p)
	repairWithin(t, e, c, &planned, with(n-1))
	repairWithin(t, e, c, nil, without(n))
}

// The crew's goal and the member's prompt are counted beside the task's
// instructions: two crews with the same task, one with a short goal and
// prompt, the other with both filling the task's text to its bound: near
// the limit the first accepts what the second refuses, and every request
// accepted writes its repair attempt's task files within 64 KiB each.
func TestFitChangeRequestCountsTheGoalAndTheRole(t *testing.T) {
	dir := newRepo(t)
	instructions := clefs(24 << 10)
	short := textCrew(t, dir, "g", "p", instructions)
	room := maxTaskText - taskTextBytes("", "", instructions)
	long := textCrew(t, dir, clefs(maxGoal*4), clefs(room-maxGoal*4-2-8), instructions)
	if n := taskTextBytes(long.Goal, long.Members[0].Prompt, instructions); n < maxTaskText-16 || n > maxTaskText {
		t.Fatalf("the long crew's text is %d bytes", n)
	}
	n := edgeOf(t, func(n int) error {
		return FitChangeRequest(long, nil, "build", ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n)})
	})
	if n < 2 {
		t.Fatalf("the edge is at %d changes", n)
	}
	cr := ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n)}
	if err := FitChangeRequest(short, nil, "build", cr); err != nil {
		t.Fatalf("the short crew refused %d changes: %v", n, err)
	}
	e, _, _ := managedEngine(t)
	repairWithin(t, e, short, nil, cr)
	repairWithin(t, e, long, nil, ChangeRequest{Review: "rv", By: "Nate", Changes: pathChanges(n - 1)})
}
