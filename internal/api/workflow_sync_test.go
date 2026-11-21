package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The walk through sync's seams (docs/plans/end-to-end.md, WF-2c;
// docs/plans/sync-seams.md, OS-1b to OS-4) with the kit's fake sync store
// registered: the decision handed out as a person elsewhere would read it,
// and an answer held while the owning server was away handed in once.

// syncHost is the Host the walk's sync store is given, on the server of
// the walk now (a restart makes another).
func (w *workflow) syncHost() *extHost {
	return &extHost{s: w.srv, name: w.fakes.sync.Name(), ctx: context.Background()}
}

// handedOut waits for the sync store to have been handed the decision's
// document with its request (its question as the asking agent wrote it,
// judged clean: a request with a part still pending is kept back whole)
// and answers it.
func (w *workflow) handedOut(t *testing.T, did string) ext.DecisionDoc {
	t.Helper()
	var doc ext.DecisionDoc
	awaitFake(t, w.fakes.sync, did+"'s document handed out with its request", func() bool {
		docs := w.fakes.sync.Docs()
		for i := len(docs) - 1; i >= 0; i-- {
			d := docs[i]
			if d.Deleted || d.Doc.Store != "decision" || d.Doc.Path != did {
				continue
			}
			var got ext.DecisionDoc
			if json.Unmarshal(d.Doc.Data, &got) != nil || got.Request == nil {
				return false
			}
			doc = got
			return true
		}
		return false
	})
	if doc.ID != did || doc.Request.Question != walkQuestion || doc.State != ledger.DecisionOpen || len(doc.Request.Options) != 2 {
		t.Fatalf("%s's document as handed out: %+v", did, doc)
	}
	return doc
}

// admitsAna is the identity provider's check of a held answer: ana's
// statement admitted with the decide grant, anyone else's not.
func admitsAna(c ext.AnswerCheck, g ext.Grant) ext.Admission {
	if c.Subject == walkAna.Subject && g == ext.GrantDecide {
		return ext.AdmitYes
	}
	return ext.AdmitNo
}

// heldWhileAway: the owning server is out of ana's reach when she answers
// on another machine, where the sync store holds her answer. The server
// stopped, its run interrupted: the answer handed in is not held there,
// nothing written. Once the run is continued it is
// recorded, through the identity provider's check of her statement, and
// a second hand-in of it is a repeat with the same receipt; it is applied
// once.
func heldWhileAway(t *testing.T) {
	fakes := newWorkflowFakes()
	fakes.answers, fakes.plainRouter = admitsAna, true
	w := newWorkflowWith(t, fakes)
	w.toDecision(t)
	doc := w.handedOut(t, w.did)
	kept := len(w.record(t, w.did).Responses)
	answer := held(w.did, "held-1", walkAna.Subject, 0, doc.Rev)
	w.restart(t)
	out, err := w.syncHost().RespondDecision(t.Context(), answer)
	if err != nil || out.Status != ext.AnswerNotHeld || !strings.Contains(out.Words, w.did) || !strings.Contains(out.Words, "continue the run to apply this answer") {
		t.Fatalf("the hand-in to an interrupted run: %+v %v", out, err)
	}
	if rec := w.record(t, w.did); rec.State != ledger.DecisionOpen || len(rec.Responses) != kept {
		t.Fatalf("a hand-in not held wrote: %s, %d responses (were %d)", rec.State, len(rec.Responses), kept)
	}
	w.assertTruthful(t, "a held answer to an interrupted run")
	next := entityOf(t, w.expect(t, http.MethodPost, "/api/runs/"+w.run+"/resume", nil, http.StatusCreated, ""), "run")["id"].(string)
	w.continued(next)
	first, err := w.syncHost().RespondDecision(t.Context(), answer)
	if err != nil || first.Status != ext.AnswerRecorded || first.Repeat {
		t.Fatalf("the hand-in once the run is continued: %+v %v", first, err)
	}
	again, err := w.syncHost().RespondDecision(t.Context(), answer)
	if err != nil || again.Status != ext.AnswerRecorded || !again.Repeat || heldReceipt(t, again) != heldReceipt(t, first) {
		t.Fatalf("the same answer handed in again: %+v %v, receipt %s (first %s)", again, err, again.Receipt, first.Receipt)
	}
	w.answer = "held-1"
	d := w.applied(t, w.did)
	rcs, _ := d["responses"].([]any)
	if len(rcs) != kept+1 || rcs[kept].(map[string]any)["actor"] != ext.SubjectActor+walkAna.Subject || rcs[kept].(map[string]any)["status"] != "accepted" {
		t.Fatalf("the responses after the held answer: %v", d["responses"])
	}
	w.assertTruthful(t, "the held answer applied")
}

