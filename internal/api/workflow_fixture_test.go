package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The first end-to-end workflow's walk (docs/plans/end-to-end.md, WF-1):
// one project, a ticket, a plan revision Brakeman drafts and the operator
// approves, a managed run of an implementing and a reviewing agent, the
// architecture decision the implementer raises, the person who answers
// it from the decide page, the candidate, its checks and reviews, a
// repair, and the ticket's acceptance and delivery. The real server, its
// agents stub agents (ready-cat, a shell line that stays alive; bk,
// Brakeman's scripted agent), the walk speaking for each through its
// session's own token; every provider slot the walk passes through filled
// by a fake of ext/exttest, or none at all (the free walk). Every wait is
// on a record (the ledger's rows, the decision's state, a fake's calls) or
// a step held at a seam the code offers (gatedIdentity, decideSeams),
// never on a clock. None is parallel: each runs git under a HOME of its
// own (planRepo).

// What the walk's later parts wait for, each named as the plan's WF-2
// names it. Its row removes the skip; what a skipped part checks is
// written behind it where main's routes can name it, and the row adds
// only what calls a seam main has not yet (a fake's method).
const (
	waitsContext = "waits for WF-2a: context recorded, the lookup and its absence (after context-seams CT-3)"
	waitsPolicy  = "waits for WF-2b: eligibility by a router's policy (after decision-policy's PR-7a and PR-7)"
	waitsSync    = "waits for WF-2c: the held answer for the owner disconnected (after sync-seams OS-3)"
	// waits1046 is step 9 on the operator's decision page, which main
	// cannot show: a Brakeman card about a decision record (issue 1046).
	waits1046 = "waits for issue 1046: Brakeman's brief on a decision record's item"
)

// The walk's first decision, which the router is set to route before it
// is raised, and the rule the router names.
const (
	walkFirstDecision = "dr-1"
	walkRouteRule     = "the architecture owner"
)

// walkPasses is every pass the walk presents: none of them is ever in a
// log line or a URL (the plan's step 10, never).
var walkPasses = []string{passAdmin, passAna, passBob, passHal}

// passHal is a pass the walk's identity provider admits with the host
// grant alone: a principal without the decide grant. The others are the
// decide tests' (passAdmin the operator, passAna the architecture owner,
// passBob a decide principal the decision was not routed to).
const passHal = "pass.host.hal-3d71"

// The principals the walk's identity provider admits.
var (
	walkOperator = ext.Principal{Subject: "nate", Name: "Nate", Grants: []ext.Grant{ext.GrantAdmin}}
	walkAna      = ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}}
	walkBob      = ext.Principal{Subject: "bob", Name: "Bob", Grants: []ext.Grant{ext.GrantDecide}}
	walkHal      = ext.Principal{Subject: "hal", Name: "Hal", Grants: []ext.Grant{ext.GrantHost}}
)

// The marks the walk's words carry, so a read can tell whose they are.
const (
	walkQuestion  = "Keep the session cookie, or move the login to tokens?"
	walkBlockMark = "pack:wf-1 Login is called from main.go and from the SSO callback."
	walkBriefMark = "BRAKEMANMARK the attempt read the login handler twice"
	walkNoteOne   = "Handle the expired cookie before the redirect."
	walkNoteTwo   = "Keep the redirect to the dashboard after a fresh sign-in."
	walkOwner     = "Ana"
)

// workflowFakes are the walk's providers, one fake of ext/exttest in each
// slot it passes through: the identity provider (as the decide tests'
// gatedIdentity, so a recheck can be held), the decision router, the
// context provider, the notification sink, the sync store and the
// entitlement source. The runtime and media slots keep their free
// defaults: the attempts run on this machine, and nothing uses media.
type workflowFakes struct {
	id      *gatedIdentity
	router  *exttest.FakeRouter
	context *exttest.FakeContext
	sink    *exttest.FakeSink
	sync    *exttest.FakeContinuity
	ents    *exttest.FakeEntitlements
	// late, when set, holds every Context call until the test ends: a
	// provider late past its wait, in the context slot in place of
	// context.
	late *lateContext
}

// lateContext is the walk's context fake held: each Context call is
// recorded by the fake and then waits, ignoring its context, until gate
// closes as the test ends; asked is told each call as it begins.
type lateContext struct {
	*exttest.FakeContext
	gate  chan struct{}
	asked chan ext.ContextQuery
}

func (l *lateContext) Context(ctx context.Context, q ext.ContextQuery) ([]ext.ContextBlock, error) {
	bs, err := l.FakeContext.Context(ctx, q)
	select {
	case l.asked <- q:
	default:
	}
	<-l.gate
	return bs, err
}

func newWorkflowFakes() *workflowFakes {
	fake := exttest.NewFakeIdentity("cloud")
	fake.Add(passAdmin, walkOperator)
	fake.Add(passAna, walkAna)
	fake.Add(passBob, walkBob)
	fake.Add(passHal, walkHal)
	f := &workflowFakes{
		id:      &gatedIdentity{FakeIdentity: fake, asked: map[string]int{}, then: map[string]*ext.Principal{}, hold: map[string]chan struct{}{}, reached: make(chan string, 8)},
		router:  exttest.NewFakeRouter("switchman"),
		context: exttest.NewFakeContext("graph"),
		sink:    exttest.NewFakeSink("push", nil),
		sync:    exttest.NewFakeContinuity("sync"),
		ents:    exttest.NewFakeEntitlements("plans"),
	}
	f.context.SetBlocks([]ext.ContextBlock{{Title: "Callers of Login", Ref: "login.go", Body: walkBlockMark}})
	return f
}

// option registers the fakes, every other slot its free default; a
// server made again (a restart) is handed the same fakes.
func (f *workflowFakes) option() Option {
	var cp ext.ContextProvider = f.context
	if f.late != nil {
		cp = f.late
	}
	return WithExtensions(local.Fill(ext.Providers{
		Identity:      f.id,
		Router:        f.router,
		Context:       []ext.ContextProvider{cp},
		Notifications: []ext.NotificationSink{f.sink},
		Continuity:    f.sync,
		All: []ext.Entry{
			{Kind: ext.KindIdentity, Name: "cloud", Value: f.id},
			{Kind: ext.KindDecisions, Name: "switchman", Value: f.router},
			{Kind: ext.KindContext, Name: "graph", Value: cp},
			{Kind: ext.KindNotifications, Name: "push", Value: f.sink},
			{Kind: ext.KindSync, Name: "sync", Value: f.sync},
			{Kind: ext.KindEntitlements, Name: "plans", Value: f.ents},
		},
	}))
}

// workflow is the walk's fixture: a server over a data directory of the
// test's with the stub agents, its project CND whose folder is a
// repository, and what the walk has made so far.
type workflow struct {
	*testEnv
	fakes *workflowFakes // nil: the free walk, no provider registered
	repo  string
	head  string // the repository's first commit, the run's base
	bk    *bkEnv

	crew   crew.Crew
	plan   string   // the approved plan revision
	run    string   // the managed run
	runs   []string // the run and its continuations, in order
	sid    string   // the implementer's attempt's session
	tok    string   // its token
	did    string   // the architecture decision
	answer string   // the accepted response's id

	// ents is the entitlement source, registered in the free walk too (its
	// slot alone): no code of this repository asks it. logs is the
	// server's log and urls every URL the walk's client asked: neither
	// ever holds a pass.
	ents *exttest.FakeEntitlements
	logs *logBuffer
	urls *urlLog

	// noContext is set where the context provider gives nothing (failing,
	// late, lapsed): the implementer's task carries no block.
	noContext bool

	// eligible is the actors whose accepted response the walk admits now;
	// seen is each decision's furthest fact, which never goes back.
	eligible map[string]bool
	seen     map[string]int
}

// newWorkflow makes the server, its repository and its project; free
// registers no provider.
func newWorkflow(t *testing.T, free bool) *workflow {
	t.Helper()
	if free {
		return newWorkflowWith(t, nil)
	}
	return newWorkflowWith(t, newWorkflowFakes())
}

