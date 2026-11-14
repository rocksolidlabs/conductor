package api

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// compareEnv is a server with Dispatch's store whose ticket CND-1
// launched a comparison's run (two candidates, approach-a by cand-a and
// approach-b by cand-b, each reviewed, review-a by rev-a and review-b by
// rev-b), beside an ordinary managed run linked to the same ticket (build
// by lead, docs by writer): approach-a's, approach-b's and build's
// sessions, each with its token. Every run the engine holds is stopped
// when the test ends, before its folders go.
type compareEnv struct {
	*testEnv
	run, plain, launch, plan string
	a, b, member             string
	aTok, bTok, plainTok     string
}

// compareOpts shapes a compareEnv.
type compareOpts struct {
	// identity is the server's identity provider; none for nil.
	identity ext.IdentityProvider
	// ticket is CND-1's title, description and acceptance criteria, when
	// not the env's own.
	ticket map[string]any
	// noDesk opens the server without Brakeman's desk: a file stands
	// where its records' directory would be.
	noDesk bool
	// unlinked leaves the ordinary run unlinked from CND-1.
	unlinked bool
	// beforeLaunch writes on CND-1 before the comparison is launched.
	beforeLaunch func(ce *compareEnv)
	// launched is set as the launcher's seam (launchGate.launched) before
	// the runs launch, and the env then waits for no session: the test
	// does, at its seam.
	launched func(crew.LaunchSpec, string)
}

