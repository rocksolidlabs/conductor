package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// Held answers handed in by the sync store (sync_answers.go). The fake
// identity provider decides each AdmitsAnswer by its admit function and
// keeps what it was asked; the fake sync store is the only provider whose
// Host opens the doors.

// answerAsk is one AdmitsAnswer the fake was asked.
type answerAsk struct {
	check ext.AnswerCheck
	grant ext.Grant
}

// answerIdentity is an identity provider that admits no token and judges
// held answers by admit.
type answerIdentity struct {
	mu    sync.Mutex
	admit func(a ext.AnswerCheck, g ext.Grant) ext.Admission
	asked []answerAsk
	ch    chan struct{}
	// once admits each of its tokens at its first ask alone (a response's
	// authentication), not at its recheck.
	once map[string]ext.Principal
}

func (*answerIdentity) Name() string { return "ids" }

func (p *answerIdentity) Authenticate(_ context.Context, tok string) (ext.Principal, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.once[tok]
	if ok {
		delete(p.once, tok)
	}
	return pr, ok, nil
}

func (p *answerIdentity) AdmitsAnswer(_ context.Context, a ext.AnswerCheck, g ext.Grant) (ext.Admission, error) {
	p.mu.Lock()
	p.asked = append(p.asked, answerAsk{a, g})
	admit := p.admit
	p.mu.Unlock()
	select {
	case p.ch <- struct{}{}:
	default:
	}
	return admit(a, g), nil
}

func (p *answerIdentity) set(admit func(ext.AnswerCheck, ext.Grant) ext.Admission) {
	p.mu.Lock()
	p.admit = admit
	p.mu.Unlock()
}

func (p *answerIdentity) asks() []answerAsk {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]answerAsk(nil), p.asked...)
}

// waitAsks waits until the fake has been asked n times.
func (p *answerIdentity) waitAsks(t *testing.T, n int) []answerAsk {
	t.Helper()
	end := time.After(testWait)
	for {
		if a := p.asks(); len(a) >= n {
			return a
		}
		select {
		case <-p.ch:
		case <-end:
			t.Fatalf("the identity provider was asked %d times, not %d", len(p.asks()), n)
		}
	}
}

func always(adm ext.Admission) func(ext.AnswerCheck, ext.Grant) ext.Admission {
	return func(ext.AnswerCheck, ext.Grant) ext.Admission { return adm }
}

// heldFixture is a decisionFixture with a sync store and the fake identity
// provider, and the sync store's Host.
type heldFixture struct {
	*decisionFixture
	id    *answerIdentity
	store *fakeSync
	h     *extHost
	// waits is what a held answer's recheck waits on between its asks.
	waits chan chan time.Time
}

func newHeldFixture(t *testing.T) *heldFixture {
	t.Helper()
	id := &answerIdentity{admit: always(ext.AdmitYes), ch: make(chan struct{}, 1), once: map[string]ext.Principal{}}
	f := newFakeSync()
	opt := WithExtensions(local.Fill(ext.Providers{Identity: id, Continuity: f, All: []ext.Entry{
		{Kind: ext.KindIdentity, Name: id.Name(), Value: id}, {Kind: ext.KindSync, Name: f.Name(), Value: f},
	}}))
	df := newDecisionFixture(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), opt)
	hf := &heldFixture{decisionFixture: df, id: id, store: f, h: &extHost{s: df.srv, name: f.Name(), ctx: context.Background()}, waits: make(chan chan time.Time, 16)}
	df.srv.recheckAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		hf.waits <- ch
		return ch
	}
	return hf
}

// held is subject's held answer to did at rev, the option choice.
func held(did, responseID, subject string, choice, rev int) ext.HeldAnswer {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return ext.HeldAnswer{Decision: did, ExpectedRev: rev, ResponseID: responseID, Choice: &choice, HeldAt: at, Held: "h-" + responseID, Fence: "f-1",
		Check: ext.AnswerCheck{Subject: subject, Membership: "m-1", SignIn: "s-1", At: at, Statement: "statement of " + responseID,
			Decision: did, ResponseID: responseID, ExpectedRev: rev}}
}

// hand hands r in and fails t on an error.
func (f *heldFixture) hand(t *testing.T, r ext.HeldAnswer) ext.AnswerOutcome {
	t.Helper()
	out, err := f.h.RespondDecision(context.Background(), r)
	if err != nil {
		t.Fatalf("the hand-in of %s: %v", r.ResponseID, err)
	}
	if err := out.Valid(); err != nil {
		t.Fatalf("the outcome %+v: %v", out, err)
	}
	return out
}