// newWorkflowWith makes the walk's server with the fakes (nil: none).
func newWorkflowWith(t *testing.T, fakes *workflowFakes) *workflow {
	t.Helper()
	w := &workflow{fakes: fakes, eligible: map[string]bool{adminActor: true}, seen: map[string]int{}, logs: &logBuffer{}, urls: &urlLog{}}
	// Registered first, so run last: once every server of the walk has
	// ended, nothing it logged and no URL the walk asked holds a pass.
	t.Cleanup(func() { w.noPassLeaked(t) })
	var opts []Option
	if fakes != nil {
		w.ents = fakes.ents
		opts = append(opts, fakes.option())
		w.eligible[ext.SubjectActor+walkOperator.Subject] = true
		if fakes.late == nil {
			// The test's own wait for every provider call, where the
			// product's (IdentityWait, a second) is not what is tested.
			walkWait(t)
		}
	} else {
		// The free walk registers the entitlement source alone, to show no
		// code of this repository asks it there either.
		w.ents = exttest.NewFakeEntitlements("plans")
		opts = append(opts, WithExtensions(local.Fill(ext.Providers{All: []ext.Entry{{Kind: ext.KindEntitlements, Name: "plans", Value: w.ents}}})))
	}
	e := newTestEnvAgents(t, func(cfg *config.Config) { cfg.DataDir = filepath.Join(t.TempDir(), "data") },
		slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		[]catalog.Agent{readyCat, bkAgents()[0]}, opts...)
	e.srv.limiter = newRateLimiter(1e6, 1e6)
	e.client = w.urls.client(e.client)
	w.testEnv = e
	w.repo = filepath.Join(e.root, "repo")
	w.head = planRepo(t, w.repo)
	e.newDispatchProject(t, "CND", map[string]any{"cwd": w.repo})
	w.bk = bkEnvOf(t, e)
	// Registered after the server: run before its end, so no run the walk
	// left going dispatches an attempt while the folders go.
	srv := e.srv
	t.Cleanup(func() { stopEveryRun(srv) })
	return w
}

// free reports whether the walk has no provider.
func (w *workflow) free() bool { return w.fakes == nil }

// launches is the walk's launchTest over its server: CND-1, the crew and
// the approved revision.
func (w *workflow) launches() *launchTest {
	return &launchTest{planTest: &planTest{testEnv: w.testEnv, repo: w.repo, head: w.head}, crew: w.crew, plan: w.plan}
}

// decide is the decide tests' fixture over the walk's run, its asking
// attempt and the walk's identity provider (nil in the free walk).
func (w *workflow) decide() *decideFixture {
	f := &decideFixture{decisionFixture: &decisionFixture{testEnv: w.testEnv, run: w.run, sid: w.sid, tok: w.tok}}
	if w.fakes != nil {
		f.id = w.fakes.id
	}
	return f
}

// walkCrew is the crew of the workflow: the implementer builds (its check
// passes once the file fixed is in the commit), the reviewer reviews the
// build's candidate; ana owns the architecture.
func (w *workflow) walkCrew() crew.Crew {
	return crew.Crew{Name: "Login", Goal: "fix the login loop", Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{
			{Name: "implementer", AgentID: readyCat.ID, Start: crew.Start{When: crew.StartTask}},
			{Name: "reviewer", AgentID: readyCat.ID, Start: crew.Start{When: crew.StartTask}},
		},
		Tasks: []crew.Task{
			{ID: "build", Member: "implementer", Kind: crew.TaskWork, Instructions: "Fix the login loop.", Checks: []crew.Check{{ID: "test", Argv: []string{"test", "-f", "fixed"}}}},
			{ID: "review", Member: "reviewer", Kind: crew.TaskReview, Instructions: "Review the fix.", Needs: []string{"build"}},
		},
		People: []crew.Person{{Name: "ana", Responsibilities: []string{crew.RespArchitecture}}}}
}

// fileTicket is step 1: the operator files CND-1 with its acceptance
// criteria and its owner; Dispatch keeps it at revision 1 with the
// operation's receipt.
func (w *workflow) fileTicket(t *testing.T) {
	t.Helper()
	op := opID()
	out := w.expect(t, http.MethodPost, "/api/dispatch/tickets", map[string]any{"opId": op, "project": "CND", "title": "Fix the login loop",
		"description": "Signing in loops back to the sign-in page.", "acceptance": "Signing in once lands on the dashboard.", "owner": walkOwner}, http.StatusCreated, "")
	tk, rc := entityOf(t, out, "ticket"), receiptOf(t, out)
	if tk["key"] != "CND-1" || tk["rev"] != 1.0 || tk["acceptance"] != "Signing in once lands on the dashboard." || tk["owner"] != walkOwner || rc["status"] != "done" || rc["opId"] != op {
		t.Fatalf("the ticket and its receipt: %v %v", tk, rc)
	}
}

// approvedPlan saves the crew and has the operator propose and approve a
// revision of it for CND-1 directly: the conditions' way to the run, the
// walk's own going through Brakeman's draft (step 3).
func (w *workflow) approvedPlan(t *testing.T) {
	t.Helper()
	w.crew = w.launches().saveCrew(t, w.walkCrew())
	l := w.launches()
	w.plan = l.approved(t, nil)
}

// launch is step 5: Yardmaster starts the managed run from the approved
// revision, the run carrying its ticket and revision, the implementer's
// first attempt running.
func (w *workflow) launch(t *testing.T) {
	t.Helper()
	out := w.launches().launch(t, w.testEnv, map[string]any{"byName": "Nate"}, http.StatusCreated, "")
	launch, run := entityOf(t, out, "launch"), entityOf(t, out, "run")
	w.run = launch["runId"].(string)
	w.runs = []string{w.run}
	if tk, _ := run["ticket"].(map[string]any); tk == nil || tk["key"] != "CND-1" || tk["plan"] != w.plan || tk["launch"] != launch["id"] {
		t.Fatalf("the run's ticket: %v", run["ticket"])
	}
	a := runningAttempt(t, w.testEnv, w.run, "build", 1)
	if a["start"] != w.head {
		t.Fatalf("the implementer's attempt starts at %v, the base is %s", a["start"], w.head)
	}
	w.sid = a["sessionId"].(string)
	w.tok = w.agentToken(w.sid)
}

// raiseBody is the implementer's architecture question: an approval for
// role:architecture, with its alternatives, recommendation, uncertainty
// and evidence.
func (w *workflow) raiseBody(requestID string) map[string]any {
	return map[string]any{"requestId": requestID, "type": "approval", "question": walkQuestion, "for": "role:" + crew.RespArchitecture,
		"context": "The loop comes from the cookie's path; tokens would end it too.",
		"options": []any{
			map[string]any{"label": "Keep the session cookie", "tradeoffs": "a one-line fix; the cookie stays"},
			map[string]any{"label": "Move to tokens", "tradeoffs": "a larger change; no cookie at all"},
		},
		"recommendation": 0, "uncertainty": "whether the SSO callback sets the cookie's path too",
		"authorizes": "the build goes on with the cookie", "evidence": []string{"ticket:CND-1", "run:" + w.run}}
}

// raise is step 6: the implementer reads its task and asks its
// architecture question; the decision is open at rev 1 for ana, the
// architecture owner, its attempt's result refused while it waits.
func (w *workflow) raise(t *testing.T) {
	t.Helper()
	task := w.expectAs(t, w.tok, http.MethodGet, "/api/sessions/"+w.sid+"/task", nil, http.StatusOK, "")
	md, _ := task["markdown"].(string)
	if (w.free() || w.noContext) == strings.Contains(md, walkBlockMark) || (w.free() || w.noContext) == strings.Contains(md, "## Context") {
		t.Fatalf("the implementer's task (a context provider registered: %v):\n%s", !w.free(), md)
	}
	if w.fakes != nil {
		// The router answers the decision with a destination and an answer
		// it holds: the destination is recorded, the answer refused under
		// local/1 and kept, never counted.
		w.fakes.router.SetRoute(walkFirstDecision, ext.Route{To: []ext.Destination{{Kind: "person", Name: "ana"}}, Rule: walkRouteRule,
			Answer: &ext.Answer{Input: "1", By: walkAna}})
	}
	d := entityOf(t, w.decide().ask(t, w.raiseBody("arch-1"), http.StatusCreated, ""), "decision")
	w.did = d["id"].(string)
	asker, _ := d["asker"].(map[string]any)
	if d["state"] != ledger.DecisionOpen || d["rev"] != 1.0 || d["origin"] != ledger.OriginAgent || asker["task"] != "build" || asker["attempt"] != 1.0 ||
		(w.fakes != nil && w.did != walkFirstDecision) {
		t.Fatalf("the decision: %v", d)
	}
	if w.fakes != nil {
		w.routedByTheRouter(t)
	}
	rec := w.record(t, w.did)
	if f := forOf(t, rec.Request); f["label"] != "role:"+crew.RespArchitecture || f["person"] != "ana" {
		t.Fatalf("the decision is for %v, not ana the architecture owner", f)
	}
	if len(rec.History) == 0 || rec.History[0].Kind != ledger.HistoryOpened || !slices.Equal(rec.Held, []string{"build"}) {
		t.Fatalf("the record: %+v", rec)
	}
	w.eventWhen(t, "decision.opened", w.did)
	out := w.expectAs(t, w.tok, http.MethodPost, "/api/sessions/"+w.sid+"/task/result", map[string]any{"summary": "built"}, http.StatusConflict, crew.CodeDecisionOpen)
	if !strings.Contains(apiMessage(out), w.did+" is not answered yet") {
		t.Fatalf("the result's refusal: %v", out)
	}
	if w.fakes != nil {
		awaitFake(t, w.fakes.sink, "the sink handed the opening", func() bool {
			return slices.ContainsFunc(w.fakes.sink.Notifications(), func(n ext.Notification) bool {
				return n.Type == "decision" && n.State == ledger.DecisionOpen && n.Message == walkQuestion
			})
		})
	}
}