func newCompareEnv(t *testing.T, o compareOpts) *compareEnv {
	t.Helper()
	var opts []Option
	if o.identity != nil {
		opts = append(opts, withIdentity(o.identity))
	}
	data := filepath.Join(t.TempDir(), "data")
	if o.noDesk {
		if err := os.MkdirAll(data, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, brakeman.Dir), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEnvAgents(t, func(c *config.Config) { c.DataDir = data }, slog.New(slog.NewTextHandler(io.Discard, nil)),
		[]catalog.Agent{readyCat}, opts...)
	if o.noDesk != (e.srv.brakeman == nil) {
		t.Fatalf("Brakeman's desk: %v, want none %v", e.srv.brakeman != nil, o.noDesk)
	}
	// Every refusal is the token's: the per-client limiter would answer
	// some with 429.
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	repo := filepath.Join(e.root, "repo")
	planRepo(t, repo)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": repo})
	tk := map[string]any{"title": "Fast lookup", "description": "Make the lookup fast.", "acceptance": "It is fast."}
	maps.Copy(tk, o.ticket)
	title := tk["title"].(string)
	delete(tk, "title")
	e.newDispatchTicket(t, "CND", title, tk)
	saved, err := e.srv.crews.Create(compareCrewIn(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	plan := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CND-1/plans", map[string]any{"opId": opID(), "crew": saved.ID}, http.StatusCreated, ""), "plan")["id"].(string)
	e.expect(t, "POST", "/api/dispatch/plans/"+plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
	ce := &compareEnv{testEnv: e, plan: plan}
	srv := e.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	if o.beforeLaunch != nil {
		o.beforeLaunch(ce)
	}
	if o.launched != nil {
		srv.launches.setSeams(func(g *launchGate) { g.launched = o.launched })
	}
	srv.runs.SetCompareOpenForTest(true)
	launch := entityOf(t, e.expect(t, "POST", "/api/dispatch/tickets/CND-1/launch", map[string]any{"opId": opID(), "plan": plan}, http.StatusCreated, ""), "launch")
	ce.run, ce.launch = launch["runId"].(string), launch["id"].(string)
	plain, release, err := srv.runs.LaunchNamed(t.Context(), managedCrew(repo), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	ce.plain = plain.ID
	if !o.unlinked {
		e.expect(t, "POST", "/api/dispatch/tickets/CND-1/links", map[string]any{"opId": opID(), "kind": "run", "ref": ce.plain}, http.StatusCreated, "")
	}
	if o.launched == nil {
		ce.a, ce.aTok = ce.session(t, ce.run, "approach-a")
		ce.b, ce.bTok = ce.session(t, ce.run, "approach-b")
		ce.member, ce.plainTok = ce.session(t, ce.plain, "build")
	}
	return ce
}

// compareCrewIn is the env's comparison in repo: two candidates,
// approach-a and approach-b, each reviewed.
func compareCrewIn(t *testing.T, repo string) crew.Crew {
	t.Helper()
	c, err := crew.CompareTemplate(crew.CompareSpec{Name: "Compare", Cwd: repo, Goal: "Make the lookup fast.", Instructions: "Make it fast.",
		Criteria:   []crew.Criterion{{ID: "fast", Text: "The lookup is fast."}},
		Checks:     []crew.Check{{ID: "ok", Argv: []string{"true"}}},
		Candidates: []crew.CandidateSpec{{AgentID: "ready-cat", Approach: "Cache every answer."}, {AgentID: "ready-cat", Approach: "Index the table."}},
		Reviews:    true, ReviewAgent: "ready-cat"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// session is the task's first attempt's session once it runs, and its
// token.
func (ce *compareEnv) session(t *testing.T, run, task string) (string, string) {
	t.Helper()
	sid := runningAttempt(t, ce.testEnv, run, task, 1)["sessionId"].(string)
	return sid, ce.agentToken(sid)
}

// door is a call of one of compareDoors' patterns: the path with the
// session sid and the run, and a body every route refuses after its
// token is taken (an unknown field), so that nothing is written.
func (ce *compareEnv) door(pattern, sid, run string) (method, path string, body []byte) {
	method, path, _ = strings.Cut(pattern, " ")
	for k, v := range map[string]string{"{run}": run, "{task}": "approach-b", "{n}": "1", "{key}": "CND-1", "{did}": "dr-none", "{command}": "c-none", "{id}/{k}": "s-none/1"} {
		path = strings.ReplaceAll(path, k, v)
	}
	path = strings.ReplaceAll(path, "{id}", sid)
	if method != http.MethodGet {
		body = []byte(`{"notAField":1}`)
	}
	return method, path, body
}

// callDoor calls the pattern with the token for the session sid, naming
// sid in X-Conductor-Session as a Brakeman turn's client does.
func (ce *compareEnv) callDoor(t *testing.T, tok, pattern, sid string) (int, []byte, map[string]any) {
	t.Helper()
	method, path, body := ce.door(pattern, sid, ce.run)
	return ce.callAsTurn(t, tok, sid, method, path, body)
}

// laneRefused fails t unless the reply is the refusal of a door closed to
// a comparison's lane: 409 lane, in its one sentence.
func laneRefused(t *testing.T, what string, status int, raw []byte, out map[string]any) {
	t.Helper()
	if status != http.StatusConflict || errorCode(out) != "lane" || apiMessage(out) != laneClosedWords {
		t.Errorf("%s: %d %s, want 409 lane %q", what, status, raw, laneClosedWords)
	}
}

// guardServed reports whether the reply is past the token's guard: neither
// a 401 nor the refusal of a door closed to a comparison's lane.
func guardServed(status int, out map[string]any) bool {
	return status != http.StatusUnauthorized && (errorCode(out) != "lane" || apiMessage(out) != laneClosedWords)
}

// approach-b's session reads its run as its lane: the run's id, name and
// goal, its own lane's members and tasks, each with its own state, the
// block with its own candidate; nothing of approach-a's lane, no state or
// outcome of the whole run and no log, whichever token asks. An ordinary
// run's member reads its whole run as before.
func TestRunStatusShowsOneLane(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	aWorktree := runningAttempt(t, ce.testEnv, ce.run, "approach-a", 1)["worktree"].(string)
	for _, tok := range []string{ce.bTok, adminToken} {
		_, raw, out := ce.callAs(t, tok, "GET", "/api/sessions/"+ce.b+"/run", nil)
		run, _ := out["run"].(map[string]any)
		if out["member"] != "cand-b" || run["id"] != ce.run || run["goal"] != "Make the lookup fast." {
			t.Fatalf("the lane's run: %s", raw)
		}
		if keys := slices.Sorted(maps.Keys(run)); !slices.Equal(keys, []string{"compare", "goal", "id", "members", "name", "tasks"}) {
			t.Fatalf("the lane's run has %v", keys)
		}
		if got := fieldsOf(run["members"], "name"); !slices.Equal(got, []string{"cand-b", "rev-b"}) {
			t.Fatalf("members %v", got)
		}
		if got := fieldsOf(run["tasks"], "id"); !slices.Equal(got, []string{"approach-b", "review-b"}) || fieldsOf(run["tasks"], "state")[0] != "running" {
			t.Fatalf("tasks %v: %s", got, raw)
		}
		cmp, _ := run["compare"].(map[string]any)
		if cands, _ := cmp["candidates"].([]any); len(cands) != 1 || cands[0].(map[string]any)["task"] != "approach-b" || cands[0].(map[string]any)["approach"] != "Index the table." || len(cmp["criteria"].([]any)) != 1 {
			t.Fatalf("the block: %v", cmp)
		}
		for _, other := range []string{"approach-a", "review-a", "cand-a", "rev-a", "Cache every answer.", ce.a, aWorktree} {
			if strings.Contains(string(raw), other) {
				t.Fatalf("the lane's run names %q: %s", other, raw)
			}
		}
	}
	_, raw, out := ce.callAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/run", nil)
	run, _ := out["run"].(map[string]any)
	if _, log := run["log"]; !log || run["state"] != crew.RunRunning || !slices.Equal(fieldsOf(run["tasks"], "id"), []string{"build", "docs"}) || out["member"] != "lead" {
		t.Fatalf("an ordinary run's member: %s", raw)
	}
}

// fieldsOf is the field of each object of a JSON list.
func fieldsOf(list any, field string) []string {
	var out []string
	for _, v := range list.([]any) {
		s, _ := v.(map[string]any)[field].(string)
		out = append(out, s)
	}
	return out
}

// addLink adds a link to CND-1 as the admin and returns its id; with a
// node, a read of a pull request is recorded for it.
func (ce *compareEnv) addLink(t *testing.T, fields map[string]any, node string) string {
	t.Helper()
	body := maps.Clone(fields)
	body["opId"] = opID()
	id := receiptOf(t, ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/links", body, http.StatusCreated, ""))["id"].(string)
	if node != "" {
		read := dispatch.LinkRead{System: "github", Host: "github.com", Kind: dispatch.ExternalChange, NodeID: node, State: "open", HeadSha: strings.Repeat("c", 40), UpdatedAt: time.Now().UTC()}
		if err := ce.srv.dispatch.RecordRead(context.Background(), id, read); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// addComment comments on CND-1 straight in the store, as the actor, and
// returns the comment's id.
func (ce *compareEnv) addComment(t *testing.T, actor, body string) string {
	t.Helper()
	rc, err := ce.srv.dispatch.AddComment(context.Background(), dispatch.Op{Actor: actor, ID: opID()}, "CND-1", body)
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("a comment: %+v %v", rc, err)
	}
	return rc.ID
}

// editComment edits CND-1's comment straight in the store, as the actor.
func (ce *compareEnv) editComment(t *testing.T, actor, id, body string) {
	t.Helper()
	if rc, err := ce.srv.dispatch.EditComment(context.Background(), dispatch.Op{Actor: actor, ID: opID()}, "CND-1", id, body); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("an edit: %+v %v", rc, err)
	}
}

// attach adds an attachment of the name to CND-1 straight in the store
// and returns its id.
func (ce *compareEnv) attach(t *testing.T, name string) string {
	t.Helper()
	rc, a, err := ce.srv.dispatch.AddAttachment(context.Background(), adminOp("Nate"), "CND-1", name, "text/plain", strings.NewReader(name))
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("an attachment: %+v %v", rc, err)
	}
	return a.ID
}

// laneTicketOf is the session's ticket read with the token: its status,
// its raw reply and the reply.
func (ce *compareEnv) laneTicketOf(t *testing.T, tok, sid string) ([]byte, map[string]any) {
	t.Helper()
	status, raw, out := ce.callAs(t, tok, "GET", "/api/sessions/"+sid+"/ticket", nil)
	if status != http.StatusOK || out["ticket"] == nil {
		t.Fatalf("%s's ticket: %d %.300s", sid, status, raw)
	}
	return raw, out
}

// objectKeys is the keys of a JSON object, sorted.
func objectKeys(v any) []string {
	m, _ := v.(map[string]any)
	return slices.Sorted(maps.Keys(m))
}

// laneTicketFields is every field a lane's ticket read may hold, by its
// path in the reply ("[]" for an element of a list).
var laneTicketFields = []string{
	"launch", "launch.id", "launch.rev", "launch.plan", "launch.plan.id", "launch.plan.n", "launch.digest",
	"ticket", "ticket.id", "ticket.key", "ticket.number", "ticket.title", "ticket.description", "ticket.acceptance",
	"ticket.links", "ticket.links[]", "ticket.links[].id", "ticket.links[].kind", "ticket.links[].ref", "ticket.links[].url", "ticket.links[].note",
	"ticket.links[].createdAt", "ticket.links[].createdBy",
	"ticket.attachments", "ticket.attachments[]", "ticket.attachments[].id", "ticket.attachments[].ticketId", "ticket.attachments[].name",
	"ticket.attachments[].size", "ticket.attachments[].sha256", "ticket.attachments[].contentType", "ticket.attachments[].createdAt",
	"ticket.attachments[].createdBy",
	"ticket.comments", "ticket.comments[]", "ticket.comments[].id", "ticket.comments[].author", "ticket.comments[].by", "ticket.comments[].body",
	"ticket.comments[].createdAt", "ticket.comments[].editedAt",
}

// jsonPaths is the path of every value in v, a decoded JSON value, under
// the prefix.
func jsonPaths(v any, prefix string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			jsonPaths(e, p, out)
		}
	case []any:
		for _, e := range x {
			out[prefix+"[]"] = true
			jsonPaths(e, prefix+"[]", out)
		}
	}
}

// outsideWhitelist is every path of the reply that is not one of
// laneTicketFields.
func outsideWhitelist(out map[string]any) []string {
	paths := map[string]bool{}
	jsonPaths(out, "", paths)
	var bad []string
	for p := range paths {
		if !slices.Contains(laneTicketFields, p) {
			bad = append(bad, p)
		}
	}
	slices.Sort(bad)
	return bad
}

// A lane reads its ticket for its run's launch: the statement as launched;
// what was there before the launch and has not changed since; people's
// comments as they are now; nothing else. Before the launch: a person's
// comment the person edits since, an earlier run's agent's comment under
// a name a member of this run has, an agent's comment an agent edits
// since, a delivery whose read is refreshed since and that is given a
// candidate and its verification since, an attachment kept and one
// deleted since. Since: the ticket's title edited, sixty agents' comments,
// three people's comments, delivery links of another lane's branch (as
// written, percent-escaped, and a pull request naming no branch, with its
// read) and an attachment. The reply, whichever token asks, holds the
// statement as launched, the unchanged earlier things, the people's
// comments (the edited one as it is now), no read of a link, nothing
// worked out over the ticket now (its close state, which names those
// deliveries, its status, revision and counts), and no field outside its
// whitelist; none of the sixty pushes anything out. An ordinary run's
// member reads the ticket as the admin's ticket read gives it.
func TestTicketReadHidesOtherLanes(t *testing.T) {
	var before, earlier, edited, keptFile, gone string
	ce := newCompareEnv(t, compareOpts{beforeLaunch: func(ce *compareEnv) {
		ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "a person's, as written"}, http.StatusCreated, "")
		earlier = ce.addComment(t, "agent:cand-a", "an earlier run's")
		edited = ce.addComment(t, "agent:cand-a", "an agent's, as written")
		before = ce.addLink(t, map[string]any{"kind": "delivery", "url": "https://example.test/pull/7"}, "PR_7")
		keptFile = ce.attach(t, "kept.txt")
		gone = ce.attach(t, "before.txt")
	}})
	ce.editComment(t, "agent:cand-a", edited, "an agent's, edited since")
	if rc, err := ce.srv.dispatch.RemoveAttachment(context.Background(), adminOp("Nate"), "CND-1", gone); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("remove: %+v %v", rc, err)
	}
	person := entityOf(t, ce.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")["comments"].([]any)[0].(map[string]any)["id"].(string)
	ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments/"+person+"/edit", map[string]any{"opId": opID(), "body": "a person's, edited since"}, http.StatusOK, "")
	ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"title": "A title since"}}, http.StatusOK, "")
	if err := ce.srv.dispatch.RecordRead(context.Background(), before, dispatch.LinkRead{System: "github", Host: "github.com", Kind: dispatch.ExternalChange, NodeID: "PR_7",
		State: "merged", Merged: true, HeadSha: strings.Repeat("d", 40), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// A candidate and its verification on the delivery's row, as a merge
	// could set them after its add.
	db := openRaw(t, filepath.Join(ce.srv.dispatch.Dir(), dispatch.File))
	if _, err := db.Exec("UPDATE links SET candidate = ?, verified = ? WHERE id = ?", ce.run+"/approach-a/1", `{"run":"`+ce.run+`"}`, before); err != nil {
		t.Fatal(err)
	}
	for i := range 60 {
		ce.addComment(t, []string{"agent:lead", "agent:cand-a", "agent:cand-b"}[i%3], fmt.Sprintf("an agent's, since %d", i))
	}
	ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "a person's, since 0"}, http.StatusCreated, "")
	ce.addComment(t, ext.SubjectActor+"alice", "a person's, since 1")
	ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": "a person's, since 2"}, http.StatusCreated, "")
	branch := "crew/" + ce.run + "/approach-a.1"
	escaped := strings.ReplaceAll(branch, "/", "%2F")
	ce.addLink(t, map[string]any{"kind": "delivery", "url": "https://example.test/compare/main..." + branch}, "")
	ce.addLink(t, map[string]any{"kind": "delivery", "url": "https://example.test/tree/" + escaped}, "")
	ce.addLink(t, map[string]any{"kind": "delivery", "url": "https://example.test/pull/12"}, "PR_12")
	ce.attach(t, "notes.txt")
	// The admin's read of the ticket now: its close state names the
	// candidate the delivery was given since.
	whole := entityOf(t, ce.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")
	if why, _ := json.Marshal(whole["close"]); !strings.Contains(string(why), "approach-a") {
		t.Fatalf("the ticket's close state names no candidate: %s", why)
	}

	for _, tok := range []string{ce.bTok, adminToken} {
		raw, out := ce.laneTicketOf(t, tok, ce.b)
		if bad := outsideWhitelist(out); len(bad) != 0 {
			t.Fatalf("the reply holds %v: %s", bad, raw)
		}
		tk := out["ticket"].(map[string]any)
		if keys := objectKeys(out); !slices.Equal(keys, []string{"launch", "ticket"}) || out["launch"].(map[string]any)["id"] != ce.launch {
			t.Fatalf("the reply: %s", raw)
		}
		if tk["key"] != "CND-1" || tk["title"] != "Fast lookup" || tk["description"] != "Make the lookup fast." || tk["acceptance"] != "It is fast." {
			t.Fatalf("the ticket's statement: %s", raw)
		}
		if got := fieldsOf(tk["comments"], "body"); !slices.Equal(got, []string{"a person's, edited since", "an earlier run's", "a person's, since 0", "a person's, since 1", "a person's, since 2"}) {
			t.Fatalf("the comments: %q", got)
		}
		if got := fieldsOf(tk["comments"], "id"); got[0] != person || got[1] != earlier {
			t.Fatalf("the comments' ids: %v", got)
		}
		if got := fieldsOf(tk["links"], "id"); !slices.Equal(got, []string{before}) || tk["links"].([]any)[0].(map[string]any)["url"] != "https://example.test/pull/7" {
			t.Fatalf("the links: %s", raw)
		}
		if got := fieldsOf(tk["attachments"], "id"); !slices.Equal(got, []string{keptFile}) {
			t.Fatalf("the attachments: %s", raw)
		}
		for _, other := range []string{"approach-a", branch, escaped, "example.test/compare", "example.test/tree", "example.test/pull/12", "notes.txt", "before.txt",
			"an agent's", "as written", "A title since", "PR_7", "merged", "headSha", "candidate", "verified", "linkReads"} {
			if strings.Contains(string(raw), other) {
				t.Fatalf("the lane's ticket names %q: %s", other, raw)
			}
		}
	}

	_, raw, out := ce.callAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/ticket", nil)
	got, _ := json.Marshal(out["ticket"])
	want, _ := json.Marshal(whole)
	if string(got) != string(want) || !strings.Contains(string(raw), branch) {
		t.Fatalf("an ordinary run's member's read:\n%s\nthe admin's:\n%s", got, want)
	}
}

// Two launches of one ticket each read the ticket by their own mark: a
// comment, links and an attachment made between the launches are in
// the second comparison's lane's read and not in the first's.
func TestEachLaunchsLaneReadsByItsOwnLaunch(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	ce.addComment(t, "agent:lead", "between the launches")
	between := ce.addLink(t, map[string]any{"kind": "delivery", "url": "https://example.test/pull/8"}, "")
	ce.attach(t, "between.txt")
	launch := entityOf(t, ce.expect(t, "POST", "/api/dispatch/tickets/CND-1/launch", map[string]any{"opId": opID(), "plan": ce.plan}, http.StatusCreated, ""), "launch")
	second := launch["runId"].(string)
	sid, tok := ce.session(t, second, "approach-b")
	_, first := ce.laneTicketOf(t, ce.bTok, ce.b)
	_, next := ce.laneTicketOf(t, tok, sid)
	if tk := first["ticket"].(map[string]any); len(tk["comments"].([]any)) != 0 || len(tk["links"].([]any)) != 0 || len(tk["attachments"].([]any)) != 0 ||
		first["launch"].(map[string]any)["id"] != ce.launch {
		t.Fatalf("the first launch's lane: %v", first)
	}
	tk := next["ticket"].(map[string]any)
	// The ordinary run's link, made once the first comparison launched, is
	// the second's too.
	if !slices.Equal(fieldsOf(tk["comments"], "body"), []string{"between the launches"}) || !slices.Equal(fieldsOf(tk["links"], "ref"), []string{ce.plain, "https://example.test/pull/8"}) ||
		fieldsOf(tk["links"], "id")[1] != between ||
		!slices.Equal(fieldsOf(tk["attachments"], "name"), []string{"between.txt"}) || next["launch"].(map[string]any)["id"] != launch["id"] {
		t.Fatalf("the second launch's lane: %v", next)
	}
}

// A ticket with as many comments, links and attachments as the store
// admits, each text at its escaped worst, before the launch: the lane's
// read holds the newest fifty comments, every link and every attachment,
// under its stated bound (MaxTicketReply).
func TestALanesTicketReadIsUnderItsBound(t *testing.T) {
	f := worstFiller
	ctx := context.Background()
	var comments []string
	ce := newCompareEnv(t, compareOpts{unlinked: true,
		ticket: map[string]any{"title": f.runes(dispatch.MaxTicketTitle, 0), "description": f.text(dispatch.MaxTicketDescription, 0), "acceptance": f.text(dispatch.MaxAcceptance, 1)},
		beforeLaunch: func(ce *compareEnv) {
			ds := ce.srv.dispatch
			by := f.runes(dispatch.MaxByName, 0)
			for i := range dispatch.MaxLinks {
				l := dispatch.NewLink{Kind: dispatch.LinkDelivery, Ref: f.text(dispatch.MaxLinkRef, i), URL: f.url(dispatch.MaxLinkURL, i), Note: f.runes(dispatch.MaxLinkNote, i)}
				if rc, err := ds.AddLink(ctx, adminOp(by), "CND-1", l, nil); err != nil || rc.Status != dispatch.StatusDone {
					t.Fatalf("link %d: %+v %v", i, rc, err)
				}
			}
			for i := range dispatch.MaxAttachments {
				if rc, _, err := ds.AddAttachment(ctx, adminOp(by), "CND-1", f.file(i), f.text(dispatch.MaxContentType, i), strings.NewReader("x")); err != nil || rc.Status != dispatch.StatusDone {
					t.Fatalf("attachment %d: %+v %v", i, rc, err)
				}
			}
			actor := "agent:" + strings.Repeat("a", dispatch.MaxActor-len("agent:"))
			for i := range dispatch.MaxComments {
				body := f.text(dispatch.MaxCommentBody, i)
				if rc, err := ds.AddComment(ctx, dispatch.Op{Actor: actor, ID: opID(), By: by}, "CND-1", body); err != nil || rc.Status != dispatch.StatusDone {
					t.Fatalf("comment %d: %+v %v", i, rc, err)
				}
				comments = append(comments, body)
			}
		}})
	raw, out := ce.laneTicketOf(t, ce.bTok, ce.b)
	t.Logf("the largest lane's ticket read: %d bytes of %d", len(raw), MaxTicketReply)
	if len(raw) >= MaxTicketReply {
		t.Fatalf("the lane's ticket read is %d bytes, not under %d", len(raw), MaxTicketReply)
	}
	tk := out["ticket"].(map[string]any)
	if got := fieldsOf(tk["comments"], "body"); !slices.Equal(got, comments[len(comments)-dispatch.MaxTicketComments:]) {
		t.Fatalf("%d comments, not the newest %d", len(got), dispatch.MaxTicketComments)
	}
	if len(tk["links"].([]any)) != dispatch.MaxLinks || len(tk["attachments"].([]any)) != dispatch.MaxAttachments {
		t.Fatalf("%d links, %d attachments", len(tk["links"].([]any)), len(tk["attachments"].([]any)))
	}
}

// Every lane's session is refused a ticket comment, 409 lane, whichever
// token asks, and nothing is written; an ordinary run's member comments.
func TestTicketCommentIsRefusedToALane(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	for _, s := range []struct{ sid, tok, actor string }{{ce.a, ce.aTok, "agent:cand-a"}, {ce.b, ce.bTok, "agent:cand-b"}} {
		for _, tok := range []string{s.tok, adminToken} {
			op := opID()
			out := ce.expectAs(t, tok, "POST", "/api/sessions/"+s.sid+"/ticket/comments", map[string]any{"opId": op, "body": "mine is better"}, http.StatusConflict, "lane")
			if msg := apiMessage(out); msg != "a candidate of a comparison does not comment on its ticket before the selection: say it in your result's summary" {
				t.Fatalf("the refusal: %q", msg)
			}
			if _, found, err := ce.srv.dispatch.Receipt(context.Background(), s.actor, op); err != nil || found {
				t.Fatalf("a receipt for the refused comment: %v %v", found, err)
			}
		}
	}
	if cs := entityOf(t, ce.expect(t, "GET", "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")["comments"].([]any); len(cs) != 0 {
		t.Fatalf("the ticket's comments: %v", cs)
	}
	ce.expectAs(t, ce.plainTok, "POST", "/api/sessions/"+ce.member+"/ticket/comments", map[string]any{"opId": opID(), "body": "built"}, http.StatusCreated, "")
}

// agentScopes are the scopes of the operations an agent's token calls.
var agentScopes = []string{"session", "task", "ticket", "observe", "act"}

// registeredPatterns is every pattern the server's mux is given: the
// literal patterns of the package's HandleFunc and Handle calls, and
// Dispatch's routes.
func registeredPatterns(t *testing.T, s *Server) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{decisionRoutePattern: true}
	for _, r := range s.dispatchRoutes() {
		seen[r.pattern] = true
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "HandleFunc" || sel.Sel.Name == "Handle") {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					seen[strings.Trim(lit.Value, "`\"")] = true
				}
			}
			return true
		})
	}
	return slices.Sorted(maps.Keys(seen))
}

