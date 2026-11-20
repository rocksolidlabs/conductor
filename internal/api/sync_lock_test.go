package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

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