// routedByTheRouter is what the router's answer to the walk's decision
// leaves (C5, under local/1): a routed row by the router naming ana and its
// rule, its held answer kept as a refused not_approver row, never counted
// or applied, and the decision still open, for the operator. What the
// operator's item shows of the route waits for decision-policy's PR-7b,
// which fills a record's item from its policy (WF-2b).
func (w *workflow) routedByTheRouter(t *testing.T) {
	t.Helper()
	awaitFake(t, w.fakes.router, "the router told of "+w.did, func() bool {
		return slices.ContainsFunc(w.fakes.router.Decisions(), func(d ext.Decision) bool { return d.Request == w.did })
	})
	d := w.decisionWhen(t, w.did, "routed by the router, its answer refused", func(d map[string]any) bool {
		rs, _ := d["responses"].([]any)
		return slices.Contains(historyOf(d), ledger.HistoryRouted+" router:switchman") && len(rs) > 0
	})
	rec := w.record(t, w.did)
	routed := 0
	for _, h := range rec.History {
		if h.Kind == ledger.HistoryRouted && h.By == "router:switchman" {
			routed++
			if !strings.Contains(h.Detail, `"name":"ana"`) || !strings.Contains(h.Detail, walkRouteRule) {
				t.Fatalf("the router's routed row: %s", h.Detail)
			}
		}
	}
	rcs, _ := d["responses"].([]any)
	if routed != 1 || len(rcs) != 1 || rec.State != ledger.DecisionOpen || d["answer"] != nil || d["applied"] != nil {
		t.Fatalf("after the router's answer: %d routed rows, %s, responses %v, answer %v", routed, rec.State, d["responses"], d["answer"])
	}
	if rc := rcs[0].(map[string]any); rc["status"] != "refused" || rc["code"] != crew.CodeNotApprover {
		t.Fatalf("the router's answer: %v", rc)
	}
	t.Run("the operator's item routed to ana under a router's policy", func(t *testing.T) {
		t.Skip(waitsPolicy)
		it := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
		to, _ := it["routedTo"].([]any)
		if len(to) != 1 || to[0].(map[string]any)["name"] != "ana" || it["routedRule"] != walkRouteRule {
			t.Fatalf("the item's route: %v %v", it["routedTo"], it["routedRule"])
		}
	})
}

// holdsTheAskerAlone is step 7: the asking task alone is held by the
// decision, its attempt not dispatched again; the reviewer waits on its
// needs, as it would anyway.
func (w *workflow) holdsTheAskerAlone(t *testing.T) {
	t.Helper()
	build := taskVia(t, w.testEnv, w.run, "build", "held by "+w.did, func(task map[string]any) bool {
		holds, _ := task["holds"].([]any)
		return len(holds) == 1 && holds[0].(map[string]any)["id"] == w.did
	})
	if a, _ := build["attempt"].(map[string]any); a == nil || a["n"] != 1.0 {
		t.Fatalf("the asking task's attempt: %v", build)
	}
	review := taskVia(t, w.testEnv, w.run, "review", "waiting on its needs", func(map[string]any) bool { return true })
	if holds, _ := review["holds"].([]any); len(holds) != 0 || review["attempt"] != nil {
		t.Fatalf("the reviewer's task: %v", review)
	}
}

// give is step 8 under a provider: the operator gives the decision to
// the decide grant: its for rewritten to the grant, the routed row with
// what it was for before, rev + 1, and the router told of it. Every
// holder of the grant is eligible while it is given.
func (w *workflow) give(t *testing.T) {
	t.Helper()
	before := w.record(t, w.did)
	w.decide().give(t, w.did)
	rec := w.record(t, w.did)
	if f := forOf(t, rec.Request); rec.Rev != before.Rev+1 || f["label"] != "grant:decide" || f["person"] != "" {
		t.Fatalf("given to the grant: rev %d (was %d), for %v", rec.Rev, before.Rev, f)
	}
	last := rec.History[len(rec.History)-1]
	if last.Kind != ledger.HistoryRouted || !strings.Contains(last.Detail, "role:"+crew.RespArchitecture) {
		t.Fatalf("the routed row: %+v", last)
	}
	for _, p := range []ext.Principal{walkAna, walkBob} {
		w.eligible[ext.SubjectActor+p.Subject] = true
	}
	awaitFake(t, w.fakes.router, "the router told of "+w.did, func() bool {
		return slices.ContainsFunc(w.fakes.router.Decisions(), func(d ext.Decision) bool { return d.Request == w.did })
	})
}

// toDecision is the walk to step 8 the conditions start from: the ticket,
// the approved revision, the run, the decision raised and, under a
// provider, given to the decide grant.
func (w *workflow) toDecision(t *testing.T) {
	t.Helper()
	w.fileTicket(t)
	w.approvedPlan(t)
	w.launch(t)
	w.raise(t)
	w.assertTruthful(t, "the decision raised")
	if !w.free() {
		w.give(t)
		w.assertTruthful(t, "the decision given to the decide grant")
	}
}

// respondAs sends pass's response to the decision through the decide
// route at the rev the card shows, answering the status and the reply.
func (w *workflow) respondAs(t *testing.T, pass, responseID string, choice int) (int, []byte, map[string]any) {
	t.Helper()
	return w.decide().respondAs(t, pass, w.did, responseID, choice, w.record(t, w.did).Rev)
}

// applied waits for the decision's application and checks how the held
// work resumed (the answer typed into the asking attempt, handed to its
// next, or its gate released) and its decision.applied event; whether it
// was validated is assertTruthful's, which every test calls after it.
func (w *workflow) applied(t *testing.T, did string) map[string]any {
	t.Helper()
	d := w.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	app, _ := d["applied"].(map[string]any)
	if app == nil || (app["how"] != crew.HowTyped && app["how"] != crew.HowTaskFile && app["how"] != crew.HowReleased) {
		t.Fatalf("the application: %v", d["applied"])
	}
	w.eventWhen(t, "decision.applied", did)
	return d
}

// record is the decision's record as the ledger keeps it.
func (w *workflow) record(t *testing.T, did string) ledger.DecisionRecord {
	t.Helper()
	return w.decide().record(t, did)
}

// forOf is a kept request's for.
func forOf(t *testing.T, request string) map[string]any {
	t.Helper()
	var r struct {
		For map[string]any `json:"for"`
	}
	if err := json.Unmarshal([]byte(request), &r); err != nil {
		t.Fatalf("the request %q: %v", request, err)
	}
	return r.For
}

// eventsOf is the run's events of the kind naming the decision, from the
// ledger.
func (w *workflow) eventsOf(t *testing.T, run, kind, did string) int {
	t.Helper()
	evs, err := w.srv.ledger.Events(t.Context(), run, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind != kind {
			continue
		}
		var p struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatalf("%s's payload %q: %v", kind, ev.Payload, err)
		}
		if p.Decision == did {
			n++
		}
	}
	return n
}

// eventWhen waits for an event of the kind naming the decision in one of
// the walk's runs: the ledger commits a change's events after the change.
func (w *workflow) eventWhen(t *testing.T, kind, did string) {
	t.Helper()
	waitUntil(t, kind+" "+did, func() bool {
		for _, run := range w.runs {
			if w.eventsOf(t, run, kind, did) > 0 {
				return true
			}
		}
		return false
	})
}

// continued records a continuation of the walk's run.
func (w *workflow) continued(run string) {
	if !slices.Contains(w.runs, run) {
		w.runs = append(w.runs, run)
	}
}

// The facts of a decision in their order: a step once done or refused is
// never shown going back.
func factRank(state string) int {
	switch state {
	case ledger.DecisionOpen:
		return 1 // available
	case ledger.DecisionAnswered:
		return 2 // recorded
	case ledger.DecisionApplied:
		return 3 // validated and resumed
	}
	return 4 // final: superseded, cancelled, expired
}