// Every route an agent's token may call has its rule in compareDoors, and
// the table answers a lane as it says, entry by entry: each entry is such
// a route (the guide's operations of the agent's scopes, the granted
// acts' routes, Brakeman's shared reads); the decision, crew, link and
// capacity routes are refused by name, so a door that would serve them
// fails here and not only in the calls below; a lane's own token is served
// every own and lane entry and refused every other, 409 lane in one
// sentence; the same token on another lane's session is refused every
// entry, 401, as a token the route does not take; and a route the server
// registers that the table does not list answers an ordinary run's
// member's agent token as it answers a token that is nobody's.
func TestEveryAgentDoorIsListed(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	routes := map[string]bool{}
	for _, op := range operationsTable() {
		if slices.Contains(agentScopes, op.Scope) && op.Path != "" {
			routes[op.Method+" "+op.Path] = true
		}
	}
	for p := range actRoutes {
		routes[p] = true
	}
	for _, sr := range brakemanReads {
		routes[sr.pattern] = true
	}
	for p := range routes {
		if _, ok := compareDoors[p]; !ok {
			t.Errorf("%s has no entry in compareDoors", p)
		}
	}
	for p := range compareDoors {
		if !routes[p] {
			t.Errorf("compareDoors' %s is no route an agent's token calls", p)
		}
	}
	for _, p := range []string{"POST /api/sessions/{id}/decisions", "GET /api/sessions/{id}/decisions", "GET /api/sessions/{id}/decisions/{did}",
		"POST /api/sessions/{id}/decisions/{did}/withdraw", "POST /api/sessions/{id}/crew", "POST /api/sessions/{id}/run/members", "POST /api/sessions/{id}/links/agent",
		"GET /api/capacity"} {
		if rule, ok := compareDoors[p]; !ok || rule != doorRefused {
			t.Errorf("%s is %v, not refused", p, rule)
		}
	}

	for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
		status, raw, out := ce.callDoor(t, ce.bTok, p, ce.b)
		switch rule := compareDoors[p]; {
		case rule == doorRefused:
			laneRefused(t, p+" for its lane", status, raw, out)
		case !guardServed(status, out):
			t.Errorf("%s: approach-b's token on its session is refused: %d %s", p, status, raw)
		}
		if status, raw, _ := ce.callDoor(t, ce.bTok, p, ce.a); status != http.StatusUnauthorized {
			t.Errorf("%s: approach-b's token on approach-a's session: %d %s", p, status, raw)
		}
	}

	nobody := "agent-token-of-no-session"
	for _, p := range registeredPatterns(t, ce.srv) {
		if _, listed := compareDoors[p]; listed {
			continue
		}
		method, path, body := ce.door(p, ce.member, ce.plain)
		if !strings.HasPrefix(path, "/") {
			method, path = http.MethodGet, p
		}
		got, raw, _ := ce.callAsTurn(t, ce.plainTok, ce.member, method, path, body)
		want, wantRaw, _ := ce.callAsTurn(t, nobody, ce.member, method, path, body)
		if got != want {
			t.Errorf("%s is not in compareDoors and takes an agent's token: %d %.200s, a token of no session %d %.200s", p, got, raw, want, wantRaw)
		}
	}
}

