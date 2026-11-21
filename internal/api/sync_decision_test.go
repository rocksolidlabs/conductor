package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The decision's document as it leaves the machine (sync_decision.go).

var docAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// docRecord is a plan's decision dr-7 whose request is req.
func docRecord(origin, req string) ledger.DecisionRecord {
	return ledger.DecisionRecord{DecisionRow: ledger.DecisionRow{N: 7, RunID: "team-00000001", CurrentRun: "team-00000002", Origin: origin,
		Type: ledger.DecisionClarification, TaskID: "build", Attempt: 2, Member: "lead", Request: req,
		Policy: `{"revision":"local/1","approver":"operator","quorum":1}`, State: ledger.DecisionOpen, Rev: 3, CreatedAt: docAt}}
}

// verdicts is the ledger's verdicts kept, every other part judged by
// judge (clean when nil).
func verdicts(kept map[string]string, judge func(string) string) verdictsOf {
	if judge == nil {
		judge = func(string) string { return verdictClean }
	}
	return verdictsOf{kept: kept, judge: judge}
}

func mustDoc(t *testing.T, rec ledger.DecisionRecord, v verdictsOf) ext.DecisionDoc {
	t.Helper()
	doc, err := syncDecision(rec, v)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The document is the table: the record's identifiers, its request, a hash
// of the request part that the part itself gives, the policy whole, the
// states; a recommendation of none and an empty list are left out, and the
// same record gives the same bytes.
func TestDecisionDocIsTheTable(t *testing.T) {
	req := `{"question":"Which table?","context":"two fit","options":[{"label":"users","tradeoffs":"slow"},{"label":"orders"}],"recommendation":-1,"expertise":[],"evidence":[],"subject":{"kind":"commit","commit":"abc123"}}`
	rec := docRecord(ledger.OriginPlan, req)
	doc := mustDoc(t, rec, verdicts(map[string]string{}, nil))
	if doc.ID != "dr-7" || doc.Run != "team-00000002" || doc.OpenedIn != "team-00000001" || doc.Origin != "plan" || doc.Task != "build" ||
		doc.Attempt != 2 || doc.Member != "lead" || doc.Rev != 3 || doc.State != ledger.DecisionOpen || !doc.States.Open {
		t.Fatalf("the identifiers: %+v", doc)
	}
	if doc.Request == nil || doc.RequestKept != "" || doc.Request.Question != "Which table?" || doc.Request.Context != "two fit" ||
		len(doc.Request.Options) != 2 || doc.Request.Options[0].Tradeoffs != "slow" || doc.Request.Subject == nil || doc.Request.Subject.Commit != "abc123" {
		t.Fatalf("the request: %+v", doc.Request)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, left := range []string{`"recommendation"`, `"expertise"`, `"evidence"`, `"cut"`} {
		if strings.Contains(string(b), left) {
			t.Errorf("%s is in the document: %s", left, b)
		}
	}
	if string(doc.Policy) != rec.Policy {
		t.Fatalf("the policy: %s", doc.Policy)
	}
	part, err := doc.RequestPart()
	if err != nil {
		t.Fatal(err)
	}
	if sum, _ := part.SHA256(); doc.RequestSHA256 != sum || len(sum) != 64 {
		t.Fatalf("requestSHA256 %q, the part's %q", doc.RequestSHA256, sum)
	}
	again, _ := json.Marshal(mustDoc(t, rec, verdicts(map[string]string{}, nil)))
	// Nothing the plan's "never" cell names leaves: a marker in each.
	marked := rec
	marked.SessionID, marked.RequestID, marked.Fingerprint, marked.Evidence = "MARK-session", "MARK-request", "MARK-fingerprint", `[{"ref":"commit:x","seen":"MARK-seen"}]`
	marked.History = []ledger.HistoryRow{{Kind: ledger.HistoryOpened, By: "MARK-history-by", Detail: `{"text":"MARK-history"}`}}
	marked.Held = []string{"MARK-hold"}
	marked.Responses = []ledger.ResponseRow{
		{Decision: 7, K: 1, Actor: "user:ana", Authority: `{"grant":"admin","via":"sync","subject":"ana","boot":"MARK-boot","membership":"MARK-membership","statementSha256":"MARK-statement","fence":"MARK-fence","held":"MARK-held"}`,
			ResponseID: "r1", Fingerprint: "MARK-response-fingerprint", Status: ledger.ResponseRefused, At: docAt,
			Receipt: `{"code":"stale_decision","words":"later","text":"MARK-refused-text","rationale":"MARK-refused-rationale","byName":"MARK-name","policyWhy":"MARK-why"}`},
		{Decision: 7, K: 2, Actor: "admin", Authority: `{"grant":"admin","via":"workbench","boot":"MARK-boot2"}`, ResponseID: "r2", Fingerprint: "MARK-fp2",
			Status: ledger.ResponseAccepted, At: docAt, Receipt: `{"code":"accepted","words":"recorded","choice":0}`},
	}
	if mb, _ := json.Marshal(mustDoc(t, marked, verdicts(map[string]string{}, nil))); strings.Contains(string(mb), "MARK") {
		t.Fatalf("a part the plan says never leaves is in the document: %s", mb)
	}
	if string(again) != string(b) {
		t.Fatalf("the same record gave other bytes:\n%s\n%s", b, again)
	}
	// A recommendation of an option is given.
	rec.Request = strings.Replace(req, `"recommendation":-1`, `"recommendation":1`, 1)
	if d := mustDoc(t, rec, verdicts(map[string]string{}, nil)); d.Request.Recommendation == nil || *d.Request.Recommendation != 1 {
		t.Fatalf("a recommendation of option 1: %v", d.Request.Recommendation)
	}
}

// Two decisions asking the same question are told apart by their request
// parts' hashes; a continuation moving a decision leaves its hash.
func TestTwoDecisionsWithTheSameQuestionToldApart(t *testing.T) {
	req := `{"question":"Ship it?","subject":{"kind":"commit","commit":"abc123"}}`
	a := docRecord(ledger.OriginPlan, req)
	b := a
	b.N = 8
	da, db := mustDoc(t, a, verdicts(map[string]string{}, nil)), mustDoc(t, b, verdicts(map[string]string{}, nil))
	if da.RequestSHA256 == db.RequestSHA256 {
		t.Fatal("dr-7 and dr-8 have one hash")
	}
	a.CurrentRun = "team-00000009"
	if moved := mustDoc(t, a, verdicts(map[string]string{}, nil)); moved.RequestSHA256 != da.RequestSHA256 {
		t.Fatal("a move changed the request part's hash")
	}
}

// openedTo is decide only for a request made for the decide grant that is
// not a comparison's selection.
func TestDecisionDocOpenedToFollowsTheRecord(t *testing.T) {
	plain := `{"question":"Ship it?"}`
	forGrant := `{"question":"Ship it?","for":{"label":"grant:decide","person":""}}`
	forPerson := `{"question":"Ship it?","for":{"label":"ana","person":"user:ana"}}`
	for _, c := range []struct {
		origin, req, want string
	}{
		{ledger.OriginPlan, plain, ext.DecisionOpenedToOperator},
		{ledger.OriginAgent, forGrant, ext.DecisionOpenedToDecide},
		{ledger.OriginPlan, forGrant, ext.DecisionOpenedToDecide},
		{ledger.OriginCompare, forGrant, ext.DecisionOpenedToOperator},
		{ledger.OriginAgent, forPerson, ext.DecisionOpenedToOperator},
	} {
		if got := mustDoc(t, docRecord(c.origin, c.req), verdicts(map[string]string{}, nil)).OpenedTo; got != c.want {
			t.Errorf("%s %s: %q, want %q", c.origin, c.req, got, c.want)
		}
	}
}

// Every origin the ledger writes is one the document carries.
func TestEveryOriginTheLedgerWritesIsOneTheDocumentCarries(t *testing.T) {
	for _, o := range []string{ledger.OriginAgent, ledger.OriginPlan, ledger.OriginCompare} {
		found := false
		for _, d := range ext.DecisionOrigins {
			found = found || d == o
		}
		if !found {
			t.Errorf("the ledger writes origin %q, which the document does not carry", o)
		}
	}
}

// A question, a label, an uncertainty or an authorizes that reads as output,
// and any part whose verdict is pending (a context, a tradeoffs too),
// keeps the whole request back, and the request part with it.
func TestOutputInTheQuestionOrAnOptionKeepsTheRequestBack(t *testing.T) {
	req := `{"question":"Which table?","context":"two of them fit","options":[{"label":"users","tradeoffs":"slower"},{"label":"orders"}],"uncertainty":"some","authorizes":"a deploy"}`
	for _, part := range []string{ledger.PartQuestion, ledger.PartLabel(1), ledger.PartUncertainty, ledger.PartAuthorizes, ledger.PartContext, ledger.PartTradeoffs(0)} {
		for _, v := range []string{verdictOutput, verdictPending} {
			if v == verdictOutput && (part == ledger.PartContext || part == ledger.PartTradeoffs(0)) {
				continue // cut, not kept back: TestAgentContextQuotingOutputIsCut
			}
			doc := mustDoc(t, docRecord(ledger.OriginPlan, req), verdicts(map[string]string{part: v}, nil))
			if doc.Request != nil || doc.RequestKept != ext.DecisionRequestKeptOutput {
				t.Errorf("%s %s: the request left: %+v", part, v, doc.Request)
			}
			b, _ := json.Marshal(doc)
			if strings.Contains(string(b), "Which table?") || strings.Contains(string(b), "orders") {
				t.Errorf("%s %s: the request's words left: %s", part, v, b)
			}
		}
	}
}

// A context, a tradeoffs, a response's words, text or rationale that reads
// as output (or is pending) is cut, and only that part.
func TestAgentContextQuotingOutputIsCut(t *testing.T) {
	const quoted = "panic: runtime error at main.go:12"
	req := fmt.Sprintf(`{"question":"Which table?","context":%q,"options":[{"label":"users","tradeoffs":%q}]}`, quoted, quoted)
	rec := docRecord(ledger.OriginPlan, req)
	choice := `{"code":"accepted","words":"recorded","choice":0,"text":"` + quoted + `","rationale":"fine"}`
	rec.Responses = []ledger.ResponseRow{{Decision: 7, K: 1, Actor: "admin", Authority: `{"via":"workbench"}`, ResponseID: "r1", Status: ledger.ResponseAccepted, Receipt: choice, At: docAt}}
	v := verdicts(map[string]string{ledger.PartContext: verdictOutput, ledger.PartTradeoffs(0): verdictOutput, ledger.PartResponse(1, "text"): verdictOutput}, nil)
	doc := mustDoc(t, rec, v)
	if doc.Request == nil || doc.Request.Question != "Which table?" || doc.Request.Context != outputCutWords || doc.Request.Options[0].Tradeoffs != outputCutWords || doc.Request.Cut != "output" {
		t.Fatalf("the request: %+v", doc.Request)
	}
	r := doc.Responses[0]
	if r.Text != outputCutWords || r.Cut != "output" || r.Rationale != "fine" || r.Words != "recorded" || r.Choice == nil || *r.Choice != 0 || r.Via != "workbench" {
		t.Fatalf("the response: %+v", r)
	}
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), "panic:") {
		t.Fatalf("output left: %s", b)
	}
}

