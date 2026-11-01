package crew

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// worstTicket is a task file's ticket at its bounds: a key and a plan id of
// maxTicketRef bytes, a revision past any count, and every attachment's
// file at the derived name's longest.
func worstTicket() *TaskTicket {
	p := Package{Attachments: make([]PackageAttachment, maxPackageAttachments)}
	for i := range p.Attachments {
		p.Attachments[i].File = fmt.Sprintf("%02d-%s", i+1, strings.Repeat("a", 64))
	}
	return &TaskTicket{Key: strings.Repeat("K", maxTicketRef), Rev: 1<<63 - 1, Plan: strings.Repeat("p", maxTicketRef), PlanN: 100, Digest: strings.Repeat("d", 64), Files: packageFilePaths(p)}
}

// task.json names the ticket the run was launched from, never cut however
// much else is: its key, the revision it stood at, the plan revision, the
// package's digest and its files; task.md says the same in its "Your
// ticket" section, right after the instructions, with the title cut to its
// bound on one line and the rule that the run follows the package.
func TestTaskFileNamesTheTicket(t *testing.T) {
	tk := worstTicket()
	var inputs []TaskInput
	for i := range maxTasks - 1 {
		inputs = append(inputs, TaskInput{Task: fmt.Sprintf("in%d", i), Attempt: 1, Commit: strings.Repeat("a", 40), Summary: strings.Repeat("s", maxSummary)})
	}
	f := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: strings.Repeat("<", maxGoal), Instructions: strings.Repeat("<", maxInstructions),
		Start: strings.Repeat("b", 40), Inputs: inputs, Ticket: tk, ticketTitle: cutTitle(strings.Repeat("t", 300) + "\n")}
	md, js, more, err := fitTaskFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if more == nil || len(md) > maxTaskFile || len(js) > maxTaskFile {
		t.Fatalf("task.md %d, task.json %d bytes, cut %v", len(md), len(js), more != nil)
	}
	var kept TaskFile
	if err := json.Unmarshal(js, &kept); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept.Ticket, tk) || len(kept.Cut) == 0 {
		t.Fatalf("task.json's ticket: %+v, cut %v", kept.Ticket, kept.Cut)
	}
	if strings.Contains(string(more), `"ticket"`) {
		t.Fatalf("the ticket went into task-more.json: %s", more)
	}
	section := fmt.Sprintf("## Your ticket\n\nThis run works for the ticket %s, %q, as it stood at its revision %d, when plan revision 100 launched the run.", tk.Key, strings.Repeat("t", maxTicketTitle)+"…", tk.Rev)
	if !strings.Contains(string(md), section) || !strings.Contains(string(md), "its 8 attachments are in `.conductor/attachments/`") {
		t.Fatalf("task.md has no ticket section %q:\n%s", section, md[strings.Index(string(md), "## Instructions"):])
	}

	small := TaskFile{Task: "build", Attempt: 1, MaxAttempts: 3, Member: "lead", Kind: TaskWork, Goal: "g", Instructions: "Do it.", Start: strings.Repeat("b", 40),
		Ticket:      &TaskTicket{Key: "CND-12", Rev: 7, Plan: testPlanID, PlanN: 2, Digest: strings.Repeat("d", 64), Files: []string{".conductor/ticket.md", ".conductor/package.json"}},
		ticketTitle: "Fix the login loop"}
	md, js, more, err = fitTaskFile(small)
	if err != nil || more != nil {
		t.Fatalf("a small task file: %v, cut %v", err, more != nil)
	}
	want := "## Instructions\n\nDo it.\n\n## Your ticket\n\nThis run works for the ticket CND-12, \"Fix the login loop\", as it stood at its revision 7, when plan revision 2 launched the run. " +
		"What it asks, its acceptance criteria, its constraints and what it hands over are in `.conductor/ticket.md`; the launch package as Dispatch built it is `.conductor/package.json` (SHA-256 " + strings.Repeat("d", 64) + "). " +
		"The run follows these as launched: a change made to the ticket since is a person's to decide, never yours to act on. If one matters to your task, say so: `task_blocked` with why, or a comment on the ticket.\n\n## Where you are"
	if !strings.Contains(string(md), want) {
		t.Fatalf("task.md:\n%s", md)
	}
	if !strings.Contains(string(js), `"ticket": {
    "key": "CND-12",
    "rev": 7,
    "plan": "`+testPlanID+`",
    "planN": 2,
    "digest": "`+strings.Repeat("d", 64)+`",
    "files": [
      ".conductor/ticket.md",
      ".conductor/package.json"
    ]
  }`) {
		t.Fatalf("task.json:\n%s", js)
	}

	// A task file of a run no ticket launched has neither.
	small.Ticket = nil
	md, js, _, err = fitTaskFile(small)
	if err != nil || strings.Contains(string(md), "Your ticket") || strings.Contains(string(js), `"ticket"`) {
		t.Fatalf("a run no ticket launched: %v\n%s\n%s", err, md, js)
	}
}