// heldReceipt is the response a hand-in's receipt names: its number, its id
// and its status.
func heldReceipt(t *testing.T, o ext.AnswerOutcome) string {
	t.Helper()
	var rc struct {
		K          int    `json:"k"`
		ResponseID string `json:"responseId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(o.Receipt, &rc); err != nil || rc.K == 0 {
		t.Fatalf("the receipt %s: %v", o.Receipt, err)
	}
	return fmt.Sprintf("%d %s %s", rc.K, rc.ResponseID, rc.Status)
}

// heldRefusedWherePoliciesAreConfirmed: on a server whose router confirms
// policies, a held answer is refused at its door, answered_at_server,
// final, nothing written: the decision is answered at its server alone.
func heldRefusedWherePoliciesAreConfirmed(t *testing.T) {
	fakes := newWorkflowFakes()
	fakes.answers = admitsAna
	w := newWorkflowWith(t, fakes)
	w.toDecision(t)
	doc := w.handedOut(t, w.did)
	before := w.record(t, w.did)
	out, err := w.syncHost().RespondDecision(t.Context(), held(w.did, "held-1", walkAna.Subject, 0, doc.Rev))
	if err != nil || out.Status != ext.AnswerRefused || out.Code != "answered_at_server" {
		t.Fatalf("a held answer where the router confirms policies: %+v %v", out, err)
	}
	if after := w.record(t, w.did); after.Rev != before.Rev || len(after.Responses) != len(before.Responses) || after.State != ledger.DecisionOpen ||
		!slices.Equal(after.Held, []string{"build"}) {
		t.Fatalf("the refused hand-in wrote: rev %d (was %d), %d responses (were %d), %s", after.Rev, before.Rev, len(after.Responses), len(before.Responses), after.State)
	}
	w.assertTruthful(t, "a held answer refused at its door")
}

// fromHanded is a row as the sync store hands it to another machine's
// store, as the apply door takes it in.
func fromHanded(d ext.DispatchChange) dispatch.ChangeRow {
	return dispatch.ChangeRow{Entity: d.Entity, EntityID: d.EntityID, TicketID: d.TicketID, ProjectID: d.ProjectID, Field: d.Field, Op: d.Op,
		Value: d.Value, Prior: d.Prior, At: d.At, Actor: d.Actor, By: d.By, OpID: d.OpID, N: d.N, Origin: d.Origin, RecoveredBy: d.RecoveredBy,
		ViewDigest: d.ViewDigest}
}

// otherMachine is another machine of the walk's workspace: a store of
// its own, joined under its own tag, and how far it has taken the rows
// the walk's server hands out and handed back its own.
type otherMachine struct {
	ds          *dispatch.Store
	took, gave  int64
	projectKeys map[string]string
}

func newOtherMachine(t *testing.T) *otherMachine {
	t.Helper()
	ds, err := dispatch.Open(filepath.Join(t.TempDir(), "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	if err := ds.Join(context.Background(), "b1", "w1", "dev-b", 2_000_001, 2_100_000); err != nil {
		t.Fatal(err)
	}
	return &otherMachine{ds: ds, projectKeys: map[string]string{}}
}

// take is the other machine taking every row the walk's server hands out
// through the sync store's door (rows the copied-output check has not
// settled end the page), with the views their revisions name.
func (o *otherMachine) take(t *testing.T, w *workflow) []ext.DispatchChange {
	t.Helper()
	ctx := context.Background()
	var all []ext.DispatchChange
	for {
		rows, next, err := w.syncHost().DispatchChanges(ctx, o.took, ext.MaxDispatchBatch)
		if err != nil {
			t.Fatal(err)
		}
		if next == o.took {
			return all
		}
		o.took = next
		in := make([]dispatch.ChangeRow, 0, len(rows))
		views := map[string][]byte{}
		for _, d := range rows {
			in = append(in, fromHanded(d))
			if d.ViewDigest != "" {
				v, err := w.syncHost().DispatchPlanView(ctx, d.ViewDigest)
				if err != nil {
					t.Fatal(err)
				}
				views[d.ViewDigest] = v
			}
		}
		if _, err := o.ds.ApplyRemote(ctx, in, views, o.projectKeys); err != nil {
			t.Fatalf("the other machine's apply: %v", err)
		}
		all = append(all, rows...)
	}
}

// give is the other machine's own rows handed to the walk's server
// through the sync store's door.
func (o *otherMachine) give(t *testing.T, w *workflow) {
	t.Helper()
	ctx := context.Background()
	for {
		rows, next, err := o.ds.HandedChanges(ctx, o.gave, dispatch.MaxChangesPage)
		if err != nil {
			t.Fatal(err)
		}
		if next == o.gave {
			return
		}
		o.gave = next
		var batch []ext.DispatchChange
		views := map[string][]byte{}
		for _, c := range rows {
			if c.Origin != "b1" {
				continue // rows it took from the walk's server
			}
			batch = append(batch, toDispatchChange(c))
			if c.ViewDigest != "" {
				v, err := o.ds.RevisionView(ctx, c.ViewDigest)
				if err != nil {
					t.Fatal(err)
				}
				views[c.ViewDigest] = v
			}
		}
		if len(batch) == 0 {
			continue
		}
		if _, err := w.syncHost().DispatchApply(ctx, batch, views, o.projectKeys); err != nil {
			t.Fatalf("the other machine's rows applied here: %v", err)
		}
	}
}

// TestWorkflowThroughTheSyncStore walks the workflow through sync's seams
// with the sync store registered, as the person meets each step:
//  1. the server joins a workspace; the operator files CND-1 and shares
//     CND, which is pending until the store completes and confirms it,
//     then shared;
//  2. the operator's comment is judged clean once settled (never read
//     straight after its write) and is handed out with CND-1's rows, which
//     another machine of the workspace takes;
//  3. that machine's comment, a row from elsewhere, is applied here and
//     listed on CND-1;
//  4. a plan revision approved here and then approved on the other
//     machine, after an edit there, launches nothing here until it is
//     approved here again;
//  5. the run's decision is handed out with its question;
//  6. what another machine holds of CND-1 (a decision open there, a run
//     running there) is listed as that machine's, and nothing here
//     answers or opens it.
func TestWorkflowThroughTheSyncStore(t *testing.T) {
	ctx := context.Background()
	w := newWorkflow(t, false)
	if err := w.syncHost().DispatchJoin(ctx, "a1", "w1", "dev-a", 1_000_001, 1_100_000); err != nil {
		t.Fatal(err)
	}
	w.fileTicket(t)
	project := entityOf(t, w.expect(t, http.MethodPost, "/api/dispatch/projects/CND/share", map[string]any{"opId": opID()}, http.StatusOK, ""), "project")
	if share, _ := project["share"].(map[string]any); share == nil || share["state"] != dispatch.SharePending {
		t.Fatalf("CND asked to be shared: %v", project["share"])
	}
	pid := project["id"].(string)
	head, err := w.srv.dispatch.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.syncHost().DispatchCompleteShare(ctx, pid, "w1", head); err != nil {
		t.Fatal(err)
	}
	if err := w.syncHost().DispatchConfirmShare(ctx, pid); err != nil {
		t.Fatal(err)
	}
	project = entityOf(t, w.expect(t, http.MethodGet, "/api/dispatch/projects/CND", nil, http.StatusOK, ""), "project")
	if share, _ := project["share"].(map[string]any); share == nil || share["state"] != dispatch.ShareShared || share["shared"] != "w1" {
		t.Fatalf("CND once shared: %v", project["share"])
	}
	w.assertTruthful(t, "step 1, CND shared")

	// 2. The operator's comment, judged once settled, handed out.
	const note = "The loop starts at the cookie's path, set on the login page."
	w.expect(t, http.MethodPost, "/api/dispatch/tickets/CND-1/comments", map[string]any{"opId": opID(), "body": note}, http.StatusCreated, "")
	if v := settledVerdict(t, w.testEnv, note); v != verdictClean {
		t.Fatalf("the operator's comment settled %q", v)
	}
	other := newOtherMachine(t)
	other.projectKeys[pid] = "CND"
	handed := other.take(t, w)
	if !slices.ContainsFunc(handed, func(d ext.DispatchChange) bool {
		return d.Entity == dispatch.EntityTicket && d.Op == dispatch.OpCreate && d.ProjectID == pid
	}) ||
		!slices.ContainsFunc(handed, func(d ext.DispatchChange) bool { return d.Value == note && !d.OutputLike }) {
		t.Fatalf("the rows handed out hold no create of CND-1 or no comment of the operator's: %d rows", len(handed))
	}
	w.assertTruthful(t, "step 2, the rows handed out")

	// 3. A comment from the other machine, applied here.
	const theirs = "Seen on the other machine: the loop stops once the cookie's path is the root."
	if rc, err := other.ds.AddComment(ctx, dispatch.Op{Actor: "admin", ID: "their-note", By: "Bea"}, "CND-1", theirs); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the other machine's comment: %+v %v", rc, err)
	}
	other.give(t, w)
	comments := w.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-1/comments", nil, http.StatusOK, "")
	if b, _ := json.Marshal(comments); !strings.Contains(string(b), theirs) || !strings.Contains(string(b), note) {
		t.Fatalf("CND-1's comments: %s", b)
	}
	w.assertTruthful(t, "step 3, a row from elsewhere applied")

	// 4. An approval from elsewhere is approved again here before a launch.
	w.approvedPlan(t)
	other.take(t, w)
	title := "Fix the login loop, the cookie's path at the root"
	if rc, err := other.ds.EditTicket(ctx, dispatch.Op{Actor: "admin", ID: "their-edit", By: "Bea"}, "CND-1", dispatch.TicketEdit{Set: dispatch.TicketSet{Title: &title}}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the other machine's edit: %+v %v", rc, err)
	}
	if rc, err := other.ds.ApprovePlan(ctx, dispatch.Op{Actor: "admin", ID: "their-approval", By: "Bea"}, w.plan, 0, "approved there"); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the other machine's approval: %+v %v", rc, err)
	}
	other.give(t, w)
	w.launches().launch(t, w.testEnv, map[string]any{"byName": "Nate"}, http.StatusConflict, dispatch.RefusalApprovedElsewhere)
	w.assertTruthful(t, "step 4, a launch of an approval from elsewhere refused")
	w.expect(t, http.MethodPost, "/api/dispatch/plans/"+w.plan+"/approve", map[string]any{"opId": opID(), "byName": "Nate", "note": "Go, here."}, http.StatusOK, "")
	w.launch(t)
	w.assertTruthful(t, "step 4, launched once approved here")

	// 5. The run's decision, handed out with its question.
	w.raise(t)
	w.handedOut(t, w.did)
	w.assertTruthful(t, "step 5, the decision handed out")

	// 6. What another machine holds of CND-1, listed as its, acted on by
	// nothing here.
	tk := entityOf(t, w.expect(t, http.MethodGet, "/api/dispatch/tickets/CND-1", nil, http.StatusOK, ""), "ticket")
	ticketID := tk["id"].(string)
	for _, r := range []ext.Remote{
		remoteOf(ext.RemoteDecision, "dev-b", "dr-41", 1, remoteDecisionOf("dr-41", ticketID, "open")),
		remoteOf(ext.RemotePointer, "dev-b", "team-0000bb01", 1, map[string]any{"id": "team-0000bb01", "crew": "team", "state": "running", "revision": 1, "ticket": ticketID}),
	} {
		if err := w.syncHost().PutRemote(ctx, r); err != nil {
			t.Fatalf("%s %s: %v", r.Kind, r.ID, err)
		}
	}
	recs, _ := remoteRecords(t, w.testEnv, "?ticket=CND-1")
	if len(recs) != 2 || slices.ContainsFunc(recs, func(r remoteRecord) bool { return r.Device != "dev-b" || r.DeviceName != "studio" }) {
		t.Fatalf("CND-1's records on other machines: %+v", recs)
	}
	w.expect(t, http.MethodPost, "/api/decisions/dr-41/respond", map[string]any{"responseId": "here-1", "choice": 0, "expectedRev": 1}, http.StatusNotFound, "")
	w.expect(t, http.MethodGet, "/api/runs/team-0000bb01", nil, http.StatusNotFound, "")
	w.assertTruthful(t, "step 6, what another machine holds, read only")
}
