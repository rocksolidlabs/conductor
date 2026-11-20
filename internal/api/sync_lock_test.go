package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
)

// The ledger's check of a decision's parts runs under the ledger's lock:
// it never waits for the index and never reads a kept source (which would
// take that lock again); what it cannot judge now is pending, and the
// settle loop judges it outside the lock.

// keptCheckLog writes a finished run whose check kept a log of random
// words, and waits for the index to hold it; it answers the log.
func keptCheckLog(t *testing.T, srv *Server, run string, seed uint64) string {
	t.Helper()
	ctx := context.Background()
	logText := words(rand.New(rand.NewPCG(seed, seed+1)), 3000)
	sum := sha256.Sum256([]byte(logText))
	if _, err := srv.ledger.Transition(ctx, run, func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutRun(ledger.RunRow{ID: run, CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: ledger.RunFinished, CreatedAt: now, ClosedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptSucceeded, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, State: ledger.CheckFinished,
				Log: []byte(logText), LogSHA256: hex.EncodeToString(sum[:]), StartedAt: now, FinishedAt: now}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !srv.outputs.whole(ctx) {
		t.Fatal("the index is not whole")
	}
	return logText
}

// verdictWhen waits until decision n's part has the verdict want.
func verdictWhen(t *testing.T, ld *ledger.Ledger, n int64, part, want string) {
	t.Helper()
	waitUntil(t, "dr-"+part+" "+want, func() bool {
		v, _, err := ld.OutputVerdicts(context.Background(), n)
		return err == nil && v[part] == want
	})
}

// A decision whose context quotes a check's log the ledger keeps opens
// (the check does not read the ledger inside its own transaction), its
// context pending, and the settle judges it output.
func TestADecisionQuotingAKeptLogOpens(t *testing.T) {
	f := newHeldFixture(t)
	logText := keptCheckLog(t, f.srv, "team-0000c0de", 1037)
	opened := make(chan int64, 1)
	go func() {
		var n int64
		_, err := f.srv.ledger.Transition(context.Background(), f.run, func(tx *ledger.Tx) error {
			d, _, err := tx.OpenDecision(ledger.DecisionRow{RunID: f.run, Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: "g1", Plan: "pl-1", PlanDigest: "d1",
				Fingerprint: "fp-g1", Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`,
				Request: `{"question":"Ship it?","context":"the check said ` + logText[800:900] + `","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`})
			n = d.N
			return err
		})
		if err != nil {
			n = -1
		}
		opened <- n
	}()
	var n int64
	select {
	case n = <-opened:
	case <-time.After(testWait):
		t.Fatal("the decision's opening never returned: its check waits on the ledger it is inside")
	}
	if n <= 0 {
		t.Fatal("the decision did not open")
	}
	verdictWhen(t, f.srv.ledger, n, ledger.PartContext, verdictOutput)
	if v, _, _ := f.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartQuestion] != verdictClean {
		t.Fatalf("the question: %v", v)
	}
}

// A transaction's judging never waits for the index: with a feed held, the
// opening writes its parts pending at once, and the settle judges them
// once the index is whole.
func TestTheLedgersCheckNeverWaitsForTheIndex(t *testing.T) {
	f := newHeldFixture(t)
	x := f.srv.outputs
	gate := make(chan struct{})
	var release sync.Once
	open := func() { release.Do(func() { close(gate) }) }
	// Let go before the server's stop, which waits for the index.
	t.Cleanup(open)
	x.enqueue(func() { <-gate })
	before := x.waited.Load()
	n := openPlanDecision(t, f.srv.ledger, f.run, "g1", `{"question":"Ship the migration to every region?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	if got := x.waited.Load(); got != before {
		t.Fatalf("the opening waited for the index %d times", got-before)
	}
	if v, _, _ := f.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartQuestion] != verdictPending {
		t.Fatalf("the question with the index held: %v", v)
	}
	open()
	verdictWhen(t, f.srv.ledger, n, ledger.PartQuestion, verdictClean)
}

// putCheckLog writes, through ld, a finished run whose check kept a log
// of random words, and answers the log.
func putCheckLog(t *testing.T, ld *ledger.Ledger, run string, seed uint64) string {
	t.Helper()
	logText := words(rand.New(rand.NewPCG(seed, seed+1)), 3000)
	sum := sha256.Sum256([]byte(logText))
	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		now := tx.Now()
		for _, err := range []error{
			tx.PutRun(ledger.RunRow{ID: run, CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: ledger.RunFinished, CreatedAt: now, ClosedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskSucceeded, Attempts: 1, ResultAttempt: 1}),
			tx.PutAttempt(ledger.AttemptRow{Where: ledger.WhereNative, TaskID: "build", N: 1, Member: "lead", State: ledger.AttemptSucceeded, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{Where: ledger.WhereNative, TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, State: ledger.CheckFinished,
				Log: []byte(logText), LogSHA256: hex.EncodeToString(sum[:]), StartedAt: now, FinishedAt: now}),
		} {
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return logText
}

// A decision written with no check is judged at its document's first
// build and the verdict kept: a response quoting a check's log is cut, and
// still cut once that log has left the index, its row there.
func TestTheFirstBuildsVerdictIsKept(t *testing.T) {
	dir := syncDataDir(t)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	logText := putCheckLog(t, ld, "team-0000c0de", 4073)
	quote := "as the check said, " + logText[800:900]
	const run = "team-00000001"
	newLedgerRun(t, ld, run, ledger.RunRunning)
	n := openPlanDecision(t, ld, run, "g1", `{"question":"Ship the migration?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	if _, err := ld.Transition(context.Background(), run, func(tx *ledger.Tx) error {
		d, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		receipt, _ := json.Marshal(map[string]any{"code": "", "words": "recorded", "choice": 0, "text": quote})
		if _, err := tx.Respond(n, ledger.ResponseRow{Actor: "admin", Authority: `{"grant":"admin","via":"workbench","boot":"b"}`, ResponseID: "r1", Fingerprint: "fp",
			Status: ledger.ResponseAccepted, Receipt: string(receipt)}); err != nil {
			return err
		}
		_, err = tx.Answer(n, run, d.Rev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := ld.OutputVerdicts(context.Background(), n); len(v) != 0 {
		t.Fatalf("verdicts written with no check: %v", v)
	}
	ld.Close()

	f := newFakeSync()
	e := syncEnv(t, dir, f)
	key := syncKey{syncDecisionStore, ledger.DecisionID(n)}
	calls := f.until(t, "the decision", func(c []syncCall) bool {
		_, _, ok := docOf(c, key)
		return ok
	})
	text := func(doc ext.DecisionDoc) string {
		for _, r := range doc.Responses {
			if r.Status == ledger.ResponseAccepted {
				return r.Text
			}
		}
		return "none"
	}
	if doc, _, _ := docOf(calls, key); text(doc) != outputCutWords {
		t.Fatalf("the first build: %q", text(doc))
	}
	if v, _, _ := e.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartResponse(1, "text")] != verdictOutput {
		t.Fatalf("the first build's verdict was not kept: %v", v)
	}
	if err := e.srv.ledger.Prune(context.Background(), "team-0000c0de"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the quote judged clean now", func() bool { return e.srv.copyCheck.Judge(context.Background(), quote, false) == verdictClean })
	doc, _, err := e.srv.decisionDoc(context.Background(), n)
	if err != nil || text(doc) != outputCutWords {
		t.Fatalf("a later build: %q %v", text(doc), err)
	}
}

// withSettleTick sets the decisions' settle loop's tick: a long one leaves
// the settling to the wake a pending write gives (pokeSettle).
func withSettleTick(d time.Duration) Option {
	return func(o *options) { o.settleTick = d }
}

// syncEnvWith is syncEnv with the server's options.
func syncEnvWith(t *testing.T, dir string, p ext.ContinuityStore, opts ...Option) *testEnv {
	t.Helper()
	providers := local.Fill(ext.Providers{Continuity: p, All: []ext.Entry{{Kind: ext.KindSync, Name: p.Name(), Value: p}}})
	return newTestEnvAgents(t, func(c *config.Config) { c.DataDir = dir }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, append([]Option{WithExtensions(providers)}, opts...)...)
}

// A part written pending is settled as soon as the feeds in flight are
// indexed, woken by its own commit (the tick here never comes): an
// unrelated source pruned a moment later leaves it clean.
func TestAPendingPartSettlesAtOnce(t *testing.T) {
	f := newFakeSync()
	e := syncEnvWith(t, syncDataDir(t), f, withSettleTick(time.Hour))
	const run = "team-00000001"
	newLedgerRun(t, e.srv.ledger, run, ledger.RunRunning)
	e.srv.outputs.feed("ring:other", []byte(strings.Repeat("another session's output, line by line\n", 20)), 1<<20)
	release := holdIndex(t, e.srv.outputs)
	n := openPlanDecision(t, e.srv.ledger, run, "g1", `{"question":"Ship the migration to every region?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	if v, _, _ := e.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartQuestion] != verdictPending {
		t.Fatalf("the question with the index held: %v", v)
	}
	release()
	verdictWhen(t, e.srv.ledger, n, ledger.PartQuestion, verdictClean)
	e.srv.outputs.prune("ring:other")
	if !e.srv.outputs.whole(context.Background()) {
		t.Fatal("not whole")
	}
	if v, _, _ := e.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartQuestion] != verdictClean {
		t.Fatalf("after an unrelated prune: %v", v)
	}
}

// A server that starts while another holds its ledger withholds what that
// one left pending as it takes the owner lock, and nothing it writes
// pending itself after: its own pending part is settled clean (woken by
// its commit, the tick never coming) and its document reaches the store
// with its request.
func TestPendingPartsSettleAfterAHandOver(t *testing.T) {
	was := ledger.LockWait
	ledger.LockWait = testWait
	t.Cleanup(func() { ledger.LockWait = was })
	dir := syncDataDir(t)
	other, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetOutputCheck(func(context.Context, string, bool) string { return ledger.VerdictPending })
	const run = "team-00000001"
	newLedgerRun(t, other, run, ledger.RunRunning)
	left := openPlanDecision(t, other, run, "g0", `{"question":"Left pending by the server before?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	f := newFakeSync()
	e := syncEnvWith(t, dir, f, withSettleTick(time.Hour))
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server took the owner lock another ledger holds")
	}
	other.Close()
	waitUntil(t, "the ledger the server's", func() bool {
		ro, _ := e.srv.ledger.ReadOnly()
		return !ro
	})
	if v, _, _ := e.srv.ledger.OutputVerdicts(context.Background(), left); v[ledger.PartQuestion] != verdictOutput {
		t.Fatalf("what the server before left pending: %v", v)
	}
	release := holdIndex(t, e.srv.outputs)
	n := openPlanDecision(t, e.srv.ledger, run, "g1", `{"question":"Ship the migration to every region?","options":[{"label":"Approve"},{"label":"Reject"}],"subject":{"kind":"plan","plan":"pl-1","digest":"d1"}}`)
	if v, _, _ := e.srv.ledger.OutputVerdicts(context.Background(), n); v[ledger.PartQuestion] != verdictPending {
		t.Fatalf("the question with the index held: %v", v)
	}
	release()
	verdictWhen(t, e.srv.ledger, n, ledger.PartQuestion, verdictClean)
	key := syncKey{syncDecisionStore, ledger.DecisionID(n)}
	f.until(t, "the decision with its request", func(c []syncCall) bool {
		doc, _, ok := docOf(c, key)
		return ok && doc.Request != nil && doc.Request.Question == "Ship the migration to every region?"
	})
}