// assertTruthful is the plan's truthful state, read from the records after
// a step: each decision the walk's runs hold read whole through its route
// (its state, responses with their receipts, application, history), the
// runs' events, attempts and holds from the ledger, never the test's own
// memory. One accepted response at most, by an actor the decision admits
// now; no state that needs an answer without one (silence is never
// approval); at most one decision.applied and one application; no step
// shown going back; no held task's attempt reserved inside its hold, and
// no hold released before its decision's application or end; no attempt
// number of a task started twice, and none started while the one before
// it ran; no answer typed in twice; no refused row counted; no code of
// this repository asking the entitlement source, in the free walk too. An open or answered decision
// holds its work, in the run that holds it now.
func (w *workflow) assertTruthful(t *testing.T, step string) {
	t.Helper()
	ctx := t.Context()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("not truthful after %s: %s", step, fmt.Sprintf(format, a...))
	}
	records := map[int64]ledger.DecisionRecord{}
	var dids []string
	for _, run := range w.runs {
		rows, err := w.srv.ledger.Decisions(ctx, ledger.DecisionFilter{CurrentRun: run})
		if err != nil {
			fail("the decisions of %s: %v", run, err)
		}
		for _, r := range rows {
			if !slices.Contains(dids, r.ID()) {
				dids = append(dids, r.ID())
			}
		}
	}
	appliedEvents := map[string]int{}
	for _, did := range dids {
		for _, run := range w.runs {
			appliedEvents[did] += w.eventsOf(t, run, "decision.applied", did)
		}
	}
	for _, did := range dids {
		d := entityOf(t, w.expect(t, http.MethodGet, "/api/decisions/"+did, nil, http.StatusOK, ""), "decision")
		state, _ := d["state"].(string)
		if r := factRank(state); r < w.seen[did] {
			fail("%s went back to %s", did, state)
		} else {
			w.seen[did] = r
		}
		accepted := 0
		responses, _ := d["responses"].([]any)
		for _, r := range responses {
			rc := r.(map[string]any)
			switch rc["status"] {
			case "accepted":
				accepted++
				if actor, _ := rc["actor"].(string); !w.eligible[actor] {
					fail("%s accepted a response of %q, whom it does not admit", did, actor)
				}
			case "refused":
				if rc["code"] == nil || rc["code"] == "" {
					fail("%s keeps a refusal with no code: %v", did, rc)
				}
			default:
				fail("%s keeps a response %v", did, rc)
			}
		}
		if accepted > 1 {
			fail("%s has %d accepted responses", did, accepted)
		}
		answer, _ := d["answer"].(string)
		if (answer != "") != (accepted == 1) && state != ledger.DecisionSuperseded {
			fail("%s's answer %q with %d accepted responses", did, answer, accepted)
		}
		if (state == ledger.DecisionAnswered || state == ledger.DecisionApplied) && accepted != 1 {
			fail("%s is %s with %d accepted responses: silence approved it", did, state, accepted)
		}
		app, _ := d["applied"].(map[string]any)
		switch {
		case appliedEvents[did] > 1:
			fail("%s was applied %d times", did, appliedEvents[did])
		case state == ledger.DecisionApplied && (app == nil || app["validated"] != true):
			fail("%s is applied, its application %v", did, d["applied"])
		case state != ledger.DecisionApplied && (app != nil || appliedEvents[did] != 0):
			fail("%s is %s with an application %v and %d decision.applied", did, state, d["applied"], appliedEvents[did])
		}
		typed := 0
		for _, h := range historyOf(d) {
			if strings.HasPrefix(h, ledger.HistoryApplied+" ") {
				typed++
			}
		}
		if typed > 1 {
			fail("%s's history holds %d applications", did, typed)
		}
		entered := 0
		for _, h := range historyOf(d) {
			if strings.HasPrefix(h, ledger.HistoryDeliveryEntered+" ") {
				entered++
			}
		}
		if entered > 1 {
			fail("%s's answer was typed in %d times", did, entered)
		}
		rec := w.record(t, did)
		records[rec.N] = rec
	}
	for _, run := range w.runs {
		holds, err := w.srv.ledger.Holds(ctx, run)
		if err != nil {
			fail("the holds of %s: %v", run, err)
		}
		attempts, err := w.srv.ledger.Attempts(ctx, run)
		if err != nil {
			fail("the attempts of %s: %v", run, err)
		}
		for _, h := range holds {
			rec, ok := records[h.Decision]
			if !ok {
				r, found, err := w.srv.ledger.Decision(ctx, h.Decision)
				if err != nil || !found {
					fail("the hold of %s names %s, which the ledger has not: %v", h.TaskID, ledger.DecisionID(h.Decision), err)
				}
				rec = r
			}
			for _, a := range attempts {
				if a.TaskID == h.TaskID && a.ReservedAt.After(rec.CreatedAt) && (h.ReleasedAt.IsZero() || a.ReservedAt.Before(h.ReleasedAt)) {
					fail("%s's attempt %d started while %s held it", h.TaskID, a.N, rec.ID())
				}
			}
			if !h.ReleasedAt.IsZero() && !rec.Final() && rec.CurrentRun == run {
				fail("%s's hold on %s was let go while it is %s", rec.ID(), h.TaskID, rec.State)
			}
		}
		byTask := map[string][]ledger.AttemptRow{}
		for _, a := range attempts {
			byTask[a.TaskID] = append(byTask[a.TaskID], a)
		}
		for task, as := range byTask {
			sessions := map[string]bool{}
			for i, a := range as {
				if a.N != as[0].N+i {
					fail("%s's attempts in %s are numbered %v", task, run, attemptNumbers(as))
				}
				if i > 0 && (as[i-1].ClosedAt.IsZero() || a.ReservedAt.Before(as[i-1].ClosedAt)) {
					fail("%s's attempt %d started while attempt %d ran (closed %v, started %v)", task, a.N, as[i-1].N, as[i-1].ClosedAt, a.ReservedAt)
				}
				if a.SessionID != "" && sessions[a.SessionID] {
					fail("%s's session %s ran two attempts", task, a.SessionID)
				}
				sessions[a.SessionID] = true
			}
		}
	}
	for _, rec := range records {
		if (rec.State == ledger.DecisionOpen || rec.State == ledger.DecisionAnswered) && len(rec.Held) == 0 {
			fail("%s is %s and holds nothing: its work could run before its answer is applied", rec.ID(), rec.State)
		}
	}
	if asked := w.ents.Asked(); len(asked) != 0 {
		fail("the entitlement source was asked about %v", asked)
	}
}

// attemptNumbers is the attempts' numbers, in order.
func attemptNumbers(as []ledger.AttemptRow) []int {
	var out []int
	for _, a := range as {
		out = append(out, a.N)
	}
	return out
}

// brakemanBrief is a brief of Brakeman's for the walk's items.
func brakemanBrief(title, summary string, items ...brakeman.Item) brakeman.Brief {
	return brakeman.Brief{Schema: brakeman.BriefSchema, Title: title, Summary: summary, Items: items}
}

// restart ends the walk's server as `conductor serve` ends, and starts
// another over its data directory with the same fakes, as the plan's
// restarts do: a restart drops what a provider was given, and the walk
// hands the new server the same providers. The new server's runs are
// stopped at the end, before its directories go.
func (w *workflow) restart(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w.srv.Shutdown(ctx)
	w.serveAgain(t)
}