// The session read and the files route take no agent token: a candidate's
// and an ordinary run's member's are refused 401, as every agent's.
func TestTheSessionReadsTakeNoAgentToken(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	for _, s := range []struct{ sid, tok string }{{ce.a, ce.aTok}, {ce.b, ce.bTok}, {ce.member, ce.plainTok}} {
		for _, path := range []string{"/api/sessions/" + s.sid, "/api/sessions/" + s.sid + "/files?path=README"} {
			ce.expectAs(t, s.tok, "GET", path, nil, http.StatusUnauthorized, "unauthorized")
		}
	}
}

// A session is a lane's from its launch: held by the launcher after its
// session exists and before the engine records its attempt, approach-b's
// session is refused every door the table refuses, 409 lane, reads its
// lane's run and ticket and is refused a ticket comment with nothing
// written, as once it runs; build's session, held the same way, is no
// lane's, and is answered as any session.
func TestALaneIsALaneFromItsLaunch(t *testing.T) {
	held := make(chan string, 2)
	release := make(chan struct{})
	ce := newCompareEnv(t, compareOpts{launched: func(spec crew.LaunchSpec, id string) {
		if spec.Ref.Attempt == 1 && (spec.Ref.Task == "approach-b" || spec.Ref.Task == "build") {
			held <- spec.Ref.Task + " " + id
			<-release
		}
	}})
	var once bool
	unhold := func() {
		if !once {
			once = true
			close(release)
		}
	}
	t.Cleanup(unhold)
	ids := map[string]string{}
	for range 2 {
		task, id, _ := strings.Cut(<-held, " ")
		ids[task] = id
	}
	b, member := ids["approach-b"], ids["build"]
	bTok, plainTok := ce.agentToken(b), ce.agentToken(member)
	for _, sid := range []string{b, member} {
		if run, _, ok := ce.srv.runs.MemberOf(sid); ok {
			t.Fatalf("%s is the engine's already, of %s", sid, run)
		}
	}
	walk := func(when string) {
		t.Helper()
		for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
			status, raw, out := ce.callDoor(t, bTok, p, b)
			if compareDoors[p] == doorRefused {
				laneRefused(t, when+": "+p, status, raw, out)
			} else if !guardServed(status, out) {
				t.Errorf("%s: %s refused approach-b's token: %d %s", when, p, status, raw)
			}
			if status, raw, out := ce.callDoor(t, plainTok, p, member); errorCode(out) == "lane" {
				t.Errorf("%s: %s answered build's token as a lane's: %d %s", when, p, status, raw)
			}
		}
		_, raw, out := ce.callAs(t, bTok, "GET", "/api/sessions/"+b+"/run", nil)
		if run, _ := out["run"].(map[string]any); run == nil || out["member"] != "cand-b" || !slices.Equal(fieldsOf(run["tasks"], "id"), []string{"approach-b", "review-b"}) {
			t.Errorf("%s: approach-b's run: %s", when, raw)
		}
		_, raw, out = ce.callAs(t, bTok, "GET", "/api/sessions/"+b+"/ticket", nil)
		if keys := objectKeys(out); !slices.Equal(keys, []string{"launch", "ticket"}) {
			t.Errorf("%s: approach-b's ticket: %s", when, raw)
		}
		op := opID()
		ce.expectAs(t, bTok, "POST", "/api/sessions/"+b+"/ticket/comments", map[string]any{"opId": op, "body": "mine"}, http.StatusConflict, "lane")
		if _, found, err := ce.srv.dispatch.Receipt(context.Background(), "agent:cand-b", op); err != nil || found {
			t.Errorf("%s: a receipt for the refused comment: %v %v", when, found, err)
		}
	}
	walk("held")
	// build's session is no member of its run yet: the run's read answers
	// it 404 no_run, its ticket's 404 no_ticket, as on main.
	ce.expectAs(t, plainTok, "GET", "/api/sessions/"+member+"/run", nil, http.StatusNotFound, "no_run")
	ce.expectAs(t, plainTok, "GET", "/api/sessions/"+member+"/ticket", nil, http.StatusNotFound, "no_ticket")
	unhold()
	runningAttempt(t, ce.testEnv, ce.run, "approach-b", 1)
	runningAttempt(t, ce.testEnv, ce.plain, "build", 1)
	walk("running")
	ce.expectAs(t, plainTok, "GET", "/api/sessions/"+member+"/run", nil, http.StatusOK, "")
}

