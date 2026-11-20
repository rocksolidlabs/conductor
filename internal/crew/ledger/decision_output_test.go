package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// OnDecisions is told, after each committed transaction, the decisions it
// changed (opened, responded to, answered, put), each once; a transaction
// that changed none tells nothing; a prune tells the decisions it deleted;
// a transaction that failed tells nothing.
func TestOnDecisionsNamesTheDecisionsChanged(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	var mu sync.Mutex
	var told [][]int64
	l.OnDecisions(func(ns []int64) {
		mu.Lock()
		defer mu.Unlock()
		told = append(told, slices.Clone(ns))
	})
	last := func() [][]int64 {
		mu.Lock()
		defer mu.Unlock()
		out := told
		told = nil
		return out
	}
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "deploy")
	last()
	n := open(t, l, gateDecision(run, "g1"))
	if got := last(); len(got) != 1 || !slices.Equal(got[0], []int64{n}) {
		t.Fatalf("an opening told %v", got)
	}
	m := open(t, l, gateDecision(run, "g2"))
	last()
	answer(t, l, run, n, "r1", workbenchAuth)
	if got := last(); len(got) != 1 || !slices.Equal(got[0], []int64{n}) {
		t.Fatalf("a response and its answer told %v", got)
	}
	in(t, l, run, func(tx *Tx) error { return tx.Event("note", map[string]any{"x": 1}) })
	if got := last(); len(got) != 0 {
		t.Fatalf("a transition changing no decision told %v", got)
	}
	if err := try(l, run, func(tx *Tx) error {
		if _, err := tx.Respond(m, ResponseRow{Actor: "admin", Authority: workbenchAuth, ResponseID: "r2", Fingerprint: "fp", Status: ResponseAccepted, Receipt: `{}`}); err != nil {
			return err
		}
		return errors.New("undone")
	}); err == nil {
		t.Fatal("the failing transition committed")
	}
	if got := last(); len(got) != 0 {
		t.Fatalf("a transition that failed told %v", got)
	}
	setState(t, l, run, n, DecisionApplied, `{"how":"released","words":"done"}`)
	setState(t, l, run, m, DecisionCancelled, "")
	last()
	finish(t, l, run)
	if err := l.Prune(ctx, run); err != nil {
		t.Fatal(err)
	}
	if got := last(); len(got) != 1 || !slices.Equal(got[0], []int64{n, m}) {
		t.Fatalf("a prune told %v", got)
	}
}

// With a check given, every free-text part of a decision is judged in the
// transaction that writes it: the request's fields at its opening (an
// agent's as an agent's), a response's text, rationale and words, the
// application's words and why; a held answer's statement is kept beside
// its response. With none, no verdict is written.
func TestDecisionOutputWrittenWithItsPart(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	var mu sync.Mutex
	agents := map[string]bool{}
	l.SetOutputCheck(func(_ context.Context, text string, agent bool) string {
		mu.Lock()
		agents[text] = agent
		mu.Unlock()
		if strings.Contains(text, "PANIC") {
			return VerdictOutput
		}
		return VerdictClean
	})
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "build")
	d := agentDecision(run, "build", 1, "q1")
	d.Request = `{"question":"Which table?","context":"the log said PANIC: boom","options":[{"label":"users","tradeoffs":"slow"},{"label":"orders"}],"subject":{"kind":"commit","commit":"abc123"}}`
	n := open(t, l, d)
	v, kept, err := l.OutputVerdicts(ctx, n)
	if err != nil || !kept {
		t.Fatalf("the verdicts: %v %v", kept, err)
	}
	want := map[string]string{PartQuestion: VerdictClean, PartContext: VerdictOutput, PartLabel(0): VerdictClean, PartTradeoffs(0): VerdictClean, PartLabel(1): VerdictClean}
	for p, w := range want {
		if v[p] != w {
			t.Errorf("%s: %q, want %q (%v)", p, v[p], w, v)
		}
	}
	if !agents["Which table?"] {
		t.Fatal("an agent's request judged as a person's")
	}
	in(t, l, run, func(tx *Tx) error {
		cur, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		if _, err := tx.Respond(n, ResponseRow{Actor: "user:ana", Authority: syncAuth("signed-statement"), ResponseID: "r1", Fingerprint: "fp", Status: ResponseAccepted,
			Receipt: `{"text":"use users","rationale":"PANIC was in the log","words":"recorded"}`, Statement: "signed-statement"}); err != nil {
			return err
		}
		_, err = tx.Answer(n, run, cur.Rev)
		return err
	})
	v, _, _ = l.OutputVerdicts(ctx, n)
	if v[PartResponse(1, "text")] != VerdictClean || v[PartResponse(1, "rationale")] != VerdictOutput || v[PartResponse(1, "words")] != VerdictClean {
		t.Fatalf("the response's verdicts: %v", v)
	}
	if agents["use users"] {
		t.Fatal("a person's answer judged as an agent's")
	}
	if s, ok, err := l.Statement(ctx, n, 1); err != nil || !ok || s != "signed-statement" {
		t.Fatalf("the statement: %q %v %v", s, ok, err)
	}
	in(t, l, run, func(tx *Tx) error {
		cur, _, err := tx.Decision(n)
		if err != nil {
			return err
		}
		cur.State, cur.Rev, cur.Why, cur.Applied = DecisionApplied, cur.Rev+1, "applied as PANIC said", `{"how":"typed","words":"typed into the session"}`
		return tx.PutDecision(cur)
	})
	v, _, _ = l.OutputVerdicts(ctx, n)
	if v[PartWhy] != VerdictOutput || v[PartApplied] != VerdictClean {
		t.Fatalf("the put's verdicts: %v", v)
	}

	plain := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	newRun(t, plain, run)
	tasksOf(t, plain, run, "build")
	m := open(t, plain, d)
	if v, _, err := plain.OutputVerdicts(ctx, m); err != nil || len(v) != 0 {
		t.Fatalf("verdicts with no check: %v %v", v, err)
	}
}