// serveAgain starts a server over the walk's data directory, with the same
// fakes, and makes it the walk's.
func (w *workflow) serveAgain(t *testing.T) {
	t.Helper()
	var opts []Option
	if w.fakes != nil {
		opts = append(opts, w.fakes.option())
	}
	srv, err := New(w.srv.cfg, w.srv.base, w.srv.log, nil, w.srv.store, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if srv.home != "" {
		srv.home = t.TempDir()
	}
	again := w.serve(srv)
	again.srv.limiter = newRateLimiter(1e6, 1e6)
	again.client = w.urls.client(again.client)
	t.Cleanup(func() { stopEveryRun(srv) })
	w.testEnv = again
	w.bk = &bkEnv{testEnv: again, desk: again.srv.brakeman}
}

// walkReply is a reply to a request a goroutine of the test's made.
type walkReply struct {
	status int
	data   []byte
	err    error
}

// walkSend sends body to path on the server at base as the token, from any
// goroutine (it reports, never fails the test).
func walkSend(client *http.Client, base, token, method, path string, body any) walkReply {
	b, err := json.Marshal(body)
	if err != nil {
		return walkReply{err: err}
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(b))
	if err != nil {
		return walkReply{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return walkReply{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return walkReply{status: resp.StatusCode, data: data, err: err}
}

// onlyAnaUnderThePolicy is step 8 under a router's policy, once
// decision-policy's PR-7a and PR-7 are on main (WF-2b sets the fake
// router's policy for the decision before it is given): the card says
// "for ana, the architecture owner", and a decide principal the policy
// does not name is refused not_eligible, its refusal kept, never counted.
func onlyAnaUnderThePolicy(t *testing.T, w *workflow) {
	t.Helper()
	rec := w.record(t, w.did)
	if f := forOf(t, rec.Request); f["person"] != "ana" {
		t.Fatalf("under the policy, the decision is for %v", f)
	}
	if status, data, out := w.respondAs(t, passBob, "bob-1", 0); status != http.StatusForbidden || errorCode(out) != "not_eligible" {
		t.Fatalf("bob's answer under the policy: %d %s", status, data)
	}
	if after := w.record(t, w.did); after.State != ledger.DecisionOpen {
		t.Fatalf("after bob's refused answer: %s", after.State)
	}
}

// asBrakeman sends body to path with a Brakeman turn's token, its session
// named, answering the status and the reply.
func (w *workflow) asBrakeman(t *testing.T, sid, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(encodeNoHTML(t, body))
	}
	req, err := http.NewRequest(method, w.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(brakemanSessionHeader, sid)
	resp, err := w.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

// turn asks Brakeman a request of kind about the subjects and answers it
// with its turn's session and token once the turn runs and has read it.
func (w *workflow) turn(t *testing.T, kind string, subjects ...string) (id, sid, tok string) {
	t.Helper()
	status, req := w.bk.ask(t, kind, map[string]any{"subjects": subjects})
	if status != http.StatusCreated {
		t.Fatalf("the %s request: %d %v", kind, status, req)
	}
	id = req["id"].(string)
	sid, tok, _ = w.bk.turnSession(t, id)
	w.bk.waitState(t, id, brakeman.StateRunning)
	if status, out := w.bk.readRequest(t, sid, tok); status != http.StatusOK {
		t.Fatalf("the turn's read of %s: %d %v", id, status, out)
	}
	return id, sid, tok
}

// briefed submits the turn's brief and waits for the request briefed;
// the turn's run then ends after its grace, on the desk's clock, so the
// next turn has the slot.
func (w *workflow) briefed(t *testing.T, clock *clock, id, sid, tok string, b brakeman.Brief) {
	t.Helper()
	if status, out := w.bk.submit(t, sid, tok, "brief-"+id, b); status != http.StatusCreated {
		t.Fatalf("%s's brief: %d %v", id, status, out)
	}
	w.bk.waitState(t, id, brakeman.StateBriefed)
	clock.add(brakemanGrace + time.Second)
	w.bk.settle(t)
	w.bk.slotFree(t)
}

// draftPlan is step 3: asked about CND-1, Brakeman's brief carries a draft
// plan revision of the saved crew (the implementer, then the reviewer;
// the check; ana the architecture owner). Brakeman keeps its request and
// brief and never a revision; its token proposes and approves nothing.
// The operator proposes the draft: a revision of CND-1, proposed.
func (w *workflow) draftPlan(t *testing.T, clock *clock) {
	t.Helper()
	w.crew = w.launches().saveCrew(t, w.walkCrew())
	w.bk.enable(t, "bk", nil)
	id, sid, tok := w.turn(t, brakeman.KindProposeCrew, "ticket:CND-1")
	w.briefed(t, clock, id, sid, tok, brakemanBrief("A plan for CND-1", "The implementer fixes the loop, the reviewer reviews the candidate; ana owns the architecture.",
		brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: "CND-1 says what done means", Evidence: []brakeman.Ref{ref(t, "ticket:CND-1")}}},
		brakeman.Item{Kind: brakeman.ItemDraftPlan, DraftPlan: &brakeman.DraftPlan{Ticket: ref(t, "ticket:CND-1"), Crew: w.crew.ID,
			Constraints: "Keep the build green.", Note: "The implementer builds; the reviewer reviews its candidate."}}))
	plans, err := w.srv.dispatch.Plans(t.Context(), "CND-1")
	if err != nil || len(plans) != 0 {
		t.Fatalf("a revision before a person proposed one: %+v %v", plans, err)
	}
	// Brakeman's token proposes nothing: the make is the operator's.
	if status, out := w.asBrakeman(t, sid, tok, http.MethodPost, "/api/brakeman/requests/"+id+"/items/1/make", map[string]any{"opId": "plan-bk", "by": "Brakeman"}); status != http.StatusUnauthorized {
		t.Fatalf("Brakeman's token on the make: %d %v", status, out)
	}
	status, _, out := w.call(t, http.MethodPost, "/api/brakeman/requests/"+id+"/items/1/make", map[string]any{"opId": "plan-1", "by": "Nate"})
	if status != http.StatusCreated {
		t.Fatalf("the operator's make: %d %v", status, out)
	}
	p := entityOf(t, out, "plan")
	if p["state"] != dispatch.PlanProposed || p["n"] != 1.0 {
		t.Fatalf("the proposed revision: %v", p)
	}
	w.plan = p["id"].(string)
	// Brakeman's token approves nothing.
	if status, out := w.asBrakeman(t, sid, tok, http.MethodPost, "/api/dispatch/plans/"+w.plan+"/approve", map[string]any{"opId": opID(), "byName": "Brakeman"}); status != http.StatusUnauthorized {
		t.Fatalf("Brakeman's token on the approval: %d %v", status, out)
	}
	if v, err := w.srv.dispatch.Plan(t.Context(), w.plan); err != nil || v.State != dispatch.PlanProposed || v.CreatedBy != "Nate" {
		t.Fatalf("the revision: %+v %v", v.PlanSummary, err)
	}
}

// approve is step 4: the operator approves the revision, its receipt kept
// against the ticket's revision then. The ticket's intent edited since,
// the launch is refused stale_plan in words that say so, nothing
// launched; approved again, it holds.
func (w *workflow) approve(t *testing.T) {
	t.Helper()
	approve := func() {
		t.Helper()
		out := w.expect(t, http.MethodPost, "/api/dispatch/plans/"+w.plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go."}, http.StatusOK, "")
		if p := entityOf(t, out, "plan"); p["state"] != dispatch.PlanApproved || receiptOf(t, out)["status"] != "done" {
			t.Fatalf("the approval: %v", out)
		}
	}
	approve()
	w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"description": "Signing in loops back to the sign-in page, on every browser."}}, http.StatusOK, "")
	out := w.launches().launch(t, w.testEnv, nil, http.StatusConflict, dispatch.RefusalStalePlan)
	if msg := apiMessage(out); !strings.Contains(msg, "changed since revision 1 was approved") {
		t.Fatalf("the stale launch's words: %q", msg)
	}
	if rows, err := w.srv.ledger.Runs(t.Context()); err != nil || slices.ContainsFunc(rows, func(r ledger.RunRow) bool { return r.Ticket != "" }) {
		t.Fatalf("a run launched from a stale approval: %+v %v", rows, err)
	}
	approve()
}

// briefDecision is step 9 as main has it. Asked about the asking task,
// Brakeman's brief carries a decision card about that task, at the
// revision its turn read (main takes a card about a question, a task, a
// ticket or a plan, not about a decision record: issue 1046), and cites
// the decision and the run. It is accepted and kept as Brakeman's, its
// card and refs among it; the decision's record is unchanged (its rev, its
// state, its responses) and so are its words on the operator's item;
// Brakeman's token answers nothing. The operator's item carrying the
// brief waits for issue 1046 (briefedItem). The decide page a decide
// principal reads shows the decision's own words and none of the brief's.
func (w *workflow) briefDecision(t *testing.T, clock *clock) {
	t.Helper()
	before := w.record(t, w.did)
	itemBefore := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
	task := "task:" + w.run + "/build"
	id, sid, tok := w.turn(t, brakeman.KindDecision, task)
	if status, out := w.asBrakeman(t, sid, tok, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "bk-1", "choice": 0, "expectedRev": before.Rev}); status != http.StatusUnauthorized {
		t.Fatalf("Brakeman's token on the response: %d %v", status, out)
	}
	status, read := w.asBrakeman(t, sid, tok, http.MethodGet, "/api/runs/"+w.run+"/tasks/build", nil)
	if status != http.StatusOK {
		t.Fatalf("the turn's read of the task: %d %v", status, read)
	}
	taskRev := fmt.Sprint(entityOf(t, read, "task")["revision"])
	evidence := []brakeman.Ref{ref(t, "decision:"+w.did), ref(t, "run:"+w.run)}
	w.briefed(t, clock, id, sid, tok, brakemanBrief("The architecture question of "+w.did, "Brakeman recommends keeping the cookie.",
		brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
		walkCard(t, ref(t, task), taskRev, evidence)))
	got := w.expect(t, http.MethodGet, "/api/brakeman/requests/"+id, nil, http.StatusOK, "")
	if r := entityOf(t, got, "request"); r["state"] != brakeman.StateBriefed || r["kind"] != brakeman.KindDecision {
		t.Fatalf("the request: %v", r)
	}
	kept := entityOf(t, got, "brief")
	items, _ := kept["items"].([]any)
	card := slices.IndexFunc(items, func(it any) bool {
		m := it.(map[string]any)
		return m["kind"] == brakeman.ItemDecision && m["about"] == task && m["subjectRevision"] == taskRev && m["happened"] == walkBriefMark
	})
	if len(items) != 2 || card < 0 {
		t.Fatalf("the brief kept: %v", kept)
	}
	refs := w.expect(t, http.MethodGet, "/api/brakeman/requests/"+id+"/refs", nil, http.StatusOK, "")["refs"].([]any)
	var named []string
	for _, r := range refs {
		named = append(named, r.(map[string]any)["ref"].(string))
	}
	if !slices.Contains(named, "decision:"+w.did) || !slices.Contains(named, "run:"+w.run) || !slices.Contains(named, task) {
		t.Fatalf("the brief's refs: %v", named)
	}
	after := w.record(t, w.did)
	if after.Rev != before.Rev || after.State != ledger.DecisionOpen || len(after.Responses) != len(before.Responses) || after.Request != before.Request {
		t.Fatalf("the brief changed %s: rev %d %s, %d responses (were %d)", w.did, after.Rev, after.State, len(after.Responses), len(before.Responses))
	}
	// The operator's item keeps the decision's own words and rev.
	it := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
	if it["question"] != walkQuestion || it["title"] != walkQuestion || it["revision"] != itemBefore["revision"] || it["state"] != ledger.DecisionOpen ||
		strings.Contains(fmt.Sprint(it["question"], it["title"]), "BRAKEMANMARK") {
		t.Fatalf("the operator's item after the brief: %v", it)
	}
	t.Run("the operator's item with Brakeman's brief on the decision", func(t *testing.T) {
		t.Skip(waits1046)
		w.briefedItem(t, clock)
	})
	if w.free() {
		return
	}
	status, data := w.decide().readAs(t, passAna, w.did)
	var v proto.DecideView
	strictly(t, data, &v)
	if status != http.StatusOK || v.Question != walkQuestion || bytes.Contains(data, []byte("BRAKEMANMARK")) {
		t.Fatalf("ana's card: %d %s", status, data)
	}
}