// A comparison's session whose run the engine no longer holds (stopped
// and forgotten, as an ended run is evicted) cannot be told its lane: its
// own token is refused every door, 409 lane, and its run and ticket read
// refuse the admin's token too, never the whole run or ticket. build's
// session, its run forgotten the same way, is answered as on main: 404
// no_run, 404 no_ticket, and no door answers it as a lane's.
func TestALaneWhoseRunIsGoneIsRefusedEveryDoor(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	for _, run := range []string{ce.run, ce.plain} {
		if err := ce.srv.runs.Stop(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		ce.srv.runs.ForgetForTest(run)
	}
	for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
		status, raw, out := ce.callDoor(t, ce.bTok, p, ce.b)
		laneRefused(t, p, status, raw, out)
		if status, raw, out := ce.callDoor(t, ce.plainTok, p, ce.member); errorCode(out) == "lane" {
			t.Errorf("%s answered build's token as a lane's: %d %s", p, status, raw)
		}
	}
	for _, path := range []string{"/run", "/ticket"} {
		status, raw, out := ce.callAs(t, adminToken, "GET", "/api/sessions/"+ce.b+path, nil)
		laneRefused(t, "the admin's "+path, status, raw, out)
	}
	ce.expectAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/run", nil, http.StatusNotFound, "no_run")
	ce.expectAs(t, ce.plainTok, "GET", "/api/sessions/"+ce.member+"/ticket", nil, http.StatusNotFound, "no_ticket")
}