// A decision whose parts were never judged (opened before the check, or
// with no sync store) is judged as its document is built; an agent's
// request of that kind is kept back whatever the check finds now.
func TestEarlierSchemaDecisionOfAnAgentKeptBack(t *testing.T) {
	req := `{"question":"Which table?","context":"two fit"}`
	if doc := mustDoc(t, docRecord(ledger.OriginAgent, req), verdicts(nil, nil)); doc.Request != nil || doc.RequestKept != ext.DecisionRequestKeptOutput {
		t.Fatalf("an agent's unjudged request left: %+v", doc.Request)
	}
	// A response judged since does not make the request judged.
	if doc := mustDoc(t, docRecord(ledger.OriginAgent, req), verdicts(map[string]string{ledger.PartResponse(1, "text"): verdictClean}, nil)); doc.Request != nil {
		t.Fatalf("an agent's request judged by its response's verdict: %+v", doc.Request)
	}
	if doc := mustDoc(t, docRecord(ledger.OriginAgent, req), verdicts(map[string]string{ledger.PartQuestion: verdictClean, ledger.PartContext: verdictClean}, nil)); doc.Request == nil {
		t.Fatal("an agent's request judged as it was written kept back")
	}
	judged := map[string]bool{}
	judge := func(s string) string {
		judged[s] = true
		if s == "two fit" {
			return verdictOutput
		}
		return verdictClean
	}
	doc := mustDoc(t, docRecord(ledger.OriginPlan, req), verdicts(nil, judge))
	if doc.Request == nil || doc.Request.Context != outputCutWords || !judged["Which table?"] {
		t.Fatalf("a plan's unjudged request: %+v (judged %v)", doc.Request, judged)
	}
}

