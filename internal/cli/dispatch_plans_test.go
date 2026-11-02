package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// conductor dispatch plans, plan, launch and runs, and the attempt's
// ticket as launched (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-13): against a real server, which proposes from a saved crew at a
// commit of a real repository, approves, and launches a real managed run;
// and against stand-ins for what a real one cannot make cheaply (a
// revision's read at its bound, many pages of runs).

// readyCat is a cat that prints a line first, so a managed attempt's
// session is ready for its task line at once.
var readyCat = catalog.Agent{ID: "ready-cat", Name: "ready cat", Command: []string{"/bin/sh", "-c", "echo ready; exec cat"}}

// gitIn runs git in dir under the test's HOME (clearConductorEnv) and
// fails the test unless it succeeds; it returns the output trimmed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// plannedAPI is a dispatchAPI whose project CND's folder is a git
// repository of one commit (a README), with the ticket CND-1, its
// description and criteria, and a saved managed crew of one member on
// ready-cat with one work task and one check.
type plannedAPI struct {
	*dispatchAPI
	repo, head, crew string
}

// newPlannedAPI makes a plannedAPI; it skips the test without git.
func newPlannedAPI(t *testing.T) *plannedAPI {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	a := newDispatchAPI(t)
	repo := filepath.Join(a.root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "a readme")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	a.admin(t, "POST", "/api/dispatch/projects", map[string]any{"opId": "p1", "key": "CND", "name": "Conductor", "cwd": repo}, http.StatusCreated)
	a.admin(t, "POST", "/api/dispatch/tickets", map[string]any{"opId": "t1", "project": "CND", "title": "Fix the login loop", "description": "It loops.", "acceptance": "It does not."}, http.StatusCreated)
	crewID := a.admin(t, "POST", "/api/crews", map[string]any{
		"name": "Team", "goal": "fix it", "cwd": repo, "where": "server", "isolation": "worktree",
		"members": []any{map[string]any{"name": "lead", "agentId": "ready-cat", "start": map[string]any{"when": "task"}}},
		"tasks": []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it.",
			"checks": []any{map[string]any{"id": "test", "argv": []string{"true"}}}}},
	}, http.StatusCreated)["crew"].(map[string]any)["id"].(string)
	return &plannedAPI{dispatchAPI: a, repo: repo, head: head, crew: crewID}
}