// A lane's own token on a door closed to its lane is answered 409 lane at
// once: through a walk of every such door, the identity provider is asked
// about no token. The same token on another session is answered as on
// main, 401, and as a live token of the server's own it is never asked of
// the provider (ownToken).
func TestALaneRefusalAsksNoIdentityProvider(t *testing.T) {
	idp := &tokenIdentity{}
	ce := newCompareEnv(t, compareOpts{identity: idp})
	asked := len(idp.askedTokens())
	for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
		if compareDoors[p] != doorRefused {
			continue
		}
		status, raw, out := ce.callDoor(t, ce.bTok, p, ce.b)
		laneRefused(t, p, status, raw, out)
	}
	if got := idp.askedTokens()[asked:]; len(got) != 0 {
		t.Fatalf("the provider was asked %d times", len(got))
	}
	ce.expectAs(t, ce.bTok, "POST", "/api/sessions/"+ce.member+"/decisions", map[string]any{}, http.StatusUnauthorized, "unauthorized")
	ce.expectAs(t, ce.bTok, "POST", "/api/sessions/"+ce.member+"/crew", map[string]any{}, http.StatusUnauthorized, "unauthorized")
	if got := idp.askedTokens()[asked:]; len(got) != 0 {
		t.Fatalf("the provider was asked %d times about a token of the server's own", len(got))
	}
}