// Past its bound the request is cut, the context first, at a character's
// end, marked size, and the document fits.
func TestDecisionDocCutsPastItsBound(t *testing.T) {
	long := strings.Repeat("é", ext.MaxDecisionDocRequest)
	req := fmt.Sprintf(`{"question":"Which?","context":%q,"options":[{"label":"a","tradeoffs":%q}]}`, long, long)
	doc := mustDoc(t, docRecord(ledger.OriginPlan, req), verdicts(map[string]string{}, nil))
	b, _ := json.Marshal(doc.Request)
	if len(b) > ext.MaxDecisionDocRequest || doc.Request.Cut != "size" || !strings.HasPrefix(long, doc.Request.Context) {
		t.Fatalf("the request: %d bytes, cut %q", len(b), doc.Request.Cut)
	}
	if !json.Valid(b) || strings.ContainsRune(doc.Request.Context+doc.Request.Options[0].Tradeoffs, '�') {
		t.Fatal("a character was cut in two")
	}
	whole, _ := json.Marshal(doc)
	if len(whole) > ext.MaxDecisionDoc {
		t.Fatalf("the document is %d bytes", len(whole))
	}
}

// With the most responses a decision keeps, the accepted one is given with
// what was answered and the newest others while they fit; the rest are
// counted, and the document fits.
func TestDecisionDocWithTheMostResponsesFits(t *testing.T) {
	rec := docRecord(ledger.OriginPlan, `{"question":"Which?","options":[{"label":"a"},{"label":"b"}]}`)
	words := strings.Repeat("w", 600)
	for k := 1; k <= 200; k++ {
		st, rc := ledger.ResponseRefused, fmt.Sprintf(`{"code":"stale","words":%q,"choice":7}`, words)
		if k == 3 {
			st, rc = ledger.ResponseAccepted, fmt.Sprintf(`{"code":"accepted","words":"recorded","choice":1,"text":%q,"rationale":%q}`, strings.Repeat("t", 5000), strings.Repeat("r", 3000))
		}
		rec.Responses = append(rec.Responses, ledger.ResponseRow{Decision: 7, K: k, Actor: "admin", Authority: `{"via":"workbench"}`, ResponseID: fmt.Sprint("r", k), Status: st, Receipt: rc, At: docAt})
	}
	doc := mustDoc(t, rec, verdicts(map[string]string{}, nil))
	b, _ := json.Marshal(doc.Responses)
	if len(b) > ext.MaxDecisionDocResponses {
		t.Fatalf("the responses are %d bytes", len(b))
	}
	var acc *ext.DecisionDocResponse
	for i := range doc.Responses {
		r := &doc.Responses[i]
		if r.Status == ledger.ResponseAccepted {
			acc = r
		} else if r.Choice != nil || len(r.Words) > docWordsMax {
			t.Fatalf("a refused response: choice %v, %d bytes of words", r.Choice, len(r.Words))
		}
	}
	if acc == nil || acc.K != 3 || len(acc.Text) != ext.MaxAnswerText || len(acc.Rationale) != ext.MaxAnswerRationale || *acc.Choice != 1 {
		t.Fatalf("the accepted response: %+v", acc)
	}
	if doc.ResponsesOmitted == 0 || len(doc.Responses)+doc.ResponsesOmitted != 200 || doc.Responses[len(doc.Responses)-1].K != 200 {
		t.Fatalf("%d given, %d omitted", len(doc.Responses), doc.ResponsesOmitted)
	}
	whole, _ := json.Marshal(doc)
	if len(whole) > ext.MaxDecisionDoc {
		t.Fatalf("the document is %d bytes", len(whole))
	}
}

