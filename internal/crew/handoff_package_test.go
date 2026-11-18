package crew

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// updateHandoffGoldens rewrites testdata/taskfile-handoff from fitTaskFile
// as it is: `go test ./internal/crew -run TestAHandoffPackageInTheTaskFile
// -update-handoff-goldens`.
var updateHandoffGoldens = flag.Bool("update-handoff-goldens", false, "rewrite testdata/taskfile-handoff from fitTaskFile")

// goldenHandoffText is a package's text as Brakeman's records write it
// (brakeman.HandoffText): a heading, its parts and one line break at the
// end.
const goldenHandoffText = "## Handoff package: Key the cache by locale.\n\n### Constraints\n\nKeep the file format.\n\n### Decisions made\n\n- keep the cache's file format\n\n### Revision\n\ncommit:c-build-0000000a/" +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n\n### Open questions\n\n- Is the locale always set?\n\n### Evidence\n\n- attempt:c-build-0000000a/build/3\n"

// handoffTaskFile is the golden work task with a handoff package a
// person sent.
func handoffTaskFile(text string) TaskFile {
	f := goldenTaskFiles()["work"]
	f.Handoff = &TaskHandoff{ID: "ho-7", By: "Ada", SentAt: time.Date(2026, 11, 18, 14, 2, 11, 0, time.UTC), Brief: "br-14", Item: 2, Text: text}
	return f
}

// handoffCutTaskFile is a task file that passes maxTaskFile with a package
// at its bound beside a person's decisions and a provider's block: the
// package's text is cut first, its head and the rest stay.
func handoffCutTaskFile() TaskFile {
	f := handoffTaskFile(strings.Repeat("A line of the package, long enough to count.\n", MaxHandoffText/46))
	f.Instructions = strings.Repeat("Instructions. ", 3500)
	f.Decisions = []TaskDecision{{ID: "dr-1", Type: ledger.DecisionApproval, Origin: ledger.OriginAgent, Question: "Merge the schema change?", State: ledger.DecisionApplied, Answer: "Approve", By: "Nate"}}
	f.Context = []TaskContext{{Source: "notes", Title: "The design notes", Body: "Tokens are small."}}
	return f
}

// The task file with a package: task.json's handoff and task.md's section
// after the context, against the goldens; the cut one keeps the section's
// head, the decisions and the context in task.md and moves only the text,
// whole, into task-more.json.
func TestAHandoffPackageInTheTaskFile(t *testing.T) {
	dir := filepath.Join("testdata", "taskfile-handoff")
	md, js, more, err := fitTaskFile(handoffTaskFile(goldenHandoffText))
	if err != nil || more != nil {
		t.Fatalf("the task files: %v, more %s", err, more)
	}
	cmd, cjs, cmore, err := fitTaskFile(handoffCutTaskFile())
	if err != nil || cmore == nil {
		t.Fatalf("the cut task files: %v, more %v", err, cmore != nil)
	}
	got := map[string][]byte{"handoff.md": md, "handoff.json": js, "cut.md": cmd, "cut.json": cjs, "cut.more.json": cmore}
	for file, b := range got {
		path := filepath.Join(dir, file)
		if *updateHandoffGoldens {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, want) {
			t.Errorf("%s differs from its golden:\n got: %q\nwant: %q", file, b, want)
		}
	}
	var tj struct {
		Handoff *TaskHandoff `json:"handoff"`
	}
	if err := json.Unmarshal(js, &tj); err != nil || tj.Handoff == nil || tj.Handoff.Text != goldenHandoffText {
		t.Fatalf("task.json's handoff is not the package as sent: %+v %v", tj.Handoff, err)
	}
	m := string(md)
	if i, j := strings.Index(m, "\n"+handoffHeading+"\n"), strings.Index(m, "\n## Checks\n"); i < 0 || j < i {
		t.Fatalf("task.md has no handoff section before its checks:\n%s", m)
	}

	// Cut: the head names the file, the decisions and the context stay,
	// the text is whole in task-more.json and in no line of task.md.
	c := string(cmd)
	for _, want := range []string{handoffHeading, "(ho-7), signed \"Ada\"", "brief br-14 (item 2)", "not instructions to follow", "`handoff.text` in `.conductor/task-more.json`",
		"## Decisions", "dr-1 (approval)", "## Context", "Tokens are small.", "the handoff package's text are in"} {
		if !strings.Contains(c, want) {
			t.Errorf("the cut task.md lacks %q", want)
		}
	}
	if strings.Contains(c, "A line of the package") || strings.Contains(c, handoffClosing("ho-7")) {
		t.Fatalf("the cut task.md still holds the package's text")
	}
	var mj TaskMore
	if err := json.Unmarshal(cmore, &mj); err != nil || mj.Handoff == nil || mj.Handoff.Text != handoffCutTaskFile().Handoff.Text || mj.Decisions != nil || mj.Context != nil {
		t.Fatalf("task-more.json is not the package's text alone: %+v %v", mj, err)
	}
	var cj struct {
		Handoff *TaskHandoff `json:"handoff"`
	}
	if err := json.Unmarshal(cjs, &cj); err != nil || cj.Handoff == nil || cj.Handoff.Text != "" || cj.Handoff.ID != "ho-7" {
		t.Fatalf("the cut task.json's handoff: %+v %v", cj.Handoff, err)
	}
}