// A lane's session guide lists only the operations the table serves it,
// and tells it to ask its questions with ask; an ordinary run's member's
// guide lists the decision tools as before.
func TestALaneIsGuidedOnlyToWhatItIsServed(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	ops := map[string]string{}
	for _, op := range operationsTable() {
		ops[op.Name] = op.Method + " " + op.Path
	}
	read := func(tok, sid string) (may, rules []string) {
		ov := entityOf(t, ce.expectAs(t, tok, "GET", "/api/sessions/"+sid+"/guide", nil, http.StatusOK, ""), "overview")
		for _, v := range ov["may"].([]any) {
			may = append(may, v.(string))
		}
		for _, v := range ov["rules"].([]any) {
			rules = append(rules, v.(string))
		}
		return may, rules
	}
	may, rules := read(ce.bTok, ce.b)
	for _, name := range may {
		if compareDoors[ops[name]] == doorRefused {
			t.Errorf("the lane's guide lists %s (%s)", name, ops[name])
		}
	}
	if !slices.Contains(may, "ask") || !slices.Contains(may, "task_result") || rules[len(rules)-1] != "A question goes to a person with ask." {
		t.Fatalf("the lane's guide: %v %v", may, rules)
	}
	may, _ = read(ce.plainTok, ce.member)
	if !slices.Contains(may, "decision_request") || !slices.Contains(may, "link") {
		t.Fatalf("an ordinary member's guide: %v", may)
	}
}

// A lane's member mints no link to its session (409 lane), and no route
// its token is served answers it a credential for a viewer's connection;
// an ordinary run's member mints one.
func TestALaneGetsNoViewerCredential(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	status, raw, out := ce.callAs(t, ce.bTok, "POST", "/api/sessions/"+ce.b+"/links/agent", map[string]any{})
	laneRefused(t, "links/agent", status, raw, out)
	for _, p := range slices.Sorted(maps.Keys(compareDoors)) {
		if compareDoors[p] == doorRefused {
			continue
		}
		method, path, _ := ce.door(p, ce.b, ce.run)
		var body any
		switch p {
		case "POST /api/sessions/{id}/events":
			body = map[string]any{"type": "progress", "message": "halfway"}
		case "POST /api/sessions/{id}/attention":
			body = map[string]any{"state": "working"}
		case "POST /api/sessions/{id}/messages":
			body = map[string]any{"to": "rev-b", "text": "ready soon"}
		case "POST /api/sessions/{id}/open":
			body = map[string]any{"url": "https://example.test/"}
		case "GET /api/sessions/{id}/guide", "GET /api/sessions/{id}/task", "GET /api/sessions/{id}/inbox", "GET /api/sessions/{id}/run", "GET /api/sessions/{id}/ticket":
		default:
			body = map[string]any{"notAField": 1}
		}
		status, raw, out := ce.callAs(t, ce.bTok, method, path, body)
		if !guardServed(status, out) || out["token"] != nil || strings.Contains(string(raw), "/join/") {
			t.Errorf("%s: %d %s", p, status, raw)
		}
	}
	if links := ce.srv.links.ListBySession(ce.b); len(links) != 0 {
		t.Fatalf("approach-b's session has links: %v", links)
	}
	if links := ce.srv.links.ListByRun(ce.run); len(links) != 0 {
		t.Fatalf("the comparison's run has links: %v", links)
	}
	if out := ce.expectAs(t, ce.plainTok, "POST", "/api/sessions/"+ce.member+"/links/agent", map[string]any{}, http.StatusCreated, ""); out["token"] == nil {
		t.Fatalf("an ordinary member's link: %v", out)
	}
}

