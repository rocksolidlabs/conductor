package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/quoted"
)

// quotedMarker is the plain text the fake tracker's item holds in its
// title, its description and its steps to reproduce.
const quotedMarker = "SCRATCH-LINE-FROM-OUTSIDE"

// checkQuotedFile fails t unless every line of the file that holds the
// marker stands inside a mark: a quoted line (Conductor's words, then the
// text to the line's end) or a line of a block, which begins with the
// prefix between a block's opening line and its closing line.
func checkQuotedFile(t *testing.T, name string, file []byte) {
	t.Helper()
	inBlock, closing := false, ""
	for i, l := range strings.Split(string(file), "\n") {
		switch {
		case inBlock && l == closing:
			inBlock = false
			continue
		case !inBlock && strings.Contains(l, " quoted from ") && strings.Contains(l, ": "+quoted.Untrusted+". Each line of the quote begins with "):
			inBlock = true
			_, rest, _ := strings.Cut(l, `the quote ends at the line "`)
			closing = strings.TrimSuffix(rest, `".`)
			continue
		}
		if !strings.Contains(l, quotedMarker) {
			continue
		}
		head, _, _ := strings.Cut(l, quotedMarker)
		line := (strings.HasPrefix(l, "The title quoted from ") || strings.HasPrefix(l, "Its title quoted from ")) && strings.Contains(head, " ("+quoted.Untrusted+")"+quoted.LineHead)
		if !line && !(inBlock && (l == ">" || strings.HasPrefix(l, quoted.Prefix))) {
			t.Fatalf("%s: line %d holds the tracker's text outside every mark: %q", name, i+1, l)
		}
	}
	if inBlock {
		t.Fatalf("%s: a block is not closed", name)
	}
}

// A ticket imported through the fake tracker, its title, description and
// steps holding the marker, approved and launched with no label: the run
// is named by the key alone, and in the files the engine writes for the
// build attempt and for the reviewing member's attempt every line that
// holds the marker stands inside a mark; package.json flags the title and
// the description as written there, and task.md says so of it.
func TestAnImportedTicketsFilesQuoteItsText(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) {
		c.Members = append(c.Members, crew.Member{Name: "reviewer", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}})
		c.Tasks = append(c.Tasks, crew.Task{ID: "review", Member: "reviewer", Kind: crew.TaskReview, Instructions: "Review it.", Needs: []string{"build"}})
	})
	l.srv.limiter = newRateLimiter(1e6, 1e6)
	f := fakeGitHub(t)
	useFake(l.srv, f)
	if status, data, _ := l.saveToken(t, "", plantedToken); status != http.StatusOK {
		t.Fatalf("the token: %d %s", status, data)
	}
	items := newGHItems(f)
	items.set(41, ghItem{Node: "I_kwDOitem41", Title: quotedMarker + " title", Updated: t0,
		Body: quotedMarker + " one\r\n\r\n[Source]: https://h.invalid/" + quotedMarker + "\r\n\r\n### Steps to reproduce\r\n\r\n1. " + quotedMarker + " step\r\n"})
	key := entityOf(t, l.importItem(t, "CND", "https://github.com/octo/app/issues/41", opID(), http.StatusCreated, nil), "ticket")["key"].(string)
	plan := entityOf(t, l.propose(t, key, map[string]any{"crew": l.crew.ID}, http.StatusCreated, ""), "plan")["id"].(string)
	l.expect(t, "POST", "/api/dispatch/plans/"+plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate"}, http.StatusOK, "")
	out := l.expect(t, "POST", "/api/dispatch/tickets/"+key+"/launch", map[string]any{"opId": opID(), "plan": plan}, http.StatusCreated, "")
	runID := entityOf(t, out, "launch")["runId"].(string)
	if label := entityOf(t, out, "run")["label"]; label != key {
		t.Fatalf("the run's label: %v", label)
	}
	check := func(task string) {
		t.Helper()
		for _, name := range []string{"ticket.md", "task.md"} {
			checkQuotedFile(t, task+"'s "+name, l.treeFile(t, runID, task, 1, name))
		}
		if md := string(l.treeFile(t, runID, task, 1, "ticket.md")); !strings.Contains(md, quotedMarker+" title") || !strings.Contains(md, quotedMarker+" one") {
			t.Fatalf("%s's ticket.md does not hold the tracker's text:\n%s", task, md)
		}
		var pkg struct {
			Source struct {
				TitleFromGitHub, DescriptionFromGitHub bool
			}
		}
		if err := json.Unmarshal(l.treeFile(t, runID, task, 1, "package.json"), &pkg); err != nil || !pkg.Source.TitleFromGitHub || !pkg.Source.DescriptionFromGitHub {
			t.Fatalf("%s's package.json: %+v %v", task, pkg, err)
		}
		if md := string(l.treeFile(t, runID, task, 1, "task.md")); !strings.Contains(md, "its `source` marks as GitHub's are the same report") {
			t.Fatalf("%s's task.md does not say what package.json holds", task)
		}
	}
	a1 := runningAttempt(t, l.testEnv, runID, "build", 1)
	check("build")
	commitFile(t, a1["worktree"].(string), "a.txt", "one\n")
	l.submit(t, a1["sessionId"].(string))
	ledgerAttemptIn(t, l.srv, runID, "build", 1, ledger.AttemptSucceeded)
	runningAttempt(t, l.testEnv, runID, "review", 1)
	check("review")
}