// A pending part is settled once, to what was judged; a settled one stays.
// Withholding makes every pending part output; each tells OnDecisions.
func TestPendingDecisionOutputSettledOrWithheld(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	l.SetOutputCheck(func(context.Context, string, bool) string { return VerdictPending })
	var mu sync.Mutex
	var told []int64
	l.OnDecisions(func(ns []int64) { mu.Lock(); told = append(told, ns...); mu.Unlock() })
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "deploy")
	n := open(t, l, gateDecision(run, "g1"))
	m := open(t, l, gateDecision(run, "g2"))
	mu.Lock()
	told = nil
	mu.Unlock()
	if err := l.SettleOutput(ctx, n, PartQuestion, VerdictClean); err != nil {
		t.Fatal(err)
	}
	if err := l.SettleOutput(ctx, n, PartQuestion, VerdictOutput); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := l.OutputVerdicts(ctx, n); v[PartQuestion] != VerdictClean || v[PartLabel(0)] != VerdictPending {
		t.Fatalf("after the settle: %v", v)
	}
	if p, err := l.PendingOutputs(ctx, 100); err != nil || len(p) == 0 || slices.Contains(p, PendingOutput{n, PartQuestion}) {
		t.Fatalf("the pending parts: %v %v", p, err)
	}
	if err := l.WithholdPendingOutput(ctx); err != nil {
		t.Fatal(err)
	}
	for _, d := range []int64{n, m} {
		v, _, _ := l.OutputVerdicts(ctx, d)
		for p, s := range v {
			if s == VerdictPending || (d == m && s != VerdictOutput) {
				t.Fatalf("%s %s is %s after the withholding", DecisionID(d), p, s)
			}
		}
	}
	if v, _, _ := l.OutputVerdicts(ctx, n); v[PartQuestion] != VerdictClean {
		t.Fatalf("a settled part withheld: %v", v)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(told, n) || !slices.Contains(told, m) {
		t.Fatalf("OnDecisions was told %v", told)
	}
}

// syncAuth is a held answer's authority whose statement is statement.
func syncAuth(statement string) string {
	sum := sha256.Sum256([]byte(statement))
	return `{"grant":"admin","via":"sync","subject":"ana","boot":"boot-1","membership":"m-1","signIn":"s-1","at":"2026-11-20T09:00:00Z","statementSha256":"` +
		hex.EncodeToString(sum[:]) + `","requestSha256":"` + strings.Repeat("a", 64) + `","held":"h-1","fence":"f-1"}`
}

// A held answer's authority names every one of its fields and only it
// does; its statement is the one whose SHA-256 it names, and only a held
// answer keeps one.
func TestSyncAuthorityAndItsStatement(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	const run = "team-00000001"
	newRun(t, l, run)
	tasksOf(t, l, run, "deploy")
	n := open(t, l, gateDecision(run, "g1"))
	respond := func(id, auth, statement string) error {
		return try(l, run, func(tx *Tx) error {
			_, err := tx.Respond(n, ResponseRow{Actor: "user:ana", Authority: auth, ResponseID: id, Fingerprint: "fp-" + id, Status: ResponseRefused, Receipt: `{}`, Statement: statement})
			return err
		})
	}
	good := syncAuth("st")
	for name, c := range map[string][2]string{
		"no fence":                {strings.Replace(good, `,"fence":"f-1"`, "", 1), "st"},
		"no request hash":         {strings.Replace(good, `"requestSha256":"`, `"requestSha256":"x`, 1), "st"},
		"a sign-in and a device":  {strings.Replace(good, `"signIn":"s-1"`, `"signIn":"s-1","appDevice":"d-1"`, 1), "st"},
		"another statement":       {good, "st2"},
		"no statement":            {good, ""},
		"an identity's fence":     {`{"grant":"admin","via":"identity","subject":"ana","boot":"b","fence":"f-1"}`, ""},
		"an identity's statement": {identityAuth, "st"},
	} {
		if err := respond("r-"+strings.ReplaceAll(name, " ", "-"), c[0], c[1]); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if err := respond("r-good", good, "st"); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := l.Statement(context.Background(), n, 1); err != nil || !ok || s != "st" {
		t.Fatalf("the statement: %q %v %v", s, ok, err)
	}
	if got, err := l.LastDecision(context.Background()); err != nil || got != n {
		t.Fatalf("LastDecision: %d %v", got, err)
	}
}