// memberSession waits for the run's one member's session and gives it an
// agent token, as its process gets one: the session and the token.
func (a *plannedAPI) memberSession(t *testing.T, run string) (sid, token string) {
	t.Helper()
	for sid == "" {
		members, _ := a.admin(t, "GET", "/api/runs/"+run, nil, http.StatusOK)["run"].(map[string]any)["members"].([]any)
		if len(members) == 1 {
			sid, _ = members[0].(map[string]any)["sessionId"].(string)
		}
		if sid == "" {
			if t.Context().Err() != nil {
				t.Fatal("the member's session never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	d, ok := a.srv.Registry().Get(sid)
	local, isLocal := d.(*session.Local)
	if !ok || !isLocal {
		t.Fatalf("session %s is not a server session", sid)
	}
	token = "agent-token-" + sid
	local.SetAgentToken(token)
	return sid, token
}

// launched proposes the crew for CND-1, approves it and launches it, all
// through the CLI: the launch's run and its member's session and token.
func (a *plannedAPI) launched(t *testing.T) (run, sid, token string) {
	t.Helper()
	a.mustDispatch(t, "plan", "propose", "CND-1", "--crew", a.crew)
	a.mustDispatch(t, "plan", "approve", "CND-1", "1")
	out := a.mustDispatch(t, "launch", "CND-1")
	m := regexp.MustCompile(`, run ([a-z0-9-]+) `).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("launch: %q", out)
	}
	sid, token = a.memberSession(t, m[1])
	return m[1], sid, token
}

// posts is the POSTs to the path among the requests from the n-th on.
func (a *dispatchAPI) posts(from int, path string) []seenRequest {
	var out []seenRequest
	for _, r := range a.requests(from) {
		if r.method == "POST" && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

// The plan commands against a real server: a revision proposed from a
// saved crew at HEAD with a reference, a comment, constraints, a decision
// and a note, listed and shown whole; refused a launch before it is
// approved; approved with a note, an approval whose reply is lost asked
// once more; launched under a label, a launch whose reply is lost asked
// once more and making one run; the ticket's runs with the launch, its run
// and that nothing changed since; the ticket edited and commented on, the
// runs saying so; a revision based on it; the approved one withdrawn and
// the newest shown in its place.
func TestDispatchPlanCommandsAgainstTheServer(t *testing.T) {
	a := newPlannedAPI(t)
	if out := a.mustDispatch(t, "plans", "CND-1"); out != "CND-1 has no plan revisions\n" {
		t.Fatalf("no revisions: %q", out)
	}
	if out := a.mustDispatch(t, "runs", "CND-1"); out != "CND-1 has no runs\n" {
		t.Fatalf("no runs: %q", out)
	}
	a.mustDispatch(t, "comment", "CND-1", "the", "loop", "is", "in", "the", "<redirect>")
	constraints := filepath.Join(t.TempDir(), "constraints.md")
	if err := os.WriteFile(constraints, []byte("Keep the <API>.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := a.count()
	out := a.mustDispatch(t, "plan", "propose", "CND-1", "--crew", a.crew, "--reference", "./README:1-2=the readme", "--comment", "#1",
		"--constraints-file", constraints, "--decision", "ship:approval:Ship it @ once?@build", "--note", "first try")
	if m := regexp.MustCompile(`^CND-1 r1 proposed: ([0-9a-f]{16}), crew Team at ` + a.head[:12] + `, 1 check expected\napprove it with: conductor dispatch plan approve CND-1 1\n$`).FindStringSubmatch(out); m == nil {
		t.Fatalf("propose: %q", out)
	}
	sent := a.posts(before, "/api/dispatch/tickets/CND-1/plans")
	comment := a.ticket(t, "CND-1").Comments[0]
	if len(sent) != 1 {
		t.Fatalf("the proposals sent: %+v", sent)
	}
	b, _ := json.Marshal(sent[0].body)
	want := fmt.Sprintf(`{"comments":["%s"],"constraints":"Keep the \u003cAPI\u003e.\n","crew":"%s","decisions":[{"before":["build"],"id":"ship","kind":"approval","question":"Ship it @ once?"}],"note":"first try","opId":"%s","references":[{"end":2,"note":"the readme","path":"README","start":1}]}`,
		comment.ID, a.crew, sent[0].body["opId"])
	if string(b) != want {
		t.Fatalf("the proposal's body:\n%s\nwant\n%s", b, want)
	}
	lines := strings.Split(strings.TrimSuffix(a.mustDispatch(t, "plans", "CND-1"), "\n"), "\n")
	if len(lines) != 1 || !regexp.MustCompile(`^r1\t[0-9a-f]{16}\tTeam\t`+a.head[:12]+`\t1 check\tproposed$`).MatchString(lines[0]) {
		t.Fatalf("plans: %q", lines)
	}
	shown := a.mustDispatch(t, "plan", "show", "CND-1")
	for _, want := range []string{"# CND-1 plan revision 1 (r1)\n", "- state: proposed\n", "- base: " + a.head + "\n", "- yolo: off: its members ask before they act\n",
		"- expected checks: 1\n", "## Goal\n\nfix it\n", "## Members\n\n- lead: ready-cat\n", "## Agents\n\n- ready-cat: /bin/sh -c \"echo ready; exec cat\"\n",
		"## Tasks\n\n- build: work, by lead\n  instructions:\n    Build it.\n  check test: true\n", "## References\n\n- README:1-2 (blob ", "): the readme\n",
		"## Handed over\n\n- comment #1\n", "## Constraints\n\nKeep the <API>.\n", "## Decisions a person makes\n\n- ship (approval): Ship it @ once?; before build\n",
		"## Note\n\nfirst try\n"} {
		if !strings.Contains(shown, want) {
			t.Errorf("plan show does not say %q:\n%s", want, shown)
		}
	}
	if code, _, _, err := a.dispatchCmd(t, "", "launch", "CND-1"); code != 1 || err == nil || !strings.Contains(err.Error(), "CND-1 has no approved plan revision") {
		t.Fatalf("a launch before an approval: %d %v", code, err)
	}
	if code, _, _, err := a.dispatchCmd(t, "", "plan", "approve", "CND-1", "2"); code != 1 || err == nil || err.Error() != "CND-1 has no plan revision r2" {
		t.Fatalf("a revision the ticket has not: %d %v", code, err)
	}
	// The approval, its reply lost: asked once more, approved once.
	plan := regexp.MustCompile(`r1\t([0-9a-f]{16})`).FindStringSubmatch(lines[0])[1]
	before = a.count()
	a.loseNext("POST", "/api/dispatch/plans/"+plan+"/approve", 1)
	if out := a.mustDispatch(t, "plan", "approve", "CND-1", "r1", "--note", "Go <ahead>."); out != "CND-1 r1 approved\n" {
		t.Fatalf("approve: %q", out)
	}
	if posts := a.posts(before, "/api/dispatch/plans/"+plan+"/approve"); len(posts) != 2 || posts[0].reply != -1 || posts[0].body["opId"] != posts[1].body["opId"] || posts[1].body["note"] != "Go <ahead>." {
		t.Fatalf("the approval's posts: %+v", posts)
	}
	if line := a.mustDispatch(t, "plans", "CND-1"); !regexp.MustCompile(`\tapproved\tapproved by admin [0-9: -]+ UTC at the ticket's revision \d+\n$`).MatchString(line) {
		t.Fatalf("plans after the approval: %q", line)
	}
	if shown := a.mustDispatch(t, "plan", "show", "CND-1", plan); !strings.Contains(shown, "## Approval note\n\nGo <ahead>.\n") {
		t.Fatalf("plan show by id:\n%s", shown)
	}
	// The launch, its reply lost: asked once more with the same operation,
	// which the server answers from its receipt: one launch, one run.
	rev := a.ticket(t, "CND-1").Rev
	before = a.count()
	a.loseNext("POST", "/api/dispatch/tickets/CND-1/launch", 1)
	out = a.mustDispatch(t, "launch", "CND-1", "--label", "Fix <it>")
	m := regexp.MustCompile(`^CND-1 launched from r1: launch ([0-9a-f]{16}), run (team-[0-9a-f]{8}) "Fix <it>"\n` + regexp.QuoteMeta(a.hs.URL) + `/runs/team-[0-9a-f]{8}\n$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("launch: %q", out)
	}
	launch, run := m[1], m[2]
	if posts := a.posts(before, "/api/dispatch/tickets/CND-1/launch"); len(posts) != 2 || posts[0].reply != -1 || posts[0].body["opId"] != posts[1].body["opId"] || posts[0].body["plan"] != plan {
		t.Fatalf("the launch's posts: %+v", posts)
	}
	runs := a.mustDispatch(t, "runs", "CND-1")
	if strings.Count(runs, "launch ") != 1 || !regexp.MustCompile(`(?m)^launch `+launch+`\tr1\tstarted\t[0-9: -]+ UTC by admin\t"Fix <it>"$`).MatchString(runs) ||
		!strings.Contains(runs, "  member lead: ready-cat ") || !regexp.MustCompile(`(?m)^  run `+run+`\t\w+.*\t"Fix <it>"\tstarted `).MatchString(runs) ||
		!strings.Contains(runs, fmt.Sprintf("  unchanged since the launch, at revision %d\n", rev)) {
		t.Fatalf("runs:\n%s", runs)
	}
	// The ticket edited and commented on after the launch: the runs say
	// so; the run follows its package.
	a.admin(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": "e1", "set": map[string]any{"description": "It loops twice."}}, http.StatusOK)
	a.mustDispatch(t, "comment", "CND-1", "seen", "again")
	now := a.ticket(t, "CND-1").Rev
	if runs := a.mustDispatch(t, "runs", "CND-1"); !strings.Contains(runs, fmt.Sprintf("  now at revision %d: the description changed; 1 comment since\n", now)) {
		t.Fatalf("runs after an edit:\n%s", runs)
	}
	// A revision based on the first; the first withdrawn, which leaves none
	// approved: plan show shows the newest.
	if out := a.mustDispatch(t, "plan", "propose", "CND-1", "--crew", a.crew, "--based-on", "1", "--base", "HEAD"); !strings.HasPrefix(out, "CND-1 r2 proposed: ") {
		t.Fatalf("a second revision: %q", out)
	}
	if out := a.mustDispatch(t, "plan", "withdraw", "CND-1", "1"); out != "CND-1 r1 withdrawn\n" {
		t.Fatalf("withdraw: %q", out)
	}
	if shown := a.mustDispatch(t, "plan", "show", "CND-1"); !strings.HasPrefix(shown, "# CND-1 plan revision 2 (r2)\n") || !strings.Contains(shown, "- revises: r1\n") {
		t.Fatalf("plan show with none approved:\n%s", shown)
	}
	// A delivery naming a candidate the run has not made: the request
	// carries it, and the server's words come back.
	before = a.count()
	cand := run + "/build/1@" + a.head
	if code, _, _, err := a.dispatchCmd(t, "", "link", "CND-1", "--kind", "delivery", "--url", "https://example.com/pull/1", "--candidate", cand); code != 1 || err == nil || !strings.HasPrefix(err.Error(), "invalid: ") {
		t.Fatalf("a candidate the run has not made: %d %v", code, err)
	}
	if posts := a.posts(before, "/api/dispatch/tickets/CND-1/links"); len(posts) != 1 || posts[0].body["candidate"] != cand {
		t.Fatalf("the link's posts: %+v", posts)
	}
}

// What the plan commands refuse before any request: a revision that is
// no number or id, a missing or extra argument, a reference that leaves
// the folder or whose lines are wrong, a decision of the wrong shape or
// kind, a comment that is no number, constraints past their bound, a
// candidate on a link that is no delivery's or of the wrong shape.
func TestDispatchPlanUsageErrors(t *testing.T) {
	a := newDispatchAPI(t)
	long := filepath.Join(t.TempDir(), "long.md")
	if err := os.WriteFile(long, bytes.Repeat([]byte("x"), dispatch.MaxPlanConstraints+1), 0o600); err != nil {
		t.Fatal(err)
	}
	propose := func(args ...string) []string {
		return append([]string{"plan", "propose", "CND-1", "--crew", "team"}, args...)
	}
	for _, args := range [][]string{
		{"plans"}, {"plans", "CND-1", "x"}, {"plans", "cnd-1"}, {"runs"}, {"runs", "CND-1", "x"}, {"launch"}, {"launch", "CND-1", "2"},
		{"launch", "CND-1", "--plan", "x"}, {"launch", "CND-1", "--plan", "0"}, {"launch", "CND-1", "--plan", "+3"},
		{"plan"}, {"plan", "nope"}, {"plan", "show"}, {"plan", "show", "CND-1", "1", "2"}, {"plan", "show", "CND-1", "r"},
		{"plan", "approve", "CND-1"}, {"plan", "approve", "CND-1", "zero"}, {"plan", "withdraw", "CND-1"}, {"plan", "withdraw", "CND-1", "1", "2"},
		{"plan", "propose", "CND-1"}, {"plan", "propose", "CND-1", "--crew", " "},
		propose("--reference", "/etc/passwd"), propose("--reference", "a/../../b"), propose("--reference", `C:\x`), propose("--reference", "a:0"),
		propose("--reference", "a:5-3"), propose("--reference", ":3"), propose("--reference", ""), propose("--reference", "a="+strings.Repeat("n", dispatch.MaxReferenceNote+1)),
		propose("--decision", "x"), propose("--decision", "d:other:q"), propose("--decision", "d:approval:"), propose("--decision", "d:approval:q@Build"),
		propose("--comment", "x"), propose("--comment", "0"), propose("--based-on", "x"), propose("--attachment", " "), propose("--constraints-file", long),
		{"link", "CND-1", "--kind", "external", "--url", "https://example.com", "--candidate", "team-0123abcd/build/1@" + strings.Repeat("a", 40)},
		{"link", "CND-1", "--kind", "delivery", "--url", "https://example.com", "--candidate", "build/1@abc"},
	} {
		if code, _, _, err := a.dispatchCmd(t, "", args[0], args[1:]...); code != 2 || err == nil {
			t.Errorf("%q: exit %d %v, want a usage error", args, code, err)
		}
	}
	if n := a.count(); n != 0 {
		t.Fatalf("the usage errors sent %d requests", n)
	}
	for _, args := range [][]string{{"plan", "-h"}, {"plan", "propose", "-h"}, {"launch", "-h"}} {
		var errOut bytes.Buffer
		if code, err := runDispatch(t.Context(), args, strings.NewReader(""), io.Discard, &errOut); code != 0 || err != nil || !strings.Contains(errOut.String(), "conductor dispatch plan propose KEY-n --crew ID") {
			t.Fatalf("%q: %d %v\n%s", args, code, err, errOut.String())
		}
	}
}

// The reference field's parser reads what the ticket page's reads.
func TestParseReference(t *testing.T) {
	for in, want := range map[string]dispatch.PlanReferenceRequest{
		"README":                             {Path: "README"},
		"./internal/api/session.go:12":       {Path: "internal/api/session.go", Start: 12},
		"internal/api/session.go:12-30=loop": {Path: "internal/api/session.go", Start: 12, End: 30, Note: "loop"},
		"docs/:3-3 = a = b ":                 {Path: "docs", Start: 3, Note: "a = b"},
		"a:b:7":                              {Path: "a:b", Start: 7},
		"a:x":                                {Path: "a:x"},
	} {
		got, err := parseReference(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}
}

// A decision's question may hold ':' and '@'; its tasks follow the last
// '@'.
func TestParseDecision(t *testing.T) {
	for in, want := range map[string]dispatch.PlanDecision{
		"ship:approval:Ship it?":               {ID: "ship", Kind: "approval", Question: "Ship it?", Before: []string{}},
		"ask:clarification:Which: A or B?@a,b": {ID: "ask", Kind: "clarification", Question: "Which: A or B?", Before: []string{"a", "b"}},
		"mail:approval:Write to x@y.com?@":     {ID: "mail", Kind: "approval", Question: "Write to x@y.com?", Before: []string{}},
	} {
		got, err := parseDecision(in)
		if err != nil || got.ID != want.ID || got.Kind != want.Kind || got.Question != want.Question || !slices.Equal(got.Before, want.Before) {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}
}

// An attachment is named by its id, its name, or its name whatever its
// case; two of one name are refused rather than guessed between.
func TestAttachmentIDByName(t *testing.T) {
	at := []dispatch.Attachment{{ID: "0000000000000001", Name: "Spec.png"}, {ID: "0000000000000002", Name: "spec.png"}, {ID: "0000000000000003", Name: "log.txt"}}
	for name, want := range map[string]string{"Spec.png": "0000000000000001", "spec.png": "0000000000000002", "LOG.TXT": "0000000000000003", "0000000000000002": "0000000000000002"} {
		if got, err := attachmentID("CND-1", at, name); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", name, got, err, want)
		}
	}
	if _, err := attachmentID("CND-1", at, "SPEC.PNG"); err == nil || !strings.Contains(err.Error(), "has 2 attachments named") {
		t.Errorf("two of one name: %v", err)
	}
	if _, err := attachmentID("CND-1", at, "nope"); err == nil || err.Error() != `CND-1 has no attachment named "nope"` {
		t.Errorf("none: %v", err)
	}
}

// conductor dispatch plan show takes the largest reply a revision's read
// can make, a body at its bound padded to the server's bound, through its
// limit, and prints the body's parts whole.
func TestDispatchPlanShowReadsTheLargestRevision(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("ab", 8)
	summary := dispatch.PlanSummary{ID: id, TicketID: strings.Repeat("cd", 8), N: 1, CrewID: "team", CrewName: worstChars("crew ", dispatch.MaxCrewName),
		Base: strings.Repeat("0", 40), State: dispatch.PlanApproved, ApprovedAt: at, ApprovedBy: worstChars("by ", dispatch.MaxByName),
		ApprovedNote: worstChars("note ", dispatch.MaxApprovedNote), CreatedAt: at, CreatedBy: "admin"}
	note := worstBytes("note ", dispatch.MaxPlanNote)
	// A body at the store's bound, as stored: its constraints fill what
	// the rest leaves.
	body, err := encodeJSON(map[string]any{"base": summary.Base, "crew": map[string]any{"id": "team", "name": "Team", "members": []any{}}, "agents": []any{},
		"references": []any{}, "attachments": []any{}, "comments": []any{}, "constraints": "", "decisions": []any{}, "note": note})
	if err != nil {
		t.Fatal(err)
	}
	constraints := worstBytes("constraints ", (dispatch.MaxPlanBody-len(body)+len("constraints "))/2)
	body, _ = encodeJSON(map[string]any{"base": summary.Base, "crew": map[string]any{"id": "team", "name": "Team", "members": []any{}}, "agents": []any{},
		"references": []any{}, "attachments": []any{}, "comments": []any{}, "constraints": constraints, "decisions": []any{}, "note": note})
	if len(body) > dispatch.MaxPlanBody || len(body) < dispatch.MaxPlanBody-16 {
		t.Fatalf("the body is %d bytes, not at %d", len(body), dispatch.MaxPlanBody)
	}
	read, size, err := paddedReply(map[string]any{"plan": map[string]any{"id": summary.ID, "ticketId": summary.TicketID, "n": 1, "crewId": "team", "crewName": summary.CrewName,
		"base": summary.Base, "state": summary.State, "approvedAt": at, "approvedBy": summary.ApprovedBy, "approvedNote": summary.ApprovedNote, "createdAt": at, "createdBy": "admin",
		"crewChanged": true, "agentsChanged": true, "expectedChecks": 0, "body": json.RawMessage(body)}}, api.MaxPlanReply)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the largest revision's read: %d bytes, padded to %d", size, api.MaxPlanReply-1)
	list, _ := encodeJSON(map[string]any{"plans": []any{summary}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/tickets/BIG-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(list) })
	mux.HandleFunc("GET /api/dispatch/plans/"+id, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(read) })
	mux.HandleFunc("GET /api/dispatch/tickets/BIG-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ticket":{"id":"cdcdcdcdcdcdcdcd","key":"BIG-1","attachments":[],"comments":[]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"plan", "show", "BIG-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
		t.Fatalf("plan show: %d %v", code, err)
	}
	for i, want := range []string{"# BIG-1 plan revision 1 (r1)\n", summary.CrewName, summary.ApprovedNote, "## Constraints\n\n" + constraints + "\n", "## Note\n\n" + note + "\n",
		"the saved crew was edited or removed since", "an agent resolves otherwise in the catalog now"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("part %d of the largest revision is not printed whole: %.80q", i, want)
		}
	}
}

// conductor dispatch runs follows the pages' next to the end: every entry
// of 23 (15 launches and 8 continuations), over three pages, printed once, in the server's order, each page
// asked for with the cursor the one before gave.
func TestDispatchRunsFollowsEveryPage(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var entries []map[string]any
	for i := range 23 {
		run := fmt.Sprintf("team-%08x", i)
		switch i % 3 {
		case 0:
			entries = append(entries, map[string]any{"kind": "launch", "launch": dispatch.LaunchSummary{ID: fmt.Sprintf("%016x", i), PlanN: 2, RunID: run, Label: "CND-1 Fix it",
				TicketRev: 4, State: dispatch.LaunchStarted, CreatedAt: at, CreatedBy: "Nate", Agents: []dispatch.LaunchAgent{{Member: "lead", Agent: "claude", Version: "2.1.304"}}},
				"run":     map[string]any{"id": run, "label": "CND-1 Fix it", "state": "running", "paused": true, "startedAt": at, "tasks": map[string]int{"running": 1, "done": 2}, "questions": 1, "blockers": []any{map[string]any{"task": "build", "reason": "no token", "by": "lead", "at": at}}},
				"changed": dispatch.LaunchChanges{Fields: []string{"description", "acceptance"}, Comments: 2, Rev: 9}})
		case 1:
			// A continuation carries the launch or the link that holds its
			// chain, as ticketRunEntries answers it: its kind says what it is.
			e := map[string]any{"kind": "run", "run": map[string]any{"id": run, "state": "stopped", "outcome": "succeeded", "startedAt": at, "record": true}, "continues": fmt.Sprintf("team-%08x", i-1)}
			if i%2 == 0 {
				e["launch"] = entries[i-1]["launch"]
			} else {
				e["link"] = dispatch.Link{ID: fmt.Sprintf("l%d", i), Kind: dispatch.LinkRun, Ref: fmt.Sprintf("team-%08x", i-1), CreatedAt: at, CreatedBy: "Nate"}
			}
			entries = append(entries, e)
		default:
			entries = append(entries, map[string]any{"kind": "launch", "launch": dispatch.LaunchSummary{ID: fmt.Sprintf("%016x", i), PlanN: 1, RunID: run, Label: "x", State: dispatch.LaunchFailed, Why: "the agent claude is not installed", CreatedAt: at}})
		}
	}
	var mu sync.Mutex
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1/runs", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawQuery)
		mu.Unlock()
		from := 0
		if b := r.URL.Query().Get("before"); b != "" {
			fmt.Sscanf(b, "c%d", &from)
		}
		to := min(from+10, len(entries))
		out := map[string]any{"entries": entries[from:to]}
		if to < len(entries) {
			out["next"] = fmt.Sprintf("c%d", to)
		}
		b, _ := encodeJSON(out)
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"runs", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
		t.Fatalf("runs: %d %v", code, err)
	}
	if !slices.Equal(asked, []string{"", "before=c10", "before=c20"}) {
		t.Fatalf("the pages asked for: %q", asked)
	}
	text := out.String()
	if n := strings.Count(text, "\nlaunch ") + strings.Count(text[:7], "launch "); n != 15 {
		t.Fatalf("%d launches printed, want 15:\n%s", n, text)
	}
	if n := strings.Count(text, "\nrun team-"); n != 8 || strings.Contains(text, "linked run") {
		t.Fatalf("%d continuations printed, want 8, none as a link:\n%s", n, text)
	}
	for _, want := range []string{
		"launch 0000000000000000\tr2\tstarted\t2026-10-30 12:00 UTC by Nate\t\"CND-1 Fix it\"\n  member lead: claude 2.1.304\n  now at revision 9: the description and the acceptance criteria changed; 2 comments since\n  run team-00000000\trunning, paused\t\"CND-1 Fix it\"\tstarted 2026-10-30 12:00 UTC\n    tasks: 2 done, 1 running\n    questions: 1 of its members wait on a person\n    blocked build: no token (lead, 2026-10-30 12:00 UTC)\n",
		"run team-00000001\tcontinues team-00000000\n  run team-00000001\tstopped (succeeded)\t\"\"\tstarted 2026-10-30 12:00 UTC\tfrom its record\n",
		"\nrun team-00000004\tcontinues team-00000003\n  run team-00000004\t",
		"launch 0000000000000002\tr1\tfailed\t2026-10-30 12:00 UTC by admin\t\"x\"\n  failed: the agent claude is not installed\n",
		"launch 0000000000000015\t",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("runs does not say %q:\n%s", want, text)
		}
	}
}

// conductor dispatch plan show prints every text the attempts' agents are
// handed, the crew's goal, each member's prompt and each task's
// instructions, whole and indented under their item; and names each
// comment handed over by its number, one older than the ticket's read
// carries found through the comments route, page by page, and one found
// nowhere by its id.
func TestDispatchPlanShowSaysTheWorkAndOlderComments(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("ab", 8)
	var comments []dispatch.Comment
	for n := 1; n <= 60; n++ {
		comments = append(comments, dispatch.Comment{ID: fmt.Sprintf("c%015d", n), N: n, Body: "x", CreatedAt: at})
	}
	gone := strings.Repeat("ef", 8)
	body, _ := encodeJSON(map[string]any{"base": strings.Repeat("0", 40),
		"crew": map[string]any{"id": "team", "name": "Team", "goal": "Fix the login loop.\nKeep the API.",
			"members": []any{map[string]any{"name": "lead", "agentId": "claude", "prompt": "You lead.\n\nAsk before <rm>."}},
			"tasks":   []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it.\nThen test it.\x1b[2J"}}},
		"agents": []any{}, "references": []any{}, "attachments": []any{}, "constraints": "", "decisions": []any{}, "note": "",
		"comments": []any{map[string]any{"id": comments[2].ID, "sha256": "x"}, map[string]any{"id": comments[57].ID, "sha256": "x"}, map[string]any{"id": gone, "sha256": "x"}}})
	summary := dispatch.PlanSummary{ID: id, N: 1, CrewID: "team", CrewName: "Team", Base: strings.Repeat("0", 40), State: dispatch.PlanProposed, CreatedAt: at, CreatedBy: "admin"}
	list, _ := encodeJSON(map[string]any{"plans": []any{summary}})
	read, _ := encodeJSON(map[string]any{"plan": map[string]any{"id": id, "n": 1, "crewId": "team", "crewName": "Team", "base": summary.Base, "state": summary.State,
		"createdAt": at, "createdBy": "admin", "expectedChecks": 0, "body": json.RawMessage(body)}})
	ticket, _ := encodeJSON(map[string]any{"ticket": map[string]any{"id": "cdcdcdcdcdcdcdcd", "key": "CND-1", "attachments": []any{}, "comments": comments[10:]}})
	var mu sync.Mutex
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(list) })
	mux.HandleFunc("GET /api/dispatch/plans/"+id, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(read) })
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(ticket) })
	mux.HandleFunc("GET /api/dispatch/tickets/CND-1/comments", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawQuery)
		mu.Unlock()
		var after, limit int
		fmt.Sscan(r.URL.Query().Get("after"), &after)
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		page := comments[min(after, len(comments)):min(after+limit, len(comments))]
		b, _ := encodeJSON(map[string]any{"comments": page})
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"plan", "show", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
		t.Fatalf("plan show: %d %v", code, err)
	}
	text := out.String()
	for _, want := range []string{
		"## Goal\n\nFix the login loop.\nKeep the API.\n",
		"## Members\n\n- lead: claude\n  prompt:\n    You lead.\n\n    Ask before <rm>.\n",
		"## Tasks\n\n- build: work, by lead\n  instructions:\n    Build it.\n    Then test it.\uFFFD[2J\n",
		"## Handed over\n\n- comment #3\n- comment #58\n- comment " + gone + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan show does not say %q:\n%s", want, text)
		}
	}
	// The older comment is looked for page by page from the first; the id
	// found nowhere ends the search at the empty page past the last.
	if !slices.Equal(asked, []string{"after=0&limit=50", "after=50&limit=50", "after=60&limit=50"}) {
		t.Fatalf("the comment pages asked for: %q", asked)
	}
}

// conductor dispatch plan show prints every phase of the project manifest
// the engine runs, with its timeouts and its probes' bounds: an inline
// manifest with setup and services and no verify step, a file-form one
// with every phase, and a crew's legacy setup, which is that phase.
func TestDispatchPlanShowSaysEveryManifestPhase(t *testing.T) {
	clearConductorEnv(t)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	id := strings.Repeat("ab", 8)
	full := map[string]any{
		"prerequisites": []any{map[string]any{"id": "go", "argv": []string{"go", "version"}}},
		"env":           []string{"GOFLAGS", "NPM_TOKEN"},
		"ports":         []string{"web"},
		"setup":         []any{map[string]any{"id": "deps", "argv": []string{"npm", "ci"}, "timeoutSeconds": 900}},
		"services":      []any{map[string]any{"id": "dev", "argv": []string{"npm", "run", "dev"}}},
		"ready": []any{map[string]any{"id": "up", "url": "http://127.0.0.1:3000/", "intervalSeconds": 5, "timeoutSeconds": 120},
			map[string]any{"id": "db", "argv": []string{"pg_isready"}}},
		"preview": "http://127.0.0.1:3000/",
		"verify":  []any{map[string]any{"id": "e2e", "argv": []string{"make", "test-e2e"}}},
		"cleanup": []any{map[string]any{"id": "down", "argv": []string{"docker", "compose", "down"}, "timeoutSeconds": 30}},
	}
	for _, tc := range []struct {
		name string
		body map[string]any // the crew's manifest, setup and the body's manifestFile
		want string
	}{
		{"inline without verify", map[string]any{"manifest": map[string]any{
			"setup":    []any{map[string]any{"id": "deps", "argv": []string{"npm", "ci"}}},
			"services": []any{map[string]any{"id": "dev", "argv": []string{"npm", "run", "dev"}}}}},
			"## Project manifest\n\n- setup deps: npm ci\n- service dev: npm run dev\n"},
		{"file form", map[string]any{"manifest": map[string]any{"file": true},
			"manifestFile": map[string]any{"path": "conductor.manifest.json", "blob": strings.Repeat("b", 40), "manifest": full}},
			"## Project manifest\n\n- from conductor.manifest.json, blob bbbbbbbbbbbb at the base\n" +
				"- prerequisite go: go version\n- setup deps: npm ci (timeout 900 s)\n- service dev: npm run dev\n" +
				"- ready up: http://127.0.0.1:3000/ (every 5 s, timeout 120 s)\n- ready db: pg_isready\n" +
				"- preview: http://127.0.0.1:3000/\n- environment: GOFLAGS, NPM_TOKEN\n- ports: web\n" +
				"- verify e2e: make test-e2e\n- cleanup down: docker compose down (timeout 30 s)\n"},
		{"legacy setup", map[string]any{"setup": []any{map[string]any{"id": "deps", "argv": []string{"npm", "ci"}, "timeoutSeconds": 300}}},
			"## Project manifest\n\n- setup deps: npm ci (timeout 300 s)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crewBody := map[string]any{"id": "team", "name": "Team", "members": []any{},
				"tasks": []any{map[string]any{"id": "build", "member": "lead", "kind": "work", "instructions": "Build it."}}}
			for _, k := range []string{"manifest", "setup"} {
				if v, ok := tc.body[k]; ok {
					crewBody[k] = v
				}
			}
			planBody := map[string]any{"base": strings.Repeat("0", 40), "crew": crewBody, "agents": []any{}, "references": []any{}, "attachments": []any{},
				"comments": []any{}, "constraints": "", "decisions": []any{}, "note": ""}
			if v, ok := tc.body["manifestFile"]; ok {
				planBody["manifestFile"] = v
			}
			body, _ := encodeJSON(planBody)
			summary := dispatch.PlanSummary{ID: id, N: 1, CrewID: "team", CrewName: "Team", Base: strings.Repeat("0", 40), State: dispatch.PlanProposed, CreatedAt: at, CreatedBy: "admin"}
			list, _ := encodeJSON(map[string]any{"plans": []any{summary}})
			read, _ := encodeJSON(map[string]any{"plan": map[string]any{"id": id, "n": 1, "crewId": "team", "crewName": "Team", "base": summary.Base, "state": summary.State,
				"createdAt": at, "createdBy": "admin", "expectedChecks": 0, "body": json.RawMessage(body)}})
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/dispatch/tickets/CND-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(list) })
			mux.HandleFunc("GET /api/dispatch/plans/"+id, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(read) })
			mux.HandleFunc("GET /api/dispatch/tickets/CND-1", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"ticket":{"id":"cdcdcdcdcdcdcdcd","key":"CND-1","attachments":[],"comments":[]}}`))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			var out bytes.Buffer
			if code, err := runDispatch(t.Context(), []string{"plan", "show", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
				t.Fatalf("plan show: %d %v", code, err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("plan show does not say the manifest %q:\n%s", tc.want, out.String())
			}
		})
	}
}

// conductor dispatch launch sends a launch the server answers still
// pending (503 ledger_unavailable carrying the launch) again, the same
// operation, and prints the run the next answer names; still pending
// after launchTries, the error names the launch and conductor dispatch
// runs, and never says to launch again, every try the one operation.
func TestDispatchLaunchResendsAPendingLaunch(t *testing.T) {
	clearConductorEnv(t)
	wait := launchResendWait
	launchResendWait = 0
	t.Cleanup(func() { launchResendWait = wait })
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	plan := strings.Repeat("ab", 8)
	list, _ := encodeJSON(map[string]any{"plans": []any{dispatch.PlanSummary{ID: plan, N: 2, CrewID: "team", CrewName: "Team", State: dispatch.PlanApproved, ApprovedAt: at, CreatedAt: at}}})
	launch := dispatch.LaunchSummary{ID: strings.Repeat("12", 8), PlanID: plan, PlanN: 2, RunID: "team-0123abcd", Label: "CND-1 Fix it", State: dispatch.LaunchPending, CreatedAt: at}
	for _, tc := range []struct {
		name    string
		pending int // how many POSTs are answered pending before the launch starts
	}{{"pending once", 1}, {"pending throughout", launchTries}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var ops []string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/dispatch/tickets/CND-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(list) })
			mux.HandleFunc("POST /api/dispatch/tickets/CND-1/launch", func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				ops = append(ops, body["opId"].(string))
				n := len(ops)
				mu.Unlock()
				rc := map[string]any{"opId": body["opId"], "status": "done", "id": launch.ID}
				if n <= tc.pending {
					w.WriteHeader(http.StatusServiceUnavailable)
					b, _ := encodeJSON(map[string]any{"error": map[string]any{"code": "ledger_unavailable", "message": "the launch is still being started: send it again"}, "receipt": rc, "launch": launch})
					_, _ = w.Write(b)
					return
				}
				started := launch
				started.State = dispatch.LaunchStarted
				w.WriteHeader(http.StatusOK)
				b, _ := encodeJSON(map[string]any{"receipt": rc, "launch": started, "run": map[string]any{"id": started.RunID, "label": started.Label}})
				_, _ = w.Write(b)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			var out bytes.Buffer
			code, err := runDispatch(t.Context(), []string{"launch", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard)
			mu.Lock()
			defer mu.Unlock()
			if len(ops) == 0 || slices.ContainsFunc(ops, func(op string) bool { return op != ops[0] }) {
				t.Fatalf("the operations sent: %q, want one", ops)
			}
			if tc.pending < launchTries {
				if want := "CND-1 launched from r2: launch " + launch.ID + ", run team-0123abcd \"CND-1 Fix it\"\n" + srv.URL + "/runs/team-0123abcd\n"; code != 0 || err != nil || out.String() != want || len(ops) != tc.pending+1 {
					t.Fatalf("launch: %d %v %q, %d posts", code, err, out.String(), len(ops))
				}
				return
			}
			if code != 1 || err == nil || len(ops) != launchTries || out.Len() != 0 ||
				!strings.Contains(err.Error(), "launch "+launch.ID+" from r2 is still pending after 3 tries") || !strings.Contains(err.Error(), "conductor dispatch runs CND-1 shows it") ||
				strings.Contains(err.Error(), "send it again") {
				t.Fatalf("a launch pending throughout: %d %v, %d posts", code, err, len(ops))
			}
		})
	}
}

// A launch the server has answered pending stays named when sending it
// again leaves it unsettled: the resend and its retry both losing their
// replies, or the command interrupted during the resend. The error names
// the launch and conductor dispatch runs and says launching again would be
// a second launch; every post is the one operation.
func TestDispatchLaunchNamesAPendingLaunchItCouldNotSettle(t *testing.T) {
	clearConductorEnv(t)
	wait := launchResendWait
	launchResendWait = 0
	t.Cleanup(func() { launchResendWait = wait })
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	plan := strings.Repeat("ab", 8)
	list, _ := encodeJSON(map[string]any{"plans": []any{dispatch.PlanSummary{ID: plan, N: 2, CrewID: "team", CrewName: "Team", State: dispatch.PlanApproved, ApprovedAt: at, CreatedAt: at}}})
	launch := dispatch.LaunchSummary{ID: strings.Repeat("12", 8), PlanID: plan, PlanN: 2, RunID: "team-0123abcd", Label: "CND-1 Fix it", State: dispatch.LaunchPending, CreatedAt: at}
	for _, tc := range []struct {
		name  string
		posts int    // how many POSTs the command sends in all
		want  string // what the error says of the launch
	}{
		{"lost replies", 3, "launch " + launch.ID + " from r2 was pending, and sending it again had no answer"},
		{"interrupted", 2, "launch " + launch.ID + " from r2 was pending, interrupted while it was sent again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var ops []string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/dispatch/tickets/CND-1/plans", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(list) })
			mux.HandleFunc("POST /api/dispatch/tickets/CND-1/launch", func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				ops = append(ops, body["opId"].(string))
				n := len(ops)
				mu.Unlock()
				switch {
				case n == 1:
					w.WriteHeader(http.StatusServiceUnavailable)
					b, _ := encodeJSON(map[string]any{"error": map[string]any{"code": "ledger_unavailable", "message": "the launch is still being started: send it again"},
						"receipt": map[string]any{"opId": body["opId"], "status": "done", "id": launch.ID}, "launch": launch})
					_, _ = w.Write(b)
				case tc.name == "interrupted":
					// The command is stopped while its resend waits for the
					// answer; the request ends with it.
					cancel()
					<-r.Context().Done()
				default:
					if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
						conn.Close()
					}
				}
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			var out bytes.Buffer
			code, err := runDispatch(ctx, []string{"launch", "CND-1", "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard)
			mu.Lock()
			defer mu.Unlock()
			if len(ops) != tc.posts || slices.ContainsFunc(ops, func(op string) bool { return op != ops[0] }) {
				t.Fatalf("the operations sent: %q, want one, %d times", ops, tc.posts)
			}
			if code != 1 || err == nil || out.Len() != 0 || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "conductor dispatch runs CND-1 shows it") || !strings.Contains(err.Error(), "launching again would be a second launch") {
				t.Fatalf("a pending launch left unsettled: %d %v %q", code, err, out.String())
			}
		})
	}
}

// A delivery link's printout says whether the candidate it names passed
// its checks, as the link recorded it.
func TestDispatchLinkSaysTheCandidatesVerification(t *testing.T) {
	clearConductorEnv(t)
	cand := "team-0123abcd/build/2@" + strings.Repeat("a", 40)
	exit := 0
	verified, _ := json.Marshal(dispatch.Verified{Passed: true, Attempt: 2, Receipts: []dispatch.VerifiedReceipt{{Phase: "check", ID: "test", Where: "native", Exit: &exit, Stable: true}}})
	var sent map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/dispatch/tickets/CND-1/links", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(http.StatusCreated)
		b, _ := encodeJSON(map[string]any{"receipt": map[string]any{"opId": sent["opId"], "status": "done"}, "ticket": dispatch.TicketView{Ticket: dispatch.Ticket{Key: "CND-1"},
			Links: []dispatch.Link{{ID: "l1", Kind: "delivery", Ref: "https://example.com/pull/1", URL: "https://example.com/pull/1", Candidate: cand, Verified: verified}}}})
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	if code, err := runDispatch(t.Context(), []string{"link", "CND-1", "--kind", "delivery", "--url", "https://example.com/pull/1", "--candidate", cand, "--server", srv.URL, "--token", "t"}, strings.NewReader(""), &out, io.Discard); code != 0 || err != nil {
		t.Fatalf("link: %d %v", code, err)
	}
	if want := "CND-1 linked: delivery https://example.com/pull/1, candidate " + cand + ", verified: 1 check passed\n"; out.String() != want || sent["candidate"] != cand {
		t.Fatalf("link: %q, sent %v", out.String(), sent)
	}
	for raw, want := range map[string]string{`{"passed":false,"receipts":[]}`: ", not verified: it ran no check", `{"passed":false,"receipts":[{"id":"t"}]}`: ", not verified: its checks did not all pass", "": ""} {
		if got := verifiedWords(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