// respond is steps 10 and 11. Under a provider: every responder the
// decision does not admit is refused and nothing of it is counted (a
// principal without the decide grant, a share link, the asking agent);
// ana reads the card and approves with a rationale, at the rev she read;
// her response is recorded, then held while the identity provider is
// asked again: answered, not applied, the hold kept, until the provider
// admits her; then validated and applied once, the held work resumed.
// With no provider the operator answers.
func (w *workflow) respond(t *testing.T) {
	t.Helper()
	if w.free() {
		rc := receiptOf(t, w.respond0(t, adminToken, "op-1", 0, http.StatusCreated, ""))
		if rc["status"] != "accepted" || rc["actor"] != adminActor {
			t.Fatalf("the operator's receipt: %v", rc)
		}
		w.answer = "op-1"
		w.applied(t, w.did)
		return
	}
	_, link := w.do(http.MethodPost, "/api/sessions/"+w.sid+"/links", adminToken, map[string]any{"role": "control"})
	share, _ := link["token"].(string)
	if share == "" {
		t.Fatalf("no share link: %v", link)
	}
	kept := len(w.record(t, w.did).Responses)
	for name, tok := range map[string]string{"a principal without the decide grant": passHal, "a share link": share, "the asking agent": w.tok} {
		if status, data, _ := w.callAs(t, tok, http.MethodPost, "/api/decisions/"+w.did+"/respond", map[string]any{"responseId": "x-1", "choice": 1, "expectedRev": w.record(t, w.did).Rev}); status != http.StatusUnauthorized {
			t.Fatalf("%s's response: %d %s", name, status, data)
		}
	}
	if rec := w.record(t, w.did); len(rec.Responses) != kept || rec.State != ledger.DecisionOpen {
		t.Fatalf("a refused responder was kept: %+v", rec.Responses)
	}
	v := w.decide().view(t, passAna, w.did)
	if v.Rev != w.record(t, w.did).Rev || v.Question != walkQuestion || len(v.Options) != 2 || v.Recommendation == nil || *v.Recommendation != 0 {
		t.Fatalf("ana's card: %+v", v)
	}
	// The recheck is held past the guard's own wait, which only bounds a
	// hang: the test reads the records meanwhile. The wait is put back as
	// the held step ends.
	putBack := holdRechecks(t)
	gate := w.fakes.id.recheckAs(passAna, walkAna, true)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	sent := make(chan walkReply, 1)
	client, base := w.client, w.http.URL
	go func() {
		sent <- walkSend(client, base, passAna, http.MethodPost, "/api/decisions/"+w.did+"/respond",
			map[string]any{"responseId": "ana-1", "choice": 0, "rationale": "The cookie's path is the bug; tokens can wait.", "expectedRev": v.Rev})
	}()
	select {
	case <-w.fakes.id.reached:
	case <-time.After(testWait):
		t.Fatal("the recheck never reached the identity provider")
	}
	// Recorded, not yet validated: answered, the hold kept, nothing applied.
	w.assertTruthful(t, "step 10, the answer recorded, its recheck held")
	rec := w.record(t, w.did)
	if rec.State != ledger.DecisionAnswered || len(rec.Responses) != kept+1 || !slices.Equal(rec.Held, []string{"build"}) || rec.Applied != "" {
		t.Fatalf("held at the recheck: %s, %d responses, held %v, applied %q", rec.State, len(rec.Responses), rec.Held, rec.Applied)
	}
	close(gate)
	got := received(t, sent, "ana's response")
	putBack()
	if got.err != nil {
		t.Fatalf("ana's response: %v", got.err)
	}
	var r proto.DecideReply
	strictly(t, got.data, &r)
	if got.status != http.StatusCreated || r.Receipt.Status != "accepted" || r.Receipt.ResponseID != "ana-1" || r.Receipt.Rev != v.Rev+1 {
		t.Fatalf("ana's response: %d %s", got.status, got.data)
	}
	// The receipt's rev is the one the answer moved the decision to, from
	// the rev ana read.
	w.answer = "ana-1"
	d := w.applied(t, w.did)
	rcs, _ := d["responses"].([]any)
	if len(rcs) != kept+1 || rcs[kept].(map[string]any)["actor"] != ext.SubjectActor+"ana" || rcs[kept].(map[string]any)["status"] != "accepted" || rcs[kept].(map[string]any)["rationale"] == nil {
		t.Fatalf("the response kept: %v", d["responses"])
	}
	// The answer is on the card as recorded and applied.
	if v := w.decide().view(t, passAna, w.did); v.Outcome != proto.OutcomeApplied || len(v.Own) != 1 || v.Own[0].Status != "accepted" {
		t.Fatalf("ana's card after: %+v", v)
	}
}

// respond0 sends the token's response to the walk's decision at its rev
// now, failing t unless it answers the status and code.
func (w *workflow) respond0(t *testing.T, token, responseID string, choice, status int, code string) map[string]any {
	t.Helper()
	return w.respond1(t, w.did, token, responseID, choice, status, code)
}

// respond1 is respond0 for the decision did.
func (w *workflow) respond1(t *testing.T, did, token, responseID string, choice, status int, code string) map[string]any {
	t.Helper()
	return w.testEnv.respond(t, token, did, responseID, choice, w.record(t, did).Rev, status, code)
}