// A decision's verdict is the ledger's, kept beside it, whatever the check
// would find now: a pruned source does not make it clean again.
func TestDecisionVerdictKeptAfterThePrune(t *testing.T) {
	req := `{"question":"Which?","context":"a line the screen once showed"}`
	clean := func(string) string { return verdictClean }
	doc := mustDoc(t, docRecord(ledger.OriginPlan, req), verdicts(map[string]string{ledger.PartQuestion: verdictClean, ledger.PartContext: verdictOutput}, clean))
	if doc.Request.Context != outputCutWords {
		t.Fatalf("a kept verdict of output was judged again: %q", doc.Request.Context)
	}
}

// openPlanDecision opens a plan's decision gate g in run, through ld.
func openPlanDecision(t *testing.T, ld *ledger.Ledger, run, g, req string) int64 {
	t.Helper()
	var n int64
	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		d, _, err := tx.OpenDecision(ledger.DecisionRow{RunID: run, Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: g, Plan: "pl-1", PlanDigest: "d1",
			Fingerprint: "fp-" + g, Request: req, Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
		n = d.N
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// docOf is the last decision document the calls hand for key, and whether
// there is one.
func docOf(calls []syncCall, key syncKey) (ext.DecisionDoc, string, bool) {
	cs := callsOf(calls, key)
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].err != nil {
			continue
		}
		var doc ext.DecisionDoc
		if cs[i].op == "saved" && json.Unmarshal(cs[i].d.Data, &doc) != nil {
			continue
		}
		return doc, cs[i].op, true
	}
	return ext.DecisionDoc{}, "", false
}

// A decision opened, answered here and pruned reaches the sync store as
// its document at each step, its parts judged as the ledger wrote them,
// and is deleted with its run; a replay lists it; SyncResend hands one
// document again and refuses a path that names none.
func TestLocalAnswerReachesTheDocument(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	ld := e.srv.ledger
	const run = "team-00000001"
	newLedgerRun(t, ld, run, ledger.RunRunning)
	n := openPlanDecision(t, ld, run, "g1", `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	key := syncKey{syncDecisionStore, ledger.DecisionID(n)}
	f.until(t, "the decision opened", func(c []syncCall) bool {
		doc, op, ok := docOf(c, key)
		return ok && op == "saved" && doc.Request != nil && doc.Request.Question == "Ship the migration?" && doc.States.Open
	})
	if v, kept, err := ld.OutputVerdicts(context.Background(), n); err != nil || !kept || v[ledger.PartQuestion] != verdictClean {
		t.Fatalf("the ledger's verdicts: %v %v %v", v, kept, err)
	}
	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		if _, err := tx.Respond(n, ledger.ResponseRow{Actor: "admin", Authority: `{"grant":"admin","via":"workbench","boot":"b"}`, ResponseID: "r1", Fingerprint: "fp",
			Status: ledger.ResponseAccepted, Receipt: `{"code":"accepted","words":"recorded","choice":0}`}); err != nil {
			return err
		}
		_, err = tx.Answer(n, run, d.Rev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.until(t, "the answer", func(c []syncCall) bool {
		doc, _, ok := docOf(c, key)
		return ok && len(doc.Responses) == 1 && doc.Responses[0].Via == "workbench" && doc.States.Recorded != nil && doc.State == ledger.DecisionAnswered
	})

	// SyncResend: a decision's path is handed again; one naming nothing is
	// refused.
	h := &extHost{s: e.srv, name: f.Name(), ctx: context.Background()}
	before := len(callsOf(f.all(), key))
	if err := h.SyncResend(context.Background(), syncDecisionStore, key.path); err != nil {
		t.Fatal(err)
	}
	f.until(t, "the resend", func(c []syncCall) bool { return len(callsOf(c, key)) > before })
	for _, bad := range [][2]string{{syncDecisionStore, "dr-x"}, {"decisions", key.path}, {syncData, "workbench-token"}} {
		if err := h.SyncResend(context.Background(), bad[0], bad[1]); err == nil {
			t.Errorf("SyncResend %s/%s accepted", bad[0], bad[1])
		}
	}

	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		d.State, d.Rev, d.Applied = ledger.DecisionApplied, d.Rev+1, `{"how":"released","words":"released"}`
		if err := tx.PutDecision(d); err != nil {
			return err
		}
		r, _, err := tx.Run()
		if err != nil {
			return err
		}
		r.State, r.Outcome, r.ClosedAt = ledger.RunFinished, ledger.OutcomeSucceeded, tx.Now()
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	if err := ld.Prune(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	f.until(t, "the decision deleted", func(c []syncCall) bool {
		_, op, ok := docOf(c, key)
		return ok && op == "deleted"
	})
}

// With no sync store registered nothing of a decision is judged, taken for
// delivery or built, through its whole life (opened, answered, applied,
// listed by a replay, pruned), while the delivery takes the entries of
// other kinds.
func TestNoSyncStoreJudgesNoDecision(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = syncDataDir(t) })
	ld := e.srv.ledger
	if ld == nil {
		t.Fatal("the test server has no ledger")
	}
	if e.srv.copyCheck != nil || e.srv.outputs != nil {
		t.Fatal("an output check on a server with no sync store")
	}
	var mu sync.Mutex
	var taken []syncKey
	e.srv.syncs.mu.Lock()
	e.srv.syncs.taken = func(en syncEntry) {
		mu.Lock()
		taken = append(taken, en.key)
		mu.Unlock()
	}
	e.srv.syncs.mu.Unlock()
	const run = "team-00000001"
	newLedgerRun(t, ld, run, ledger.RunRunning)
	n := openPlanDecision(t, ld, run, "g1", `{"question":"Ship the migration to every region?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		if _, err := tx.Respond(n, ledger.ResponseRow{Actor: "admin", Authority: `{"grant":"admin","via":"workbench","boot":"b"}`, ResponseID: "r1", Fingerprint: "fp",
			Status: ledger.ResponseAccepted, Receipt: `{"code":"accepted","words":"recorded by the operator at the desk","choice":0}`}); err != nil {
			return err
		}
		if _, err := tx.Answer(n, run, d.Rev); err != nil {
			return err
		}
		d, _, _ = tx.Decision(n)
		d.State, d.Rev, d.Applied = ledger.DecisionApplied, d.Rev+1, `{"how":"released","words":"released the deploy task"}`
		if err := tx.PutDecision(d); err != nil {
			return err
		}
		r, _, err := tx.Run()
		if err != nil {
			return err
		}
		r.State, r.Outcome, r.ClosedAt = ledger.RunFinished, ledger.OutcomeSucceeded, tx.Now()
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	// A replay while the decision is there: its listing ends (its
	// `replayed` taken) before the prune.
	replays := func() int {
		mu.Lock()
		defer mu.Unlock()
		c := 0
		for _, k := range taken {
			if k.store == syncReplayed {
				c++
			}
		}
		return c
	}
	before := replays()
	e.srv.syncs.requestReplay()
	waitUntil(t, "the replay", func() bool { return replays() > before })
	newLedgerRun(t, ld, "team-00000002", ledger.RunFinished)
	if err := ld.Prune(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	newLedgerRun(t, ld, "team-00000003", ledger.RunFinished)
	waitUntil(t, "the later runs taken", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(taken, syncKey{syncLedger, "team-00000003"}) && slices.Contains(taken, syncKey{syncLedger, "team-00000002"})
	})
	mu.Lock()
	defer mu.Unlock()
	for _, k := range taken {
		if k.store == syncDecisionStore {
			t.Fatalf("a decision was taken for delivery with no sync store: %v", k)
		}
	}
	if v, _, err := ld.OutputVerdicts(context.Background(), n); err != nil || len(v) != 0 {
		t.Fatalf("verdicts with no sync store: %v %v", v, err)
	}
}

// A part a process before this one left pending is output from the start
// on: the output it may have quoted lived in that process. The replay of
// the start lists the decision, and its document keeps the request back.
func TestPendingDecisionPartWithheldAtStart(t *testing.T) {
	dir := syncDataDir(t)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	ld.SetOutputCheck(func(context.Context, string, bool) string { return ledger.VerdictPending })
	const run = "team-00000001"
	newLedgerRun(t, ld, run, ledger.RunRunning)
	n := openPlanDecision(t, ld, run, "g1", `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	ld.Close()

	f := newFakeSync()
	e := syncEnv(t, dir, f)
	key := syncKey{syncDecisionStore, ledger.DecisionID(n)}
	calls := f.until(t, "the start's replay with the decision", func(c []syncCall) bool {
		_, _, ok := docOf(c, key)
		return replayedIn(c, 1) && ok
	})
	doc, _, _ := docOf(calls, key)
	if doc.Request != nil || doc.RequestKept != ext.DecisionRequestKeptOutput {
		t.Fatalf("a pending question left: %+v", doc.Request)
	}
	v, _, err := e.srv.ledger.OutputVerdicts(context.Background(), n)
	if err != nil || v[ledger.PartQuestion] != verdictOutput || v[ledger.PartLabel(0)] != verdictOutput {
		t.Fatalf("the verdicts after the start: %v %v", v, err)
	}
	if n, _ := e.srv.settleDecisionParts(context.Background()); n != 0 {
		t.Fatal("a withheld part was settled again")
	}
}

// With no accepted response the newest eight others are given and the rest
// counted; an accepted response whose text and rationale JSON escapes six
// bytes a character is cut to keep the responses' bound on the bytes that
// leave.
func TestDecisionDocResponsesHoldTheirBound(t *testing.T) {
	rec := docRecord(ledger.OriginPlan, `{"question":"Which?","options":[{"label":"a"},{"label":"b"}]}`)
	for k := 1; k <= 20; k++ {
		rec.Responses = append(rec.Responses, ledger.ResponseRow{Decision: 7, K: k, Actor: "admin", Authority: `{"via":"workbench"}`, ResponseID: fmt.Sprint("r", k),
			Status: ledger.ResponseRefused, Receipt: `{"code":"stale","words":"later"}`, At: docAt})
	}
	doc := mustDoc(t, rec, verdicts(map[string]string{}, nil))
	if len(doc.Responses) != 8 || doc.ResponsesOmitted != 12 || doc.Responses[0].K != 13 {
		t.Fatalf("%d given from k %d, %d omitted", len(doc.Responses), doc.Responses[0].K, doc.ResponsesOmitted)
	}
	rec.Responses = append(rec.Responses, ledger.ResponseRow{Decision: 7, K: 21, Actor: "admin", Authority: `{"via":"workbench"}`, ResponseID: "r21",
		Status: ledger.ResponseAccepted, At: docAt,
		Receipt: fmt.Sprintf(`{"code":"accepted","words":%q,"choice":1,"text":%q,"rationale":%q}`, strings.Repeat("&", 500), strings.Repeat("<", ext.MaxAnswerText), strings.Repeat(">", ext.MaxAnswerRationale))})
	doc = mustDoc(t, rec, verdicts(map[string]string{}, nil))
	b, _ := json.Marshal(doc.Responses)
	if len(b) > ext.MaxDecisionDocResponses {
		t.Fatalf("the responses are %d bytes as they leave", len(b))
	}
	for _, r := range doc.Responses {
		if r.Status == ledger.ResponseAccepted && (r.Cut != "size" || r.Text == "") {
			t.Fatalf("the accepted response: cut %q, %d bytes of text", r.Cut, len(r.Text))
		}
	}
}