// rec is decision did's record.
func (f *heldFixture) rec(t *testing.T, did string) ledger.DecisionRecord {
	t.Helper()
	n, _ := ledger.ParseDecisionID(did)
	rec, ok, err := f.srv.ledger.Decision(t.Context(), n)
	if err != nil || !ok {
		t.Fatalf("%s: %v %v", did, ok, err)
	}
	return rec
}

// unchanged fails t unless did's record is at rev with n responses.
func (f *heldFixture) unchanged(t *testing.T, did string, rev, n int) {
	t.Helper()
	if rec := f.rec(t, did); rec.Rev != rev || len(rec.Responses) != n {
		t.Fatalf("%s changed: rev %d, %d responses", did, rec.Rev, len(rec.Responses))
	}
}

// open opens an approval of the attempt and answers its id.
func (f *heldFixture) open(t *testing.T, requestID string) string {
	t.Helper()
	return entityOf(t, f.ask(t, approvalBody(requestID), http.StatusCreated, ""), "decision")["id"].(string)
}

// An answer is recorded only once the identity provider admits it, asked
// with the admin grant and the content and request hashes the server
// computed (never the ones handed in); a no is not_approver and an
// unknown is unknown_authority, and neither writes a row; a server with no
// provider that checks held answers takes none.
func TestRespondDecisionNeedsAdmits(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	for _, c := range []struct {
		adm    ext.Admission
		status string
		code   string
	}{
		{ext.AdmitNo, ext.AnswerRefused, crew.CodeNotApprover},
		{ext.AdmitUnknown, ext.AnswerUnknownAuthority, ""},
	} {
		f.id.set(always(c.adm))
		out := f.hand(t, held(did, "h-no", "ana", 0, 1))
		if out.Status != c.status || out.Code != c.code {
			t.Fatalf("admission %d: %+v", c.adm, out)
		}
		f.unchanged(t, did, 1, 0)
	}
	f.id.set(always(ext.AdmitYes))
	r := held(did, "h1", "ana", 0, 1)
	r.Check.ContentSHA256, r.Check.RequestSHA256 = "forged", "forged"
	out := f.hand(t, r)
	if out.Status != ext.AnswerRecorded || out.Repeat {
		t.Fatalf("admitted: %+v", out)
	}
	asks := f.id.asks()
	last := asks[len(asks)-1]
	doc, _, _ := f.srv.decisionDoc(t.Context(), 1)
	if last.grant != ext.GrantAdmin || last.check.RequestSHA256 != doc.RequestSHA256 || last.check.ContentSHA256 != ext.AnswerContentSHA256(r.Choice, "", "") {
		t.Fatalf("the provider was asked %+v", last)
	}
	rec := f.rec(t, did)
	if len(rec.Responses) != 1 || rec.Responses[0].Actor != "user:ana" || rec.State != ledger.DecisionAnswered {
		t.Fatalf("the record: %+v", rec.Responses)
	}
}

// With no identity provider that checks held answers, none is taken.
func TestNoSubjectCheckerTakesNoHeldAnswer(t *testing.T) {
	f := newFakeSync()
	plain := &tokenIdentity{known: map[string]ext.Principal{}}
	opt := WithExtensions(local.Fill(ext.Providers{Identity: plain, Continuity: f, All: []ext.Entry{
		{Kind: ext.KindIdentity, Name: "ids", Value: plain}, {Kind: ext.KindSync, Name: f.Name(), Value: f},
	}}))
	df := newDecisionFixture(t, nil, nil, opt)
	did := entityOf(t, df.ask(t, approvalBody("q1"), http.StatusCreated, ""), "decision")["id"].(string)
	h := &extHost{s: df.srv, name: f.Name(), ctx: context.Background()}
	out, err := h.RespondDecision(context.Background(), held(did, "h1", "ana", 0, 1))
	if err != nil || out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover {
		t.Fatalf("a held answer with no checker: %+v %v", out, err)
	}
}

// An unknown admission writes nothing: no row, no statement, no rev.
func TestUnknownAdmissionWritesNothing(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(always(ext.AdmitUnknown))
	if out := f.hand(t, held(did, "h1", "ana", 0, 1)); out.Status != ext.AnswerUnknownAuthority {
		t.Fatalf("%+v", out)
	}
	f.unchanged(t, did, 1, 0)
	if _, ok, _ := f.srv.ledger.Statement(t.Context(), 1, 1); ok {
		t.Fatal("a statement was kept")
	}
	// Asked again once the provider can say: recorded.
	f.id.set(always(ext.AdmitYes))
	if out := f.hand(t, held(did, "h1", "ana", 0, 1)); out.Status != ext.AnswerRecorded {
		t.Fatalf("%+v", out)
	}
}