// candidate is step 12: the held work goes on (the answer typed into the
// implementer's attempt), the implementer submits its candidate, its
// check passes, the reviewer's attempt starts at the candidate (the
// context provider asked about that commit) and submits approved with no
// findings. It answers the candidate's commit.
func (w *workflow) candidate(t *testing.T) string {
	t.Helper()
	a := runningAttempt(t, w.testEnv, w.run, "build", 1)
	c := commitFile(t, a["worktree"].(string), "fixed", "the cookie's path\n")
	w.launches().submit(t, w.sid)
	if row := ledgerAttemptIn(t, w.srv, w.run, "build", 1, ledger.AttemptSucceeded); row.ResultCommit != c {
		t.Fatalf("the implementer's result %s, its commit %s", row.ResultCommit, c)
	}
	rv := runningAttempt(t, w.testEnv, w.run, "review", 1)
	if rv["start"] != c {
		t.Fatalf("the review starts at %v, the candidate is %s", rv["start"], c)
	}
	if w.fakes != nil {
		awaitFake(t, w.fakes.context, "the context provider asked about the review at "+c, func() bool {
			return slices.ContainsFunc(w.fakes.context.Queries(), func(q ext.ContextQuery) bool {
				return q.Run == w.run && q.Task == "review" && q.Attempt == 1 && q.Start == c && q.Kind == crew.TaskReview
			})
		})
	}
	rsid := rv["sessionId"].(string)
	if resp, out := w.do(http.MethodPost, "/api/sessions/"+rsid+"/task/result", w.agentToken(rsid), map[string]any{"verdict": crew.VerdictApproved, "findings": []any{}}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("the review: %d %v", resp.StatusCode, out)
	}
	ledgerAttemptIn(t, w.srv, w.run, "review", 1, ledger.AttemptSucceeded)
	out := candidatesOf(t, w.testEnv, w.run, "")
	build := candidateList(taskOn(t, out, "build"))
	if len(build) != 1 || build[0]["commit"] != c || build[0]["standing"] != true || len(build[0]["receipts"].([]any)) != 1 ||
		build[0]["receipts"].([]any)[0].(map[string]any)["exit"] != 0.0 {
		t.Fatalf("the build's candidate: %v", build)
	}
	review := candidateList(taskOn(t, out, "review"))
	if len(review) != 1 || review[0]["verdict"] != crew.VerdictApproved || review[0]["start"] != c {
		t.Fatalf("the review's candidate: %v", review)
	}
	return c
}

// attemptContextListed is what the run page lists of an attempt's context
// once context-seams CT-3 records it: the task route's first attempt
// carries its context rows, the block the provider gave; an attempt given
// nothing (the free walk) lists none.
func attemptContextListed(t *testing.T, w *workflow, task string) {
	t.Helper()
	out := w.expect(t, http.MethodGet, "/api/runs/"+w.run+"/tasks/"+task, nil, http.StatusOK, "")
	attempts, _ := entityOf(t, out, "task")["attempts"].([]any)
	if len(attempts) == 0 {
		t.Fatalf("%s's attempts: %v", task, out)
	}
	rows, listed := attempts[0].(map[string]any)["context"].([]any)
	if w.free() {
		if listed {
			t.Fatalf("%s's first attempt was given nothing and lists %v", task, rows)
		}
		return
	}
	if len(rows) != 1 || rows[0].(map[string]any)["source"] != "graph" || rows[0].(map[string]any)["title"] != "Callers of Login" {
		t.Fatalf("%s's first attempt's context: %v", task, rows)
	}
}

// repair is step 13: the operator reviews the candidate in the ticket's
// Review view and requests changes with two notes of their own; Dispatch
// keeps the review with its two notes and its application, and the
// repair attempt starts at the candidate with exactly those two changes
// in its task file. It answers the run the repair runs in.
func (w *workflow) repair(t *testing.T, candidate string) string {
	t.Helper()
	body := reviewOf(candName(w.run, "build", 1, candidate), w.head, "request_changes", changeOf(walkNoteOne), changeOf(walkNoteTwo))
	status, data, out := w.call(t, http.MethodPost, "/api/dispatch/tickets/CND-1/reviews", body)
	if status == http.StatusConflict && errorCode(out) == "continuation_needed" {
		body = reviewOf(candName(w.run, "build", 1, candidate), w.head, "request_changes", changeOf(walkNoteOne), changeOf(walkNoteTwo))
		body["continueRun"] = true
		status, data, out = w.call(t, http.MethodPost, "/api/dispatch/tickets/CND-1/reviews", body)
	}
	if status != http.StatusCreated {
		t.Fatalf("the review: %d %s", status, data)
	}
	id := entityOf(t, out, "review")["id"].(string)
	app := applicationOf(t, out)
	run, _ := app["run"].(string)
	if app["state"] != dispatch.ApplicationApplied || run == "" || app["attempt"] != 2.0 {
		t.Fatalf("the application: %v", app)
	}
	w.continued(run)
	if v := reviewNow(t, w.srv, id); len(v.Findings) != 2 {
		t.Fatalf("the review's notes: %+v", v.Findings)
	}
	a := runningAttempt(t, w.testEnv, run, "build", 2)
	if a["start"] != candidate {
		t.Fatalf("the repair starts at %v, the candidate is %s", a["start"], candidate)
	}
	md := string(w.launches().treeFile(t, run, "build", 2, "task.md"))
	for _, note := range []string{walkNoteOne, walkNoteTwo} {
		if strings.Count(md, note) != 1 {
			t.Fatalf("the repair's task.md holds %q %d times:\n%s", note, strings.Count(md, note), md)
		}
	}
	return run
}

// outcome is step 14: the repaired candidate passes its check; the
// operator accepts the ticket for its owner against its criteria as they
// stand (their hash recorded), and adds a delivery link naming the
// candidate, verified. The criteria edited, the acceptance reads unmet and
// the ticket cannot be closed against its close policy (close_policy, the
// acceptance missing); accepted again, then its owner changed, it reads
// unmet again, in words naming the new owner.
func (w *workflow) outcome(t *testing.T, run string) {
	t.Helper()
	a := runningAttempt(t, w.testEnv, run, "build", 2)
	c := commitFile(t, a["worktree"].(string), "expired.txt", "the expired cookie is handled\n")
	sid := a["sessionId"].(string)
	w.launches().submit(t, sid)
	if row := ledgerAttemptIn(t, w.srv, run, "build", 2, ledger.AttemptSucceeded); row.ResultCommit != c {
		t.Fatalf("the repair's result %s, its commit %s", row.ResultCommit, c)
	}
	out := w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/accept", map[string]any{"opId": opID(), "byName": "Nate", "note": "Signed in once, landed on the dashboard."}, http.StatusOK, "")
	tk := entityOf(t, out, "ticket")
	if tk["acceptedFor"] != walkOwner || tk["acceptedBy"] != "Nate" || tk["acceptedCriteria"] != acceptanceHash(tk["acceptance"]) || tk["close"].(map[string]any)["met"].(map[string]any)["acceptance"] != true {
		t.Fatalf("the acceptance: %v", tk)
	}
	out = w.deliver(t, "CND-1", opID(), "https://example.test/pull/12", candName(run, "build", 2, c), http.StatusCreated, "")
	if _, v := deliveryOf(t, entityOf(t, out, "ticket"), receiptOf(t, out)["id"].(string)); !v.Passed || v.Attempt != 2 || len(v.Receipts) != 1 {
		t.Fatalf("the delivery's verification: %+v", v)
	}
	tk = entityOf(t, w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"acceptance": "Signing in once lands on the dashboard, on a phone too."}}, http.StatusOK, ""), "ticket")
	if tk["close"].(map[string]any)["met"].(map[string]any)["acceptance"] != false {
		t.Fatalf("the acceptance after the criteria changed: %v", tk["close"])
	}
	out = w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"status": "done"}}, http.StatusConflict, "close_policy")
	if rc := receiptOf(t, out); !slices.Contains(listOfAny(rc["missing"]), "acceptance") {
		t.Fatalf("closing against the close policy: %v", rc)
	}
	if tk := entityOf(t, w.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket"); tk["status"] == "done" || tk["closedAt"] != nil {
		t.Fatalf("closed against its close policy: %v", tk)
	}
	tk = entityOf(t, w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/accept", map[string]any{"opId": opID(), "byName": "Nate"}, http.StatusOK, ""), "ticket")
	if tk["acceptedCriteria"] != acceptanceHash(tk["acceptance"]) || tk["close"].(map[string]any)["met"].(map[string]any)["acceptance"] != true {
		t.Fatalf("accepted again against the criteria now: %v", tk)
	}
	tk = entityOf(t, w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/edit", map[string]any{"opId": opID(), "set": map[string]any{"owner": "Bob"}}, http.StatusOK, ""), "ticket")
	if c := tk["close"].(map[string]any); c["met"].(map[string]any)["acceptance"] != false || !strings.Contains(fmt.Sprint(c["why"]), "the owner is now Bob") {
		t.Fatalf("the acceptance after the owner changed: %v", c)
	}
}

// acceptanceHash is the hash an acceptance records of the criteria it was
// made against: SHA-256 of their text, in hex.
func acceptanceHash(acceptance any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(acceptance)))
	return hex.EncodeToString(sum[:])
}