// One lane's view of its run is the same, byte for byte, while another
// lane's member asks a question, its attempt fails, and its tasks are
// cancelled and end: it holds nothing worked out over the whole run.
func TestALanesViewStaysWhileAnotherLaneEnds(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	view := func() string {
		t.Helper()
		status, raw, _ := ce.callAs(t, ce.bTok, "GET", "/api/sessions/"+ce.b+"/run", nil)
		if status != http.StatusOK {
			t.Fatalf("approach-b's view: %d %s", status, raw)
		}
		return string(raw)
	}
	first := view()
	same := func(step string) {
		t.Helper()
		if got := view(); got != first {
			t.Fatalf("approach-b's view changed as %s:\n%s\n%s", step, first, got)
		}
	}
	ce.expectAs(t, ce.aTok, "POST", "/api/sessions/"+ce.a+"/attention", map[string]any{"state": "needs_input", "message": "Which index?", "ask": true}, http.StatusOK, "")
	same("approach-a's member asks")
	ce.expect(t, "DELETE", "/api/sessions/"+ce.a, nil, http.StatusOK, "")
	ledgerAttemptIn(t, ce.srv, ce.run, "approach-a", 1, ledger.AttemptFailed)
	same("approach-a's attempt fails")
	command(t, ce.testEnv, ce.run, "tasks/approach-a/cancel")
	taskVia(t, ce.testEnv, ce.run, "approach-a", "cancelled", func(task map[string]any) bool { return task["state"] == ledger.TaskCancelled })
	same("approach-a is cancelled")
	command(t, ce.testEnv, ce.run, "tasks/review-a/cancel")
	taskVia(t, ce.testEnv, ce.run, "review-a", "cancelled", func(task map[string]any) bool { return task["state"] == ledger.TaskCancelled })
	same("review-a is cancelled, and approach-a's lane has ended")
}

// tokenReaders are the methods that read a session's agent token: each
// is called only by agentTokenOK and ownToken, in
// internal/api/attention.go.
var tokenReaders = []string{"AgentTokenOK", "HoldsAgentToken"}

// No file under internal/ reads a session's agent token but agentTokenOK,
// where a comparison's lanes are held to compareDoors, and ownToken, which
// says a token is the server's own and admits nothing with it: no call,
// method value or other selector of a token method elsewhere, in this
// package or any other; and the token methods of a server session and a
// hosted one are the ones named here, so a new reader is a new name this
// test knows.
func TestEveryAgentTokenPathIsGuarded(t *testing.T) {
	inGuard, inOwn := 0, 0
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		guard := filepath.ToSlash(path) == "../api/attention.go"
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !slices.Contains(tokenReaders, sel.Sel.Name) {
					return true
				}
				switch {
				case guard && fn != nil && fn.Name.Name == "agentTokenOK" && fn.Recv != nil && sel.Sel.Name == "AgentTokenOK":
					inGuard++
				case guard && fn != nil && fn.Name.Name == "ownToken" && fn.Recv != nil:
					inOwn++
				default:
					t.Errorf("%s: an agent token is read outside agentTokenOK", fset.Position(sel.Pos()))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inGuard != 2 || inOwn != 2 {
		t.Fatalf("agentTokenOK reads the token %d times and ownToken %d, not 2 each (a server session's, a hosted one's)", inGuard, inOwn)
	}
	token := regexp.MustCompile(`(?i)agenttoken`)
	for _, c := range []struct {
		typ  reflect.Type
		want []string
	}{{reflect.TypeFor[*session.Local](), []string{"AgentTokenOK", "HoldsAgentToken", "SetAgentToken"}}, {reflect.TypeFor[*signal.HostedSession](), []string{"AgentTokenOK"}}} {
		var got []string
		for i := range c.typ.NumMethod() {
			if name := c.typ.Method(i).Name; token.MatchString(name) {
				got = append(got, name)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s's token methods are %v, not %v", c.typ, got, c.want)
		}
	}
}

// Three routes no entry names, one through taskSession, one through
// attentionPrincipal and one through agentTokenOK itself, refuse a
// candidate's token 409 lane and serve an ordinary run's member.
func TestAnUnlistedDoorIsClosedToALane(t *testing.T) {
	ce := newCompareEnv(t, compareOpts{})
	s := ce.srv
	mux := http.NewServeMux()
	served := func(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]any{"served": true}) }
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-task", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.taskSession(w, r, false); ok {
			served(w)
		}
	})
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-attention", func(w http.ResponseWriter, r *http.Request) {
		if d, ok := s.reportTarget(w, r); ok {
			if _, ok := s.attentionPrincipal(w, r, d); ok {
				served(w)
			}
		}
	})
	mux.HandleFunc("GET /api/sessions/{id}/unlisted-token", func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.registry.Get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such session")
			return
		}
		switch s.agentTokenOK(r, d) {
		case agentTokenServed:
			served(w)
		case agentTokenLaneClosed:
			writeLaneClosed(w)
		default:
			writeError(w, http.StatusUnauthorized, "unauthorized", "agent token required")
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	call := func(tok, sid, route string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/sessions/"+sid+"/"+route, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	for _, route := range []string{"unlisted-task", "unlisted-attention", "unlisted-token"} {
		status, out := call(ce.aTok, ce.a, route)
		laneRefused(t, route, status, nil, out)
		if got, _ := call(ce.plainTok, ce.member, route); got != http.StatusOK {
			t.Errorf("%s: an ordinary run's member's token answered %d", route, got)
		}
	}
}