// One answer lands once whatever the provider repeats: a repeat is the
// recorded receipt, marked repeat, the provider not asked again and
// nothing written; the same response id with another content is
// response_conflict; applied once.
func TestHeldAnswerRecordedOnceAndRepeat(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	r := held(did, "h1", "ana", 0, 1)
	first := f.hand(t, r)
	if first.Status != ext.AnswerRecorded {
		t.Fatalf("%+v", first)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	asked := len(f.id.asks())
	rev := f.rec(t, did).Rev
	for range 3 {
		again := f.hand(t, r)
		if again.Status != ext.AnswerRecorded || !again.Repeat {
			t.Fatalf("the repeat: %+v", again)
		}
	}
	other := held(did, "h1", "ana", 1, 1)
	if out := f.hand(t, other); out.Status != ext.AnswerRefused || out.Code != crew.CodeResponseConflict {
		t.Fatalf("another content under the same id: %+v", out)
	}
	if n := len(f.id.asks()); n != asked {
		t.Fatalf("the provider was asked %d more times", n-asked)
	}
	f.unchanged(t, did, rev, 1)
}

// A kept refusal of a held answer is refused again from its row: an
// answer handed in after the decision was answered here, a late one
// (stale rev), and two hand-ins out of order (the second first).
func TestRepeatOfARefusalIsRefused(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	if out := f.hand(t, held(did, "h2", "bob", 1, 1)); out.Status != ext.AnswerRecorded {
		t.Fatalf("the second answer first: %+v", out)
	}
	late := held(did, "h1", "ana", 0, 1)
	out := f.hand(t, late)
	if out.Status != ext.AnswerRefused || out.Code != crew.CodeDecisionAnswered {
		t.Fatalf("the first answer after the second: %+v", out)
	}
	// The asks of h1 alone: h2's recheck at its application asks of h2 at
	// a moment of its own.
	asked := asksOf(f.id.asks(), "h1")
	again := f.hand(t, late)
	if again.Status != ext.AnswerRefused || again.Code != crew.CodeDecisionAnswered || !again.Repeat {
		t.Fatalf("the refusal again: %+v", again)
	}
	if asksOf(f.id.asks(), "h1") != asked {
		t.Fatal("the provider was asked again for a refusal kept")
	}
	if rec := f.rec(t, did); len(rec.Responses) != 2 {
		t.Fatalf("%d responses", len(rec.Responses))
	}
	stale := f.open(t, "q2")
	f.expect(t, http.MethodPost, "/api/decisions/"+stale+"/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "one more thing", "expectedRev": 1}, http.StatusOK, "")
	if out := f.hand(t, held(stale, "h3", "ana", 0, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeStaleDecision {
		t.Fatalf("a late answer: %+v", out)
	}
}

// A held answer to a run the engine does not hold is not_held, nothing
// written: the provider keeps it to hand in again.
func TestHeldAnswerToAStoppedRunIsNotHeld(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.expect(t, http.MethodPost, "/api/runs/"+f.run+"/stop", nil, http.StatusOK, "")
	if out := f.hand(t, held(did, "h1", "ana", 0, 1)); out.Status != ext.AnswerNotHeld {
		t.Fatalf("%+v", out)
	}
	f.unchanged(t, did, 1, 0)
}

// A forged hand-in is refused, nothing written: a statement the provider
// cannot verify (made for another server, another decision, response id or
// content); a check naming another decision, response or rev than its
// answer; a Host that is not the sync store's.
func TestForgedHeldAnswerRefused(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(func(a ext.AnswerCheck, _ ext.Grant) ext.Admission {
		if a.Statement == "statement of h-good" {
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	if out := f.hand(t, held(did, "h-forged", "ana", 0, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover {
		t.Fatalf("an unverified statement: %+v", out)
	}
	for name, mutate := range map[string]func(*ext.HeldAnswer){
		"another decision in the check": func(r *ext.HeldAnswer) { r.Check.Decision = "dr-99" },
		"another response id":           func(r *ext.HeldAnswer) { r.Check.ResponseID = "other" },
		"another rev":                   func(r *ext.HeldAnswer) { r.Check.ExpectedRev = 2 },
		"no fence":                      func(r *ext.HeldAnswer) { r.Fence = "" },
		"a sign-in and a device":        func(r *ext.HeldAnswer) { r.Check.AppDevice = "d-1" },
		"no statement":                  func(r *ext.HeldAnswer) { r.Check.Statement = "" },
	} {
		r := held(did, "h-good", "ana", 0, 1)
		mutate(&r)
		if _, err := f.h.RespondDecision(context.Background(), r); !errors.Is(err, ext.ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other := &extHost{s: f.srv, name: "another", ctx: context.Background()}
	if _, err := other.RespondDecision(context.Background(), held(did, "h-good", "ana", 0, 1)); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's Host: %v", err)
	}
	f.unchanged(t, did, 1, 0)
}

// A statement made for one decision's request part is refused for
// another's, though the two ask the same question: the server shows the
// provider the hash of its own record's part.
func TestSubstitutedAnswerRefused(t *testing.T) {
	f := newHeldFixture(t)
	one := f.open(t, "q1")
	two := f.open(t, "q2")
	d1, _, _ := f.srv.decisionDoc(t.Context(), 1)
	signed := held(one, "h1", "ana", 0, 1)
	f.id.set(func(a ext.AnswerCheck, _ ext.Grant) ext.Admission {
		// The statement signs dr-1's request part alone.
		if a.Statement == signed.Check.Statement && a.RequestSHA256 == d1.RequestSHA256 {
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	sub := held(two, "h1", "ana", 0, 1)
	sub.Check.Statement = signed.Check.Statement
	sub.Check.RequestSHA256 = d1.RequestSHA256
	if out := f.hand(t, sub); out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover {
		t.Fatalf("dr-1's statement on dr-2: %+v", out)
	}
	f.unchanged(t, two, 1, 0)
	if out := f.hand(t, signed); out.Status != ext.AnswerRecorded {
		t.Fatalf("dr-1's own: %+v", out)
	}
}

// Where the router confirms policies every held answer is refused
// answered_at_server, final, nothing written, neither the router nor the
// identity provider asked, whatever the provider would say.
func TestHeldAnswerRefusedWhereTheRouterConfirmsPolicies(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	routerAsked := 0
	f.srv.runs.SetRouterPolicies(true, func(context.Context, ledger.DecisionRecord, string, string) ext.Confirmation {
		routerAsked++
		return ext.Confirmation{}
	})
	for _, adm := range []ext.Admission{ext.AdmitYes, ext.AdmitUnknown} {
		f.id.set(always(adm))
		out := f.hand(t, held(did, "h1", "ana", 0, 1))
		if out.Status != ext.AnswerRefused || out.Code != crew.CodeAnsweredAtServer || !strings.Contains(out.Words, "an answer given elsewhere is not taken here") {
			t.Fatalf("%+v", out)
		}
	}
	if n := len(f.id.asks()); n != 0 || routerAsked != 0 {
		t.Fatalf("asked: the provider %d, the router %d", n, routerAsked)
	}
	f.unchanged(t, did, 1, 0)
}

// The held answer's id and fence are written with its row, in DR1's
// transaction: the accepted one's and a kept refusal's; a hand-in refused
// before DR1 leaves none.
func TestFenceWrittenInDR1sTransaction(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(always(ext.AdmitUnknown))
	f.hand(t, held(did, "h0", "ana", 0, 1))
	f.id.set(always(ext.AdmitYes))
	f.hand(t, held(did, "h1", "ana", 0, 1))
	r := held(did, "h2", "bob", 1, 1)
	r.Fence = "f-2"
	f.hand(t, r)
	rec := f.rec(t, did)
	if len(rec.Responses) != 2 {
		t.Fatalf("%d rows", len(rec.Responses))
	}
	for i, want := range []struct{ held, fence, status string }{{"h-h1", "f-1", ledger.ResponseAccepted}, {"h-h2", "f-2", ledger.ResponseRefused}} {
		var a crew.Authority
		_ = json.Unmarshal([]byte(rec.Responses[i].Authority), &a)
		if a.Held != want.held || a.Fence != want.fence || rec.Responses[i].Status != want.status || a.Via != crew.ViaSync {
			t.Errorf("row %d: %+v %s", i, a, rec.Responses[i].Status)
		}
	}
}

// SyncDelivery reads what the ledger holds for a held answer, its outcome
// as a repeat's and its fence, and writes nothing; LastDecision is the
// ledger's highest decision.
func TestSyncDeliveryReadsAndNeverWrites(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	if _, _, ok, err := f.h.SyncDelivery(t.Context(), did, "h1"); err != nil || ok {
		t.Fatalf("before the hand-in: %v %v", ok, err)
	}
	f.hand(t, held(did, "h1", "ana", 0, 1))
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
	rec := f.rec(t, did)
	out, fence, ok, err := f.h.SyncDelivery(t.Context(), did, "h1")
	if err != nil || !ok || fence != "f-1" || out.Status != ext.AnswerRecorded || !out.Repeat || out.Valid() != nil {
		t.Fatalf("the delivery: %+v %q %v %v", out, fence, ok, err)
	}
	f.unchanged(t, did, rec.Rev, len(rec.Responses))
	other := &extHost{s: f.srv, name: "another", ctx: context.Background()}
	if _, _, _, err := other.SyncDelivery(t.Context(), did, "h1"); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("another provider's Host: %v", err)
	}
}

// LastDecision reads the ledger's highest decision number.
func TestLastDecisionReadsTheLedger(t *testing.T) {
	f := newHeldFixture(t)
	if n, err := f.h.LastDecision(t.Context()); err != nil || n != 0 {
		t.Fatalf("with none: %d %v", n, err)
	}
	f.open(t, "q1")
	f.open(t, "q2")
	if n, err := f.h.LastDecision(t.Context()); err != nil || n != 2 {
		t.Fatalf("with two: %d %v", n, err)
	}
}

// At application a held answer is asked of the provider again from what
// the ledger keeps: while it cannot say, the decision stays answered and
// is asked again after the backoff, never applied and never superseded;
// once it says yes, applied.
func TestViaSyncUnknownWaitsAndNeverSupersedes(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(func(a ext.AnswerCheck, g ext.Grant) ext.Admission {
		if len(f.id.asks()) == 1 {
			return ext.AdmitYes // the door's
		}
		return ext.AdmitUnknown
	})
	f.hand(t, held(did, "h1", "ana", 0, 1))
	asks := f.id.waitAsks(t, 2)
	if asks[1].check.Statement != "statement of h1" || asks[1].grant != ext.GrantAdmin || asks[1].check.ContentSHA256 != asks[0].check.ContentSHA256 ||
		asks[1].check.RequestSHA256 != asks[0].check.RequestSHA256 || asks[1].check.ExpectedRev != 1 {
		t.Fatalf("the recheck was asked %+v, the door %+v", asks[1], asks[0])
	}
	for i := range 3 {
		var wait chan time.Time
		select {
		case wait = <-f.waits:
		case <-time.After(testWait):
			t.Fatal("the recheck never waited to ask again")
		}
		if rec := f.rec(t, did); rec.State != ledger.DecisionAnswered {
			t.Fatalf("after %d unknowns: %s", i+1, rec.State)
		}
		wait <- time.Now()
		f.id.waitAsks(t, 3+i)
	}
	if rec := f.rec(t, did); rec.State != ledger.DecisionAnswered || rec.Applied != "" {
		t.Fatalf("after the unknowns: %s", rec.State)
	}
	f.id.set(always(ext.AdmitYes))
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case w := <-f.waits:
				w <- time.Now()
			case <-done:
				return
			}
		}
	}()
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
}

// A sign-in revoked after the answer was handed in: the recheck says no
// and the decision is superseded, its answer never applied.
func TestRevokedSignInAfterDeliveryNotApplied(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.id.set(func(ext.AnswerCheck, ext.Grant) ext.Admission {
		if len(f.id.asks()) == 1 {
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	f.hand(t, held(did, "h1", "ana", 0, 1))
	d := f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
	if d["applied"] != nil || !strings.Contains(d["why"].(string), "user:ana's answer could not be rechecked") {
		t.Fatalf("%v", d)
	}
}

// A held answer at every field's bound, its statement 2 KiB, is recorded:
// its authority keeps within the ledger's bound; one past 2 KiB is refused
// by HeldAnswer.Valid.
func TestSyncAuthorityAtEveryBoundRecorded(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	r := held(did, strings.Repeat("r", 32), strings.Repeat("s", ext.MaxSubject), 0, 1)
	id64 := strings.Repeat("i", ext.MaxHeldID)
	r.Check.Membership, r.Check.SignIn, r.Held, r.Fence = id64, id64, id64, id64
	r.Check.At = time.Date(2026, 11, 20, 9, 0, 0, 123456789, time.FixedZone("x", 3600))
	r.Check.Statement = strings.Repeat("t", ext.MaxStatement)
	if out := f.hand(t, r); out.Status != ext.AnswerRecorded {
		t.Fatalf("%+v", out)
	}
	rec := f.rec(t, did)
	if n := len(rec.Responses[0].Authority); n > 700 {
		t.Fatalf("the authority is %d bytes", n)
	}
	r.Check.Statement += "t"
	r.ResponseID = "past"
	r.Check.ResponseID = "past"
	if _, err := f.h.RespondDecision(context.Background(), r); !errors.Is(err, ext.ErrRefused) {
		t.Fatalf("a statement past its bound: %v", err)
	}
}

// A decision given to the decide grant is asked of the provider with the
// decide grant, and a decide principal's held answer to it is recorded;
// its document says it is opened to the grant.
func TestHeldAnswerToAGivenDecisionAsksDecide(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/route", map[string]any{"opId": "give-1", "to": map[string]any{"kind": "grant", "grant": "decide"}, "expectedRev": 1}, http.StatusOK, "")
	rev := f.rec(t, did).Rev
	f.id.set(func(_ ext.AnswerCheck, g ext.Grant) ext.Admission {
		if g == ext.GrantDecide {
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	if doc, _, _ := f.srv.decisionDoc(t.Context(), 1); doc.OpenedTo != ext.DecisionOpenedToDecide {
		t.Fatalf("opened to %q", doc.OpenedTo)
	}
	if out := f.hand(t, held(did, "h1", "dan", 0, rev)); out.Status != ext.AnswerRecorded {
		t.Fatalf("%+v", out)
	}
	var a crew.Authority
	_ = json.Unmarshal([]byte(f.rec(t, did).Responses[0].Authority), &a)
	if a.Grant != string(ext.GrantDecide) || a.Subject != "dan" {
		t.Fatalf("the authority: %+v", a)
	}
	// An operator's decision asks admin, which dan does not hold.
	other := f.open(t, "q2")
	if out := f.hand(t, held(other, "h2", "dan", 0, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover {
		t.Fatalf("dan on the operator's decision: %+v", out)
	}
}

// What the door refuses before the provider is asked, nothing written: a
// request kept on the machine, a choice past the options, a text the
// ledger would keep other than signed.
func TestHeldAnswerRefusedBeforeItIsAsked(t *testing.T) {
	f := newHeldFixture(t)
	did := f.open(t, "q1")
	choice := held(did, "h1", "ana", 2, 1)
	if out := f.hand(t, choice); out.Status != ext.AnswerRefused || out.Code != crew.CodeInvalid {
		t.Fatalf("a choice past the options: %+v", out)
	}
	spaced := held(did, "h2", "ana", 0, 1)
	spaced.Text = "yes "
	if out := f.hand(t, spaced); out.Status != ext.AnswerRefused || out.Code != crew.CodeInvalid {
		t.Fatalf("a text the ledger would keep otherwise: %+v", out)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.srv.store.Dir(), "runs", ledger.File)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE decision_output SET verdict = 'output', cut_at = 0 WHERE decision = 1 AND part = 'question'"); err != nil {
		t.Fatal(err)
	}
	if out := f.hand(t, held(did, "h3", "ana", 0, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover || !strings.Contains(out.Words, "request is kept on its server") {
		t.Fatalf("a kept request: %+v", out)
	}
	if n := len(f.id.asks()); n != 0 {
		t.Fatalf("the provider was asked %d times", n)
	}
	f.unchanged(t, did, 1, 0)
}

// asksOf counts the asks of the response id.
func asksOf(asks []answerAsk, responseID string) int {
	n := 0
	for _, a := range asks {
		if a.check.ResponseID == responseID {
			n++
		}
	}
	return n
}

// openWithContext opens an approval of the attempt whose context is long
// enough to be judged, and answers its id.
func (f *heldFixture) openWithContext(t *testing.T, requestID string) string {
	t.Helper()
	body := approvalBody(requestID)
	body["context"] = "the migration touches every table of the billing schema"
	return entityOf(t, f.ask(t, body, http.StatusCreated, ""), "decision")["id"].(string)
}

// holdIndex holds the index's next feed until the func it answers is
// called (and at the test's end, before the server's stop waits for it).
func holdIndex(t *testing.T, x *outputIndex) func() {
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	x.enqueue(func() { <-gate })
	return release
}

// A request with a part still pending is kept back whole, so nothing is
// answered against a form that will change; settled, it leaves, and a
// hand-in made against it is admitted and applied.
func TestAPendingRequestIsAnsweredOnceSettled(t *testing.T) {
	f := newHeldFixture(t)
	release := holdIndex(t, f.srv.outputs)
	did := f.openWithContext(t, "q1")
	if v, _, _ := f.srv.ledger.OutputVerdicts(t.Context(), 1); v[ledger.PartContext] != verdictPending {
		t.Fatalf("the context with the index held: %v", v)
	}
	if doc, _, _ := f.srv.decisionDoc(t.Context(), 1); doc.Request != nil {
		t.Fatalf("a request with a pending part left: %+v", doc.Request)
	}
	if out := f.hand(t, held(did, "h0", "ana", 0, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeNotApprover {
		t.Fatalf("a hand-in to a pending request: %+v", out)
	}
	release()
	verdictWhen(t, f.srv.ledger, 1, ledger.PartContext, verdictClean)
	doc, _, _ := f.srv.decisionDoc(t.Context(), 1)
	if doc.Request == nil || doc.Request.Context == outputCutWords {
		t.Fatalf("the settled request: %+v", doc.Request)
	}
	f.id.set(func(a ext.AnswerCheck, _ ext.Grant) ext.Admission {
		if a.RequestSHA256 == doc.RequestSHA256 {
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	if out := f.hand(t, held(did, "h1", "ana", 0, 1)); out.Status != ext.AnswerRecorded {
		t.Fatalf("a hand-in against the settled request: %+v", out)
	}
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
}

// The recheck shows the provider the request hash the door admitted and
// the time in the zone the hand-in gave, whatever the document is by
// then: an answer admitted is applied though its document's form changed.
func TestTheRecheckShowsTheHashTheDoorAdmitted(t *testing.T) {
	f := newHeldFixture(t)
	did := f.openWithContext(t, "q1")
	verdictWhen(t, f.srv.ledger, 1, ledger.PartContext, verdictClean)
	admitted, _, _ := f.srv.decisionDoc(t.Context(), 1)
	r := held(did, "h1", "ana", 0, 1)
	r.Check.At = time.Date(2026, 11, 20, 10, 0, 0, 5, time.FixedZone("CET", 3600))
	made := r.Check.At.Format(time.RFC3339Nano)
	f.id.set(func(a ext.AnswerCheck, _ ext.Grant) ext.Admission {
		switch {
		case len(f.id.asks()) == 2:
			return ext.AdmitUnknown // the recheck's first ask
		case a.RequestSHA256 == admitted.RequestSHA256 && a.At.Format(time.RFC3339Nano) == made:
			return ext.AdmitYes
		}
		return ext.AdmitNo
	})
	if out := f.hand(t, r); out.Status != ext.AnswerRecorded {
		t.Fatalf("%+v", out)
	}
	var wait chan time.Time
	select {
	case wait = <-f.waits:
	case <-time.After(testWait):
		t.Fatal("the recheck never waited to ask again")
	}
	// The document's form changes at the same rev (its context judged
	// output since).
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.srv.store.Dir(), "runs", ledger.File)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE decision_output SET verdict = 'output', cut_at = 0 WHERE decision = 1 AND part = 'context'"); err != nil {
		t.Fatal(err)
	}
	if now, _, _ := f.srv.decisionDoc(t.Context(), 1); now.RequestSHA256 == admitted.RequestSHA256 {
		t.Fatal("the document's form did not change")
	}
	wait <- time.Now()
	f.decisionWhen(t, did, "applied", inState(ledger.DecisionApplied))
}

// A replacement opened with a decision's request carries its verdicts: an
// agent's question judged output stays back in the replacement though the
// check would judge it clean now, and a request never judged
// as it was written stays back in its replacement too. The replacements
// here come of an identity's answer its recheck refused.
func TestAReplacementsDocumentKeepsTheRequestBack(t *testing.T) {
	ctx := context.Background()
	t.Run("judged output", func(t *testing.T) {
		f := newHeldFixture(t)
		logText := keptCheckLog(t, f.srv, "team-0000c0de", 2041)
		body := approvalBody("q1")
		question := "Ship it? The check said " + logText[800:900]
		body["question"] = question
		did := entityOf(t, f.ask(t, body, http.StatusCreated, ""), "decision")["id"].(string)
		verdictWhen(t, f.srv.ledger, 1, ledger.PartQuestion, verdictOutput)
		// The check would find it clean now.
		f.srv.ledger.SetOutputCheck(func(context.Context, string, bool) string { return verdictClean })
		f.replaced(t, did)
	})
	t.Run("never judged", func(t *testing.T) {
		f := newHeldFixture(t)
		did := f.open(t, "q1")
		db, err := sql.Open("sqlite", "file:"+filepath.Join(f.srv.store.Dir(), "runs", ledger.File)+"?_pragma=busy_timeout(10000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec("DELETE FROM decision_output WHERE decision = 1"); err != nil {
			t.Fatal(err)
		}
		f.replaced(t, did)
		if v, _, _ := f.srv.ledger.OutputVerdicts(ctx, 2); v[ledger.PartQuestion] != "" {
			t.Fatalf("the replacement's verdicts: %v", v)
		}
	})
}

// replaced has did answered by an identity whose recheck refuses it, and
// fails t unless its replacement, dr-2, reaches the sync store with its
// request kept back.
func (f *heldFixture) replaced(t *testing.T, did string) {
	t.Helper()
	f.id.mu.Lock()
	f.id.once["tok-ana-6d0e"] = cloudAdmin("ana")
	f.id.mu.Unlock()
	f.respond(t, "tok-ana-6d0e", did, "a1", 0, f.rec(t, did).Rev, http.StatusCreated, "")
	f.decisionWhen(t, did, "superseded", inState(ledger.DecisionSuperseded))
	key := syncKey{syncDecisionStore, "dr-2"}
	calls := f.store.until(t, "the replacement", func(c []syncCall) bool {
		_, op, ok := docOf(c, key)
		return ok && op == "saved"
	})
	if doc, _, _ := docOf(calls, key); doc.Request != nil || doc.RequestKept != ext.DecisionRequestKeptOutput {
		t.Fatalf("the replacement's request left: %+v", doc.Request)
	}
}

// What the store is handed about a refusal is what the document would
// show: a stale refusal whose words quote a comment that pasted output
// hands neither its words nor its receipt's, through the outcome or
// SyncDelivery.
func TestARefusalHandsNoOutput(t *testing.T) {
	f := newHeldFixture(t)
	logText := keptCheckLog(t, f.srv, "team-0000c0de", 3061)
	excerpt := logText[800:900]
	did := f.open(t, "q1")
	f.expect(t, http.MethodPost, "/api/decisions/"+did+"/comment", map[string]any{"opId": "c1", "kind": "comment", "text": "the log said " + excerpt, "expectedRev": 1}, http.StatusOK, "")
	out := f.hand(t, held(did, "h1", "ana", 0, 1))
	if out.Status != ext.AnswerRefused || out.Code != crew.CodeStaleDecision {
		t.Fatalf("%+v", out)
	}
	rec := f.rec(t, did)
	if len(rec.Responses) != 1 || !strings.Contains(rec.Responses[0].Receipt, excerpt[20:60]) {
		t.Fatalf("the kept refusal does not quote the comment: %+v", rec.Responses)
	}
	delivered, _, ok, err := f.h.SyncDelivery(t.Context(), did, "h1")
	if err != nil || !ok {
		t.Fatalf("the delivery: %v %v", ok, err)
	}
	for name, o := range map[string]ext.AnswerOutcome{"the outcome": out, "the delivery": delivered} {
		// The receipt as the store reads it (its bytes, not their base64).
		for _, text := range []string{o.Words, string(o.Receipt)} {
			if strings.Contains(text, excerpt[20:60]) || strings.Contains(text, "the log said") {
				t.Errorf("%s holds the comment's output: %s", name, text)
			}
		}
		if len(o.Receipt) == 0 {
			t.Errorf("%s has no receipt", name)
		}
	}
}

// An assignment is answered from elsewhere like any decision: its choices
// are its type's two fixed answers, and its document carries the one
// chosen.
func TestAnAssignmentIsAnsweredFromElsewhere(t *testing.T) {
	f := newHeldFixture(t)
	did := entityOf(t, f.ask(t, map[string]any{"requestId": "a1", "type": "assignment", "question": "Run the migration on staging"}, http.StatusCreated, ""), "decision")["id"].(string)
	if out := f.hand(t, held(did, "h1", "ana", 0, 1)); out.Status != ext.AnswerRecorded {
		t.Fatalf("a held answer to an assignment: %+v", out)
	}
	if out := f.hand(t, held(did, "h2", "ana", 2, 1)); out.Status != ext.AnswerRefused || out.Code != crew.CodeInvalid {
		t.Fatalf("a choice past its two answers: %+v", out)
	}
	n, _ := ledger.ParseDecisionID(did)
	doc, _, err := f.srv.decisionDoc(t.Context(), n)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range doc.Responses {
		if r.Status == ledger.ResponseAccepted && (r.Choice == nil || *r.Choice != 0) {
			t.Fatalf("the accepted choice: %+v", r)
		}
	}
	if len(doc.Responses) == 0 {
		t.Fatal("no response in the document")
	}
}