// listOfAny is a JSON array's strings.
func listOfAny(v any) []string {
	var out []string
	xs, _ := v.([]any)
	for _, x := range xs {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// contextAbsent is what context-seams' CT-2 and CT-3 show of a provider
// away: the lookup answered 503 lookup_unavailable in its words, and the
// run page listing no context for the attempt.
func contextAbsent(t *testing.T, w *workflow) {
	t.Helper()
	out := w.expectAs(t, w.tok, http.MethodPost, "/api/sessions/"+w.sid+"/context/lookup", map[string]any{"op": "references", "symbol": "Login"}, http.StatusServiceUnavailable, "lookup_unavailable")
	if !strings.Contains(apiMessage(out), "context is unavailable now; carry on without it") {
		t.Fatalf("the lookup's words: %v", out)
	}
	task := entityOf(t, w.expect(t, http.MethodGet, "/api/runs/"+w.run+"/tasks/build", nil, http.StatusOK, ""), "task")
	for _, a := range task["attempts"].([]any) {
		if c, ok := a.(map[string]any)["context"]; ok {
			t.Fatalf("an attempt given nothing lists context: %v", c)
		}
	}
}

// holdRechecks makes every provider call's wait an hour (extWaiting, as
// TestConfirmAuthorityAdmitsDecide does): a recheck held at the
// provider's gate stays held while the test reads the records, on a
// loaded machine too, instead of ending at the guard's wait, which bounds
// a hang. The wait it found is put back by the function it answers, which
// the held step calls as it ends, and by a cleanup of the test (or
// subtest) it was called in, whichever comes first. The walk's tests are
// not parallel.
func holdRechecks(t *testing.T) (putBack func()) {
	t.Helper()
	was := extWaiting.Load()
	long := time.Hour
	extWaiting.Store(&long)
	var once sync.Once
	putBack = func() { once.Do(func() { extWaiting.Store(was) }) }
	t.Cleanup(putBack)
	return putBack
}

// walkWait gives every provider call the test's own wait, a minute, until
// the test ends, as runtime_test.go's takesAnswered does: the fakes
// answer at once, and a loaded machine that schedules an answer late must
// not turn an admitted pass into a refused one (the product's
// IdentityWait is a second). A test of the product's bound (the late
// context provider) keeps it.
func walkWait(t *testing.T) {
	t.Helper()
	wait := time.Minute
	extWaiting.Store(&wait)
	t.Cleanup(func() { extWaiting.Store(nil) })
}

// awaitFake waits for ok, asked again at each change of the fake, within
// the test's own bound, failing t with what it waited for.
func awaitFake(t *testing.T, f interface{ Changed() <-chan struct{} }, what string, ok func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testWait)
	defer cancel()
	if err := exttest.Await(ctx, f, ok); err != nil {
		t.Fatalf("%s: not after %s", what, testWait)
	}
}

// received is the next value on ch, within the test's own bound.
func received[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(testWait):
		t.Fatalf("%s: none after %s", what, testWait)
	}
	var zero T
	return zero
}

// urlLog keeps every URL the walk's client asked for.
type urlLog struct {
	mu   sync.Mutex
	urls []string
}

// client is c with every request's URL kept.
func (l *urlLog) client(c *http.Client) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out := *c
	out.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		l.mu.Lock()
		l.urls = append(l.urls, r.URL.String())
		l.mu.Unlock()
		return base.RoundTrip(r)
	})
	return &out
}

func (l *urlLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.urls)
}

// roundTripFunc is a RoundTripper of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// noPassLeaked is the plan's step 10, never: no pass the walk presented is
// in a line any of its servers logged or in a URL the walk asked for.
func (w *workflow) noPassLeaked(t *testing.T) {
	t.Helper()
	logs, urls := w.logs.String(), w.urls.all()
	if len(urls) == 0 || logs == "" {
		t.Errorf("nothing to judge: %d URLs, %d bytes of log", len(urls), len(logs))
	}
	for _, pass := range walkPasses {
		if strings.Contains(logs, pass) {
			t.Errorf("a log line holds the pass %s", pass)
		}
		for _, u := range urls {
			if strings.Contains(u, pass) {
				t.Errorf("a URL holds the pass %s: %s", pass, u)
			}
		}
	}
}

// refusingClient is a client whose every dial is refused as the kernel
// refuses a port nobody listens on: the owning server out of reach,
// without dialling a port a test has freed (AGENTS.md: a port a test
// freed is not a closed port).
func refusingClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}}}
}

// typedShows is how many times one typing of an answer shows in the
// asking attempt's terminal: the terminal echoes the typed line and the
// stub agent (a cat) prints it back.
const typedShows = 2

// typedTimes is how many times the answer to did shows in the session
// sid's terminal: a viewer's replay of the session's output, from its
// welcome to its ready, counted by the words the typed line opens with.
func (w *workflow) typedTimes(t *testing.T, sid, did string) int {
	t.Helper()
	c := dialViewer(t, w.testEnv, sid, adminToken)
	c.hello(200, 50)
	var out []byte
	for {
		f, err := c.read()
		if err != nil {
			t.Fatalf("the replay of %s: %v", sid, err)
		}
		if f.Type == proto.TypeOutput || f.Type == proto.TypeScrollback {
			out = append(out, f.Payload...)
		}
		if f.Type == proto.TypeControl {
			var m map[string]any
			_ = json.Unmarshal(f.Payload, &m)
			if m["t"] == "ready" {
				break
			}
		}
	}
	c.c.CloseNow()
	return bytes.Count(printed(out), []byte("Decision "+did+" (approval) answered by"))
}

// walkCard is Brakeman's decision card about the subject at its revision:
// what happened (the walk's mark), why a person, the decision's two
// alternatives, a recommendation and its uncertainty, citing evidence.
func walkCard(t *testing.T, about brakeman.Ref, rev string, evidence []brakeman.Ref) brakeman.Item {
	t.Helper()
	keep := 0
	return brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: about, Happened: walkBriefMark,
		WhyHuman: "Only the architecture owner decides how sign-in keeps its session.", Question: walkQuestion,
		Alternatives:   []brakeman.Alternative{{Label: "Keep the session cookie", Tradeoffs: "a one-line fix"}, {Label: "Move to tokens", Tradeoffs: "a larger change"}},
		Recommendation: &keep, Uncertainty: "BRAKEMANMARK whether the SSO callback sets the path too", Authorizes: "the build goes on with the cookie",
		Evidence: evidence, SubjectRevision: rev}}
}

// briefedItem is the plan's step 9 on the operator's decision page, once
// issue 1046 lets Brakeman brief a decision record: a turn's card about
// the decision, at the rev it read, is on the decision's item, marked
// Brakeman's, beside the decision's own question and title, its
// recommendation a mark and nothing answered; the record unchanged.
func (w *workflow) briefedItem(t *testing.T, clock *clock) {
	t.Helper()
	before := w.record(t, w.did)
	id, sid, tok := w.turn(t, brakeman.KindDecision, "task:"+w.run+"/build")
	evidence := []brakeman.Ref{ref(t, "decision:"+w.did), ref(t, "run:"+w.run)}
	w.briefed(t, clock, id, sid, tok, brakemanBrief("The architecture question of "+w.did, "Brakeman recommends keeping the cookie.",
		brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: walkBriefMark, Evidence: evidence}},
		walkCard(t, ref(t, "decision:"+w.did), strconv.Itoa(before.Rev), evidence)))
	after := w.record(t, w.did)
	it := entityOf(t, w.expect(t, http.MethodGet, "/api/inbox/item?ref="+ledger.DecisionRef(numberOf(w.did)), nil, http.StatusOK, ""), "item")
	if it["briefBy"] != "brakeman" || !strings.Contains(fmt.Sprint(it["brief"]), walkBriefMark) || it["question"] != walkQuestion || it["title"] != walkQuestion ||
		!strings.HasPrefix(fmt.Sprint(it["card"]), "brief:"+id+"/") || it["revision"] != strconv.Itoa(after.Rev) || it["cardNote"] != nil ||
		!strings.Contains(fmt.Sprint(it["uncertainty"]), "BRAKEMANMARK") || it["recommended"] != 0.0 {
		t.Fatalf("the operator's item with Brakeman's brief: %v", it)
	}
	if after.Rev != before.Rev || after.State != ledger.DecisionOpen || len(after.Responses) != len(before.Responses) {
		t.Fatalf("the brief changed %s: rev %d %s, %d responses (were %d)", w.did, after.Rev, after.State, len(after.Responses), len(before.Responses))
	}
}
