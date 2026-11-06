package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/export"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The history's indexer (docs/plans/history.md, PR-3). Every wait is on
// the indexer itself (settle: nothing left to read; reconcileNow) or on a
// store's own rows, never on a clock.

// histSettle waits until e's indexer has read every source it was told
// of, and answers its index.
func histSettle(t *testing.T, e *testEnv) *history.Index {
	t.Helper()
	return histWait(t, e, false)
}

// histReconcile reconciles e's indexer against the stores and waits until
// what it found is read.
func histReconcile(t *testing.T, e *testEnv) *history.Index {
	t.Helper()
	return histWait(t, e, true)
}

func histWait(t *testing.T, e *testEnv, reconcile bool) *history.Index {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*testWait)
	defer cancel()
	wait := e.srv.history.settle
	if reconcile {
		wait = func(ctx context.Context, _ bool) error { return e.srv.history.reconcileNow(ctx) }
	}
	if err := wait(ctx, reconcile); err != nil {
		t.Fatalf("the history's indexer did not settle: %v (%v)", err, e.srv.history.indexing())
	}
	ix := e.srv.history.index()
	if ix == nil {
		t.Fatal("no index")
	}
	return ix
}

func histEntry(t *testing.T, ix *history.Index, key string) history.Hit {
	t.Helper()
	h, err := ix.Entry(t.Context(), key)
	if err != nil {
		t.Fatalf("the entry %s: %v", key, err)
	}
	return h
}

func histGone(t *testing.T, ix *history.Index, key string) {
	t.Helper()
	if _, err := ix.Entry(t.Context(), key); err != history.ErrGone {
		t.Fatalf("the entry %s: %v, not gone", key, err)
	}
}

// histOut is the refs a ref links to by rel.
func histOut(t *testing.T, ix *history.Index, ref, rel string) []string {
	t.Helper()
	ls, err := ix.Links(t.Context(), ref, history.MaxLinks)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range ls.Out {
		if l.Rel == rel {
			out = append(out, l.Ref)
		}
	}
	sort.Strings(out)
	return out
}

func histMark(t *testing.T, ix *history.Index, key string) string {
	t.Helper()
	m, ok, err := ix.Mark(t.Context(), key)
	if err != nil || !ok {
		t.Fatalf("the mark of %s: %v %v", key, ok, err)
	}
	return m
}

func commitID(n int) string { return fmt.Sprintf("%040x", n) }

var histNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// histCrew is a crew of a work task build and a review of it, in dir.
func histCrew(dir string) crew.Crew {
	return crew.Crew{ID: "hist", Name: "Hist", Cwd: dir, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "cat"}, {Name: "reviewer", AgentID: "sh"}},
		Tasks: []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build."},
			{ID: "review", Member: "reviewer", Kind: crew.TaskReview, Instructions: "Review.", Needs: []string{"build"}}}}
}

// histCreate makes a managed run's row as its first transaction does,
// continuing from when from is set.
func histCreate(t *testing.T, ld *ledger.Ledger, id, from string, c crew.Crew) {
	t.Helper()
	b, _ := json.Marshal(c)
	histTx(t, ld, id, func(tx *ledger.Tx) error {
		if err := tx.CreateRun(ledger.RunRow{ID: id, CrewID: c.ID, Crew: string(b), Base: commitID(0), State: ledger.RunRunning, ContinuedFrom: from, CreatedAt: histNow}); err != nil {
			return err
		}
		for _, task := range c.Tasks {
			if err := tx.PutTask(ledger.TaskRow{ID: task.ID, Member: task.Member, Kind: task.Kind, State: ledger.TaskReady}); err != nil {
				return err
			}
		}
		if from != "" {
			return tx.Continued(from)
		}
		return nil
	})
}

func histTx(t *testing.T, ld *ledger.Ledger, run string, fn func(tx *ledger.Tx) error) {
	t.Helper()
	if _, err := ld.Transition(context.Background(), run, fn); err != nil {
		t.Fatal(err)
	}
}

func histAttempt(task string, n int, state, stage, start, result string) ledger.AttemptRow {
	return ledger.AttemptRow{TaskID: task, N: n, Member: "lead", State: state, Stage: stage, StartCommit: start, ResultCommit: result, Branch: "conductor/" + task,
		ReservedAt: histNow.Add(time.Duration(n) * time.Minute), Where: ledger.WhereNative}
}

func histReceipt(task string, n int, id string, k int, subject string, exit int, log string) ledger.CheckRow {
	sum := sha256.Sum256([]byte(log))
	tail := log
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	return ledger.CheckRow{TaskID: task, N: n, ID: id, K: k, Phase: ledger.PhaseCheck, Argv: []string{"go", "test", "./..."}, Subject: subject, State: ledger.CheckFinished,
		StartedAt: histNow, FinishedAt: histNow.Add(time.Duration(n)*time.Minute + time.Duration(k)*time.Second), Exit: exit, Stable: true, Log: []byte(log), LogSHA256: hex.EncodeToString(sum[:]),
		Tail: tail, Where: ledger.WhereNative}
}

// histTask puts build's row as the engine keeps it: n attempts made.
func histTask(tx *ledger.Tx, state string, n int) error {
	return tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: state, Attempts: n})
}

// failedAt is attempt n of build failed at its check unit with the
// receipt k, whose log names what broke.
func failedAt(t *testing.T, ld *ledger.Ledger, run string, n, k int, words string) {
	t.Helper()
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", n, ledger.AttemptFailed, "check unit", commitID(n-1), commitID(n))); err != nil {
			return err
		}
		if err := histTask(tx, ledger.TaskFailed, n); err != nil {
			return err
		}
		return tx.PutCheck(histReceipt("build", n, "unit", k, commitID(n), 1, "--- FAIL: "+words+"\n"))
	})
}

// passedAt is attempt n of build passing its check unit.
func passedAt(t *testing.T, ld *ledger.Ledger, run string, n, k int) {
	t.Helper()
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", n, ledger.AttemptSucceeded, "", commitID(n-1), commitID(n))); err != nil {
			return err
		}
		if err := histTask(tx, ledger.TaskSucceeded, n); err != nil {
			return err
		}
		return tx.PutCheck(histReceipt("build", n, "unit", k, commitID(n), 0, "ok\n"))
	})
}

// The fan calls every observer of each hook in the order they were added,
// for every commit in the commit's order, the sync adapter first.
func TestTheFanCallsEveryObserverInCommitOrder(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ld, err := ledger.Open(filepath.Join(st.Dir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	var mu sync.Mutex
	var got []string
	note := func(who, what string) {
		mu.Lock()
		got = append(got, who+":"+what)
		mu.Unlock()
	}
	f := newCommitFan()
	for _, who := range []string{"sync", "history"} {
		f.add(commitObserver{
			data:     func(ev store.Event) { note(who, ev.Path) },
			commit:   func(p ledger.RunPointer) { note(who, fmt.Sprintf("%s@%d", p.ID, p.Revision)) },
			prune:    func(id string, _ int64) { note(who, "prune "+id) },
			dispatch: func(cs []dispatch.Change) { note(who, cs[0].Entity) },
		})
	}
	f.observeData(st)
	f.observeLedger(ld)
	for _, name := range []string{"a.json", "b.json"} {
		if err := st.Save(name, map[string]int{}); err != nil {
			t.Fatal(err)
		}
	}
	histCreate(t, ld, "hist-0000000a", "", histCrew(t.TempDir()))
	f.dispatchCommitted([]dispatch.Change{{Entity: dispatch.EntityProject, ID: "p"}})
	want := []string{"sync:a.json", "history:a.json", "sync:b.json", "history:b.json", "sync:hist-0000000a@1", "history:hist-0000000a@1", "sync:project", "history:project"}
	if !slices.Equal(got, want) {
		t.Fatalf("the calls:\n%q\nwant\n%q", got, want)
	}
}

// The index hands what the engine hands: a managed run whose check fails
// at attempt 1, its attempt 2 handed that receipt in its task.json
// (previous.check), and the index's handed link from the same receipt to
// attempt 2, its passed_after to attempt 2's passing receipt.
func TestTheIndexHandsWhatTheEngineHands(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	c := managedCrew(e.root)
	c.Members, c.Tasks = c.Members[:1], c.Tasks[:1]
	c.Tasks[0].Checks = []crew.Check{{ID: "fixed", Argv: []string{"test", "-f", "fixed"}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopEveryRun(e.srv) })
	submit := func(a map[string]any) {
		sid := a["sessionId"].(string)
		if resp, out := e.do("POST", "/api/sessions/"+sid+"/task/result", e.agentToken(sid), map[string]any{"summary": "built"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("result: %d %v", resp.StatusCode, out)
		}
	}
	a1 := runningAttempt(t, e, run.ID, "build", 1)
	commitFile(t, a1["worktree"].(string), "a.txt", "one\n")
	submit(a1)
	ledgerAttemptIn(t, e.srv, run.ID, "build", 1, ledger.AttemptFailed)
	a2 := runningAttempt(t, e, run.ID, "build", 2)
	b, err := os.ReadFile(filepath.Join(a2["worktree"].(string), ".conductor", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tf struct {
		Previous struct{ Check crew.TaskCheck } `json:"previous"`
	}
	if err := json.Unmarshal(b, &tf); err != nil || tf.Previous.Check.Receipt == "" {
		t.Fatalf("attempt 2's task.json hands no receipt (%v): %s", err, b)
	}
	handed := tf.Previous.Check
	commitFile(t, a2["worktree"].(string), "fixed", "yes\n")
	submit(a2)
	ledgerAttemptIn(t, e.srv, run.ID, "build", 2, ledger.AttemptSucceeded)
	rows, err := e.srv.ledger.Checks(t.Context(), run.ID, "build", 2, false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attempt 2's receipts: %v %v", rows, err)
	}
	ix := histSettle(t, e)
	from := hCheckRef(handed.Run, handed.Task, handed.Attempt, handed.ID, handed.K)
	if got := histOut(t, ix, from, relHanded); !slices.Equal(got, []string{attemptKey(run.ID, "build", 2)}) {
		t.Fatalf("%s handed to %v; the engine handed it to attempt 2 (%s)", from, got, handed.Receipt)
	}
	if got := histOut(t, ix, from, relPassedAfter); !slices.Equal(got, []string{hCheckRef(run.ID, "build", 2, "fixed", rows[0].K)}) {
		t.Fatalf("%s passed after %v", from, got)
	}
	failed := histEntry(t, ix, from)
	if failed.Class != history.ClassChecked || failed.Outcome != "failed" || failed.Commit != handed.Subject || failed.Where != ledger.WhereNative || failed.Title != "fixed on build #1" {
		t.Fatalf("the failed receipt's entry: %+v", failed.Entry)
	}
	if got := histOut(t, ix, attemptKey(run.ID, "build", 2), relSessionOf); !slices.Equal(got, []string{hSessionRef(a2["sessionId"].(string))}) {
		t.Fatalf("attempt 2's session: %v", got)
	}
}

// continuedRun is a run whose attempt 1 failed at its check unit, indexed,
// and a continuation of it whose attempt 2 runs: their ids and the failed
// receipt's ref.
func continuedRun(t *testing.T, e *testEnv) (src, cont, failed string) {
	t.Helper()
	ld := e.srv.ledger
	c := histCrew(e.root)
	src, cont = "hist-00000001", "hist-00000002"
	histCreate(t, ld, src, "", c)
	failedAt(t, ld, src, 1, 1, "TestLoginRedirect")
	ix := histSettle(t, e)
	failed = hCheckRef(src, "build", 1, "unit", 1)
	if got := histOut(t, ix, failed, relHanded); len(got) != 0 {
		t.Fatalf("handed before any attempt after it: %v", got)
	}
	histCreate(t, ld, cont, src, c)
	histTx(t, ld, cont, func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 2, ledger.AttemptRunning, "", commitID(0), ""))
	})
	return src, cont, failed
}

// A continuation's first attempt is handed the receipt that failed its
// source run's last attempt, as the engine hands it.
func TestHandedAcrossAContinuation(t *testing.T) {
	e := newTestEnv(t, nil)
	src, cont, failed := continuedRun(t, e)
	ix := histSettle(t, e)
	if got := histOut(t, ix, failed, relHanded); !slices.Equal(got, []string{attemptKey(cont, "build", 2)}) {
		t.Fatalf("handed across the continuation: %v", got)
	}
	if got := histOut(t, ix, hRunRef(cont), relContinues); !slices.Equal(got, []string{hRunRef(src)}) {
		t.Fatalf("continues: %v", got)
	}
}

// The source run indexed while its continuation still runs: the
// continuation's pass, once its attempt passes, writes passed_after from
// the source's failed receipt, and a restart's catch-up keeps it.
func TestPassedAfterAContinuationIndexedLater(t *testing.T) {
	e := newTestEnv(t, nil)
	_, cont, failed := continuedRun(t, e)
	histSettle(t, e)
	passedAt(t, e.srv.ledger, cont, 2, 1)
	ix := histSettle(t, e)
	pass := hCheckRef(cont, "build", 2, "unit", 1)
	if got := histOut(t, ix, failed, relPassedAfter); !slices.Equal(got, []string{pass}) {
		t.Fatalf("passed_after from the source's receipt: %v", got)
	}
	e2 := restartServer(t, e)
	if got := histOut(t, histSettle(t, e2), failed, relPassedAfter); !slices.Equal(got, []string{pass}) {
		t.Fatalf("passed_after after a restart: %v", got)
	}
}

// A review whose verdict is needs_changes reviews the attempt whose result
// it started from and reopens that task's next attempt; its verdict and
// findings are said, never decided or checked.
func TestAReviewsNeedsChangesLinksTheReopenedAttempt(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000003", "", histCrew(e.root))
	res, _ := json.Marshal(crew.Result{Verdict: crew.VerdictNeedsChanges, Summary: "Not yet.", Findings: []crew.Finding{{Severity: "major", Path: "a.go", Line: 3, Message: "a nil map"}}})
	histTx(t, ld, "hist-00000003", func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptSucceeded, "", commitID(0), commitID(1))); err != nil {
			return err
		}
		rv := histAttempt("review", 1, ledger.AttemptSucceeded, "", commitID(1), "")
		rv.Member, rv.Result = "reviewer", string(res)
		if err := tx.PutAttempt(rv); err != nil {
			return err
		}
		return tx.PutAttempt(histAttempt("build", 2, ledger.AttemptRunning, "", commitID(1), ""))
	})
	ix := histSettle(t, e)
	rv := attemptKey("hist-00000003", "review", 1)
	if got := histOut(t, ix, rv, relReviews); !slices.Equal(got, []string{attemptKey("hist-00000003", "build", 1)}) {
		t.Fatalf("reviews: %v", got)
	}
	if got := histOut(t, ix, rv, relReopened); !slices.Equal(got, []string{attemptKey("hist-00000003", "build", 2)}) {
		t.Fatalf("reopened: %v", got)
	}
	for key, kind := range map[string]string{rv + "#verdict": "review.verdict", rv + "#finding/1": "review.finding"} {
		if h := histEntry(t, ix, key); h.Kind != kind || h.Class != history.ClassSaid {
			t.Fatalf("%s: %+v", key, h.Entry)
		}
	}
}

// A failed check's body is its log's ends: of a 1 MiB log, its first
// 4,096 bytes and its last 12,283, 16,384 bytes in all; its summary the
// argv cut to 1 KiB, so a word of the argv is found and a word from the
// log's middle is not. A passed check indexes its ledger tail alone.
func TestACheckBodyIsTheLogsEnds(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000004", "", histCrew(e.root))
	line := strings.Repeat("x", 63) + "\n"
	log := strings.Repeat(line, (1<<20)/64)
	mid := len(log) / 2
	log = log[:mid] + "zmiddleword" + log[mid+11:]
	// The largest argv a crew's check takes: 32 entries, 8 KiB in all.
	argv := []string{"go", "test", "argvword"}
	for len(argv) < 32 {
		argv = append(argv, strings.Repeat("a", (8<<10-14)/29))
	}
	r := histReceipt("build", 1, "unit", 1, commitID(1), 1, log)
	r.Argv = argv
	histTx(t, ld, "hist-00000004", func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptFailed, "check unit", commitID(0), commitID(1))); err != nil {
			return err
		}
		return tx.PutCheck(r)
	})
	ix := histSettle(t, e)
	h := histEntry(t, ix, hCheckRef("hist-00000004", "build", 1, "unit", 1))
	if len(h.Body) != history.MaxCheckBody || h.Body[:4096] != log[:4096] || h.Body[4096:4101] != "\n…\n" || h.Body[4101:] != log[len(log)-12283:] {
		t.Fatalf("the body: %d bytes", len(h.Body))
	}
	if len(h.Summary) > history.MaxSummary || !strings.HasSuffix(h.Summary, "… (32 arguments)") {
		t.Fatalf("the summary: %d bytes, ends %q", len(h.Summary), h.Summary[max(0, len(h.Summary)-20):])
	}
	search := func(line string) int {
		q, err := history.ParseQuery(line)
		if err != nil {
			t.Fatal(err)
		}
		p, err := ix.Search(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		return len(p.Entries)
	}
	if search("argvword") != 1 || search("zmiddleword") != 0 {
		t.Fatalf("argvword found %d, zmiddleword %d", search("argvword"), search("zmiddleword"))
	}
}

func TestAPassedCheckIndexesItsTailOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000005", "", histCrew(e.root))
	r := histReceipt("build", 1, "unit", 1, commitID(1), 0, "headword\n"+strings.Repeat("y", 4000)+"\ntailword\n")
	histTx(t, ld, "hist-00000005", func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptSucceeded, "", commitID(0), commitID(1))); err != nil {
			return err
		}
		return tx.PutCheck(r)
	})
	h := histEntry(t, histSettle(t, e), hCheckRef("hist-00000005", "build", 1, "unit", 1))
	if h.Outcome != "passed" || h.Body != r.Tail || strings.Contains(h.Body, "headword") {
		t.Fatalf("a passed check's entry: %s, a body of %d bytes", h.Outcome, len(h.Body))
	}
}

// A receipt's outcome is the engine's predicate (crew.FailedOutcome): exit
// 0 with the checkout changed failed, a leftover child killed after an
// exit 0 passed, an error with exit 0 failed, interrupted kept so; a
// receipt started or running is not indexed until it finishes.
func TestOutcomeIsTheEnginesPredicate(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000006", "", histCrew(e.root))
	rows := map[string]func(r *ledger.CheckRow){
		"failed:unstable":       func(r *ledger.CheckRow) { r.Stable = false },
		"passed:killed":         func(r *ledger.CheckRow) { r.Killed = true },
		"failed:error":          func(r *ledger.CheckRow) { r.Error = "its outcome could not be recorded" },
		"interrupted:cut":       func(r *ledger.CheckRow) { r.State = ledger.CheckInterrupted },
		"none:running":          func(r *ledger.CheckRow) { r.State = ledger.CheckRunning },
		"none:started":          func(r *ledger.CheckRow) { r.State = ledger.CheckStarted },
		"passed:plain":          func(r *ledger.CheckRow) {},
		"failed:timed-out-exit": func(r *ledger.CheckRow) { r.TimedOut = true },
	}
	var names []string
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)
	histTx(t, ld, "hist-00000006", func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptVerifying, "", commitID(0), commitID(1))); err != nil {
			return err
		}
		for k, name := range names {
			r := histReceipt("build", 1, "c"+fmt.Sprint(k), k+1, commitID(1), 0, "ok\n")
			rows[name](&r)
			if err := tx.PutCheck(r); err != nil {
				return err
			}
		}
		return nil
	})
	ix := histSettle(t, e)
	for k, name := range names {
		want, _, _ := strings.Cut(name, ":")
		key := hCheckRef("hist-00000006", "build", 1, "c"+fmt.Sprint(k), k+1)
		if want == "none" {
			histGone(t, ix, key)
			continue
		}
		if h := histEntry(t, ix, key); h.Outcome != want {
			t.Errorf("%s: %s, want %s", name, h.Outcome, want)
		}
	}
	// The running one finishes: indexed then, once.
	histTx(t, ld, "hist-00000006", func(tx *ledger.Tx) error {
		r := histReceipt("build", 1, "c"+fmt.Sprint(slices.Index(names, "none:running")), slices.Index(names, "none:running")+1, commitID(1), 2, "broke\n")
		return tx.PutCheck(r)
	})
	ix = histSettle(t, e)
	k := slices.Index(names, "none:running")
	if h := histEntry(t, ix, hCheckRef("hist-00000006", "build", 1, "c"+fmt.Sprint(k), k+1)); h.Outcome != "failed" {
		t.Fatalf("the receipt once finished: %+v", h.Entry)
	}
}

// candidate.verified is the delivery's predicate (verifiedOf, exactly what
// candidateVerified answers) and no receipt the engine fails: over a
// table, the two agree; a receipt at exit 0, stable, that carries an Error
// passes the delivery, the engine fails it, its check entry is failed and
// no candidate.verified entry is indexed.
func TestVerifiedIsTheDeliverysPredicate(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	cases := []struct {
		name     string
		attempt  string
		receipt  func(r *ledger.CheckRow)
		none     bool
		delivery bool
		indexed  bool
	}{
		{"passed", ledger.AttemptSucceeded, func(*ledger.CheckRow) {}, false, true, true},
		{"failed attempt, checks passed", ledger.AttemptFailed, func(*ledger.CheckRow) {}, false, false, false},
		{"no receipt", ledger.AttemptSucceeded, nil, true, false, false},
		{"unstable tree", ledger.AttemptSucceeded, func(r *ledger.CheckRow) { r.Stable = false }, false, false, false},
		{"cleanup kill", ledger.AttemptSucceeded, func(r *ledger.CheckRow) { r.Killed = true }, false, true, true},
		{"an error at exit 0", ledger.AttemptSucceeded, func(r *ledger.CheckRow) { r.Error = "the outcome was not recorded" }, false, true, false},
	}
	for i, tc := range cases {
		run := fmt.Sprintf("hist-%08x", 0x100+i)
		histCreate(t, ld, run, "", histCrew(e.root))
		histTx(t, ld, run, func(tx *ledger.Tx) error {
			stage := ""
			if tc.attempt == ledger.AttemptFailed {
				stage = "review"
			}
			if err := tx.PutAttempt(histAttempt("build", 1, tc.attempt, stage, commitID(0), commitID(1))); err != nil {
				return err
			}
			if tc.none {
				return nil
			}
			r := histReceipt("build", 1, "unit", 1, commitID(1), 0, "ok\n")
			tc.receipt(&r)
			return tx.PutCheck(r)
		})
		v, code, words, err := e.srv.candidateVerified(t.Context(), dispatch.Candidate{Run: run, Task: "build", Attempt: 1, Commit: commitID(1)})
		if err != nil || code != "" {
			t.Fatalf("%s: the delivery's read: %v %s %s", tc.name, err, code, words)
		}
		attempts, _ := ld.Attempts(t.Context(), run)
		checks, _ := ld.Checks(t.Context(), run, "build", 1, false)
		cand := candidateOf(crew.TaskWork, ledger.TaskRow{}, attempts[0], checks, nil)
		if v.Passed != tc.delivery || verifiedOf(attempts[0], cand.Receipts) != v.Passed {
			t.Fatalf("%s: the delivery %v, verifiedOf %v, want %v", tc.name, v.Passed, verifiedOf(attempts[0], cand.Receipts), tc.delivery)
		}
		ix := histSettle(t, e)
		key := fmt.Sprintf("candidate:%s/build/1", run)
		if _, err := ix.Entry(t.Context(), key); (err == nil) != tc.indexed {
			t.Fatalf("%s: candidate.verified indexed %v, want %v", tc.name, err == nil, tc.indexed)
		}
		if tc.name == "an error at exit 0" {
			if h := histEntry(t, ix, hCheckRef(run, "build", 1, "unit", 1)); h.Outcome != "failed" {
				t.Fatalf("the receipt with an error: %s", h.Outcome)
			}
		}
	}
}

// Only the admin's command receipts are indexed: two actors with one
// command id in one run are one entry, the admin's, keyed by its actor.
func TestOnlyTheAdminsCommandsAreIndexed(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000007", "", histCrew(e.root))
	for _, actor := range []string{adminActor, "user:nate"} {
		histTx(t, ld, "hist-00000007", func(tx *ledger.Tx) error {
			if _, _, err := tx.Command(actor, "c-1", "pause", "fp"); err != nil {
				return err
			}
			return tx.Settle(actor, "c-1", ledger.CommandDone, map[string]any{"paused": true})
		})
	}
	ix := histSettle(t, e)
	h := histEntry(t, ix, "run:hist-00000007#command/admin/c-1")
	if h.Ref != "command:hist-00000007/c-1" || h.Actor != adminActor || h.Class != history.ClassDecided {
		t.Fatalf("the admin's command: %+v", h.Entry)
	}
	histGone(t, ix, "run:hist-00000007#command/user:nate/c-1")
}

// countingLedger is the ledger as the indexer reads it, counting the logs
// it reads, the runs' rows it reads (passes: Tasks is read by a run's pass
// alone), the attempts' reads, and every receipt read by the attempt it
// named; failAttempts fails that many Attempts reads.
type countingLedger struct {
	indexLedger
	logs, withLog, passes, attempts, failAttempts atomic.Int64
	mu                                            sync.Mutex
	checks                                        []attemptID
}

func (c *countingLedger) Tasks(ctx context.Context, runID string) ([]ledger.TaskRow, error) {
	c.passes.Add(1)
	return c.indexLedger.Tasks(ctx, runID)
}

func (c *countingLedger) Attempts(ctx context.Context, runID string) ([]ledger.AttemptRow, error) {
	c.attempts.Add(1)
	if c.failAttempts.Add(-1) >= 0 {
		return nil, errors.New("the ledger could not be read")
	}
	return c.indexLedger.Attempts(ctx, runID)
}

// checksRead is the attempts whose receipts were read, in order, from i.
func (c *countingLedger) checksRead(i int) []attemptID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.checks[min(i, len(c.checks)):])
}

func (c *countingLedger) Check(ctx context.Context, runID, taskID string, n int, id string, k int) (ledger.CheckRow, bool, error) {
	c.logs.Add(1)
	return c.indexLedger.Check(ctx, runID, taskID, n, id, k)
}

func (c *countingLedger) Checks(ctx context.Context, runID, taskID string, n int, withLog bool) ([]ledger.CheckRow, error) {
	if withLog {
		c.withLog.Add(1)
	}
	c.mu.Lock()
	c.checks = append(c.checks, attemptID{runID, taskID, n})
	c.mu.Unlock()
	return c.indexLedger.Checks(ctx, runID, taskID, n, withLog)
}

// freshIndexer replaces e's indexer, once it has settled (its index
// closed cleanly), with one over the same stores whose seams set gives,
// started after set, its hooks on the server's fan.
func freshIndexer(t *testing.T, e *testEnv, set func(h *historyIndexer)) *historyIndexer {
	t.Helper()
	histSettle(t, e)
	e.srv.stopHistory()
	old := e.srv.history
	h := newHistoryIndexer(e.srv.store, e.srv.sessionRecords, old.ledger, e.srv.dispatch, old.host, e.srv.log)
	h.brakeman = old.brakeman
	if set != nil {
		set(h)
	}
	e.srv.commits.add(h.observer())
	e.srv.history = h
	h.start()
	return h
}

// countLedger gives e a fresh indexer reading the ledger through a
// counter.
func countLedger(t *testing.T, e *testEnv, set func(h *historyIndexer)) *countingLedger {
	t.Helper()
	c := &countingLedger{indexLedger: e.srv.ledger}
	freshIndexer(t, e, func(h *historyIndexer) {
		h.ledger = func() indexLedger { return c }
		if set != nil {
			set(h)
		}
	})
	return c
}

// A finished receipt is read once: its log by ledger.Check for that one
// row, the run's later transitions reading no log at all.
func TestAFinishedReceiptIsReadOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	c := countLedger(t, e, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000008", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000008", 1, 1, "TestOnce")
	histSettle(t, e)
	if c.logs.Load() != 1 {
		t.Fatalf("logs read: %d", c.logs.Load())
	}
	for range 3 {
		histTx(t, ld, "hist-00000008", func(tx *ledger.Tx) error { return tx.Event("note", map[string]any{"x": 1}) })
		histSettle(t, e)
	}
	if c.logs.Load() != 1 || c.withLog.Load() != 0 {
		t.Fatalf("logs read: %d, checks read with their logs: %d", c.logs.Load(), c.withLog.Load())
	}
}

// indexClock is the indexer's clock and timer, moved by the test.
type indexClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []indexTimer
}

type indexTimer struct {
	at time.Time
	ch chan time.Time
}

func (c *indexClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *indexClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, indexTimer{c.now.Add(d), ch})
	return ch
}

// waiting says a timer for the time at is set.
func (c *indexClock) waiting(at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.timers, func(tm indexTimer) bool { return tm.at.Equal(at) })
}

func (c *indexClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, tm := range c.timers {
		if !tm.at.After(c.now) {
			tm.ch <- c.now
			continue
		}
		kept = append(kept, tm)
	}
	c.timers = kept
}

// A run is read at most once every 2 s however many transitions marked
// it, and the last change is read after the interval.
func TestARunIsReadAtMostEvery2s(t *testing.T) {
	e := newTestEnv(t, nil)
	clock := &indexClock{now: histNow}
	c := countLedger(t, e, func(h *historyIndexer) { h.now, h.after = clock.Now, clock.After })
	h := e.srv.history
	histSettle(t, e)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000009", "", histCrew(e.root))
	until(t, "the run read", func() bool { return c.passes.Load() == 1 })
	for n := 1; n <= 3; n++ {
		histTx(t, ld, "hist-00000009", func(tx *ledger.Tx) error { return tx.Event("note", map[string]any{"n": n}) })
	}
	// Marked three times, deferred to the interval's end: nothing read
	// until the clock passes it.
	until(t, "the run deferred", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.queue) == 0
	})
	// The indexer waits for the interval's end on its timer: the clock is
	// moved only once that timer is set.
	until(t, "the deferred read's timer", func() bool { return clock.waiting(histNow.Add(minRunInterval)) })
	clock.Advance(minRunInterval - time.Millisecond)
	if c.passes.Load() != 1 {
		t.Fatalf("the run read %d times within the interval", c.passes.Load())
	}
	clock.Advance(time.Millisecond)
	until(t, "the last change read", func() bool { return c.passes.Load() == 2 })
	ix := e.srv.history.index()
	until(t, "the mark", func() bool {
		p, _, _ := ld.Pointer(t.Context(), "hist-00000009")
		m, _, _ := ix.Mark(t.Context(), "run:hist-00000009")
		return m == fmt.Sprintf("L%d", p.Revision)
	})
	if c.passes.Load() != 2 {
		t.Fatalf("the run read %d times", c.passes.Load())
	}
}

// A member's agent session saved to the run's record with no transition
// (the engine's crew.OnChange) changes the run's mark (its record's
// hash), so a restart's catch-up reads it.
func TestARecordOnlyChangeIsSeenAfterARestart(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-0000000b", "", histCrew(e.root))
	histTx(t, ld, "hist-0000000b", func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 1, ledger.AttemptRunning, "", commitID(0), ""))
	})
	rec := crew.Run{ID: "hist-0000000b", CrewID: "hist", Name: "Hist", Cwd: e.root, Isolation: crew.IsolationWorktree, State: crew.RunRunning, StartedAt: histNow,
		Members: []crew.MemberState{{Name: "lead", AgentID: "cat", Status: "running", Task: "build", Attempt: 1, AgentSession: &session.AgentSession{ID: "first-id"}}}}
	saveRecord(t, e.srv, rec)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, attemptKey("hist-0000000b", "build", 1)); h.AgentSession != "first-id" {
		t.Fatalf("the attempt's agent session: %q", h.AgentSession)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	rec.Members[0].AgentSession = &session.AgentSession{ID: "second-id"}
	st, err := store.Open(e.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := crew.NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Save(rec); err != nil {
		t.Fatal(err)
	}
	rs.Close()
	e2 := e.restart()
	if h := histEntry(t, histSettle(t, e2), attemptKey("hist-0000000b", "build", 1)); h.AgentSession != "second-id" {
		t.Fatalf("the attempt's agent session after the restart: %q", h.AgentSession)
	}
}

// noTick gives e an indexer whose reconcile runs only when the test asks
// (reconcileNow), never on its period.
func noTick(t *testing.T, e *testEnv) {
	t.Helper()
	freshIndexer(t, e, func(h *historyIndexer) {
		h.after = func(d time.Duration) <-chan time.Time {
			if d == h.reconcileTick {
				return nil
			}
			return time.After(d)
		}
	})
}

// otherServer is a second server on e's data directory, through a store
// of its own: its writes reach none of e's hooks.
func otherServer(t *testing.T, e *testEnv) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *config.Config) {
		c.DataDir = e.srv.cfg.DataDir
		c.AllowedRoots = []string{e.root}
		c.DefaultCwd = e.root
	})
}

// A run record the other server writes reaches no hook of this one's; one
// reconcile indexes it, read back through the index's own Entry, with no
// restart.
func TestARunRecordByTheOtherServerIsIndexed(t *testing.T) {
	e := newTestEnv(t, nil)
	noTick(t, e)
	other := otherServer(t, e)
	saveRecord(t, other.srv, crew.Run{ID: "hist-0000000c", Label: "the other's run", Cwd: e.root, StartedAt: histNow})
	ix := histSettle(t, e)
	histGone(t, ix, "run:hist-0000000c")
	ix = histReconcile(t, e)
	if h := histEntry(t, ix, "run:hist-0000000c"); h.Title != "the other's run" {
		t.Fatalf("the other server's run: %+v", h.Entry)
	}
}

// A change between a source's mark and its read leaves the source dirty:
// read again, and the mark the index keeps is the one after the change.
func TestAChangeBetweenTheMarkAndTheReadIsReadAgain(t *testing.T) {
	e := newTestEnv(t, nil)
	rec := crew.Run{ID: "hist-0000000d", Label: "before", Cwd: e.root, StartedAt: histNow}
	var once sync.Once
	var changed atomic.Bool
	freshIndexer(t, e, func(h *historyIndexer) {
		h.afterMark = func(key string) {
			if key == "run:hist-0000000d" {
				once.Do(func() {
					r := rec
					r.Label = "after"
					saveRecord(t, e.srv, r)
					changed.Store(true)
				})
			}
		}
	})
	saveRecord(t, e.srv, rec)
	ix := histSettle(t, e)
	if !changed.Load() {
		t.Fatal("the record never changed between its mark and its read")
	}
	b, err := os.ReadFile(filepath.Join(e.srv.cfg.DataDir, "runs", "hist-0000000d.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if h := histEntry(t, ix, "run:hist-0000000d"); h.Title != "after" || histMark(t, ix, "run:hist-0000000d") != "R"+hex.EncodeToString(sum[:]) {
		t.Fatalf("the run as indexed: %q, mark %s", h.Title, histMark(t, ix, "run:hist-0000000d"))
	}
}

// Runs made while no index existed are indexed by the next start's
// catch-up, and a run whose record went while the server was down is
// removed by the one after.
func TestCatchUpAfterARestart(t *testing.T) {
	e := newTestEnv(t, nil)
	histSettle(t, e)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(e.srv.cfg.DataDir, journal.Dir, history.File+suffix)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	rs, err := crew.NewRecords(e.srv.store)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hist-0000000e", "hist-0000000f"} {
		if err := rs.Save(crew.Run{ID: id, CrewID: "legacy", Name: "Legacy", Label: id, Cwd: e.root, State: crew.RunStopped, StartedAt: histNow}); err != nil {
			t.Fatal(err)
		}
	}
	rs.Close()
	// A record this version cannot read: listed as unreadable by the
	// journal, never indexed, and never a source retried for good.
	if err := os.WriteFile(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, "00000000000000ff.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	ix := histSettle(t, e2)
	histGone(t, ix, hSessionRef("00000000000000ff"))
	histEntry(t, ix, "run:hist-0000000e")
	histEntry(t, ix, "run:hist-0000000f")
	e2.srv.Shutdown(ctx)
	if err := os.Remove(filepath.Join(e.srv.cfg.DataDir, "runs", "hist-0000000f.json")); err != nil {
		t.Fatal(err)
	}
	e3 := e2.restart()
	ix = histSettle(t, e3)
	histEntry(t, ix, "run:hist-0000000e")
	histGone(t, ix, "run:hist-0000000f")
}

// The catch-up needs the history lock alone: another holds the ledger's,
// and the session records, the runs from their files and the ledger's rows
// through its read-only connection are all indexed at start.
func TestCatchUpRunsOnTheHistoryLockAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	ticket := seedTicket(t, dir, "LCK", "held at start")
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	histCreate(t, ld, "hist-00000010", "", histCrew(dir))
	failedAt(t, ld, "hist-00000010", 1, 1, "TestHeld")
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server holds the ledger's lock")
	}
	id := e.createSession("cat")
	e.stopAndWait(id)
	saveRecord(t, e.srv, crew.Run{ID: "hist-00000011", Label: "legacy", Cwd: e.root, StartedAt: histNow})
	ix := histSettle(t, e)
	histEntry(t, ix, hSessionRef(id))
	histEntry(t, ix, "run:hist-00000011")
	if h := histEntry(t, ix, hCheckRef("hist-00000010", "build", 1, "unit", 1)); h.Outcome != "failed" {
		t.Fatalf("the ledger's receipt: %+v", h.Entry)
	}
	histTicketAtStart(t, ix, ticket, "held at start")
}

// With no ledger (its database cannot be opened), the session records and
// the run records are indexed, and indexing says the ledger is
// unavailable.
func TestCatchUpWithNoLedger(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	ticket := seedTicket(t, dir, "NOL", "no ledger")
	// A database file that is a link: the ledger refuses to open.
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "runs", ledger.File)); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	if e.srv.ledger != nil {
		t.Fatal("the ledger opened")
	}
	id := e.createSession("cat")
	e.stopAndWait(id)
	saveRecord(t, e.srv, crew.Run{ID: "hist-00000012", Label: "legacy", Cwd: e.root, StartedAt: histNow})
	ix := histSettle(t, e)
	histEntry(t, ix, hSessionRef(id))
	histEntry(t, ix, "run:hist-00000012")
	if got := e.srv.history.indexing(); got["ledger"] != "unavailable" {
		t.Fatalf("indexing: %v", got)
	}
	histTicketAtStart(t, ix, ticket, "no ledger")
}

// git runs git in dir for a test, failing it on an error.
func histGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func histProject(t *testing.T, e *testEnv, key, cwd string) {
	t.Helper()
	if _, err := e.srv.dispatch.CreateProject(t.Context(), dispatch.Op{Actor: adminActor, ID: opID()}, dispatch.NewProject{Key: key, Name: key, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
}

// A plain session in a linked worktree is the project's whose folder is
// the main checkout, by the repository's common directory, and stays so
// once its worktree is removed; a crew's run in a sibling worktree is
// found by Similar's repository scope.
func TestSiblingWorktreesAreOneRepository(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	sib := filepath.Join(e.root, "sib")
	histGit(t, e.root, "worktree", "add", "-q", "-b", "sib", sib)
	other := filepath.Join(e.root, "other")
	histGit(t, e.root, "worktree", "add", "-q", "-b", "other", other)
	histProject(t, e, "HIS", e.root)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": sib})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	e.stopAndWait(id)
	ix := histSettle(t, e)
	h := histEntry(t, ix, hSessionRef(id))
	common := filepath.Join(e.root, ".git")
	if h.Project != "HIS" || h.Repo != common || h.Tree != sib {
		t.Fatalf("the session in the sibling worktree: project %q, repo %q, tree %q", h.Project, h.Repo, h.Tree)
	}
	// Two runs, each in a worktree of the repository, one command.
	ld := e.srv.ledger
	for i, cwd := range []string{other, e.root} {
		run := fmt.Sprintf("hist-%08x", 0x20+i)
		histCreate(t, ld, run, "", histCrew(cwd))
		saveRecord(t, e.srv, crew.Run{ID: run, CrewID: "hist", Name: "Hist", Cwd: cwd, StartedAt: histNow})
		if i == 0 {
			failedAt(t, ld, run, 1, 1, "TestShared")
		} else {
			passedAt(t, ld, run, 1, 1)
		}
	}
	ix = histSettle(t, e)
	similar, err := ix.Similar(t.Context(), hCheckRef("hist-00000020", "build", 1, "unit", 1), history.ScopeRepo, []string{"hist-00000020"}, 10)
	if err != nil || len(similar) != 1 || similar[0].Key != hCheckRef("hist-00000021", "build", 1, "unit", 1) {
		t.Fatalf("similar in the repository: %v %v", similar, err)
	}
	// The session's worktree removed, the session read again (a project
	// made elsewhere reads every session): still the project's, by the
	// common directory its record keeps.
	histGit(t, e.root, "worktree", "remove", "--force", sib)
	histProject(t, e, "OTH", t.TempDir())
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hSessionRef(id)); h.Project != "HIS" || h.Repo != common {
		t.Fatalf("the session once its worktree is gone: project %q, repo %q", h.Project, h.Repo)
	}
}

// A project made over a record's repository, then moved to another
// repository, with no journal write: the session's entry follows each
// change by Dispatch's hook, and again by a restart's catch-up.
func TestAProjectChangeReassignsItsSessions(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	id := e.createSession("cat")
	e.stopAndWait(id)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hSessionRef(id)); h.Project != "" {
		t.Fatalf("a project before any: %q", h.Project)
	}
	histProject(t, e, "HIS", e.root)
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hSessionRef(id)); h.Project != "HIS" {
		t.Fatalf("the project made over its repository: %q", h.Project)
	}
	elsewhere := t.TempDir()
	histGit(t, elsewhere, "init", "-q")
	cwd := elsewhere
	if _, err := e.srv.dispatch.EditProject(t.Context(), dispatch.Op{Actor: adminActor, ID: opID()}, "HIS", dispatch.ProjectEdit{Cwd: &cwd}); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	if h := histEntry(t, ix, hSessionRef(id)); h.Project != "" {
		t.Fatalf("the project moved away: %q", h.Project)
	}
	e2 := restartServer(t, e)
	if h := histEntry(t, histSettle(t, e2), hSessionRef(id)); h.Project != "" {
		t.Fatalf("after the restart: %q", h.Project)
	}
}

// A member's record indexed before the ledger holds its attempt's start
// commit: the attempt's row written later, with no journal write, reaches
// the session's entry (its mark carries the run's revision).
func TestAnAttemptsStartCommitReachesItsSessionEntry(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000013", "", histCrew(e.root))
	id := session.NewID()
	r := journal.Record{ID: id, Name: "lead", Kind: string(session.KindServer), Agent: journal.Agent{ID: "cat"}, Where: "native", Cwd: e.root, StartedAt: histNow,
		StartFrom: journal.StartFromAttempt, Crew: &journal.Crew{RunID: "hist-00000013", CrewID: "hist", Member: "lead", Task: "build", Attempt: 1}}
	if _, err := e.srv.sessionRecords.Begin(r); err != nil {
		t.Fatal(err)
	}
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hSessionRef(id)); h.Commit != "" {
		t.Fatalf("a commit before the attempt's: %q", h.Commit)
	}
	histTx(t, ld, "hist-00000013", func(tx *ledger.Tx) error {
		a := histAttempt("build", 1, ledger.AttemptRunning, "", commitID(7), "")
		a.SessionID = id
		return tx.PutAttempt(a)
	})
	if h := histEntry(t, histSettle(t, e), hSessionRef(id)); h.Commit != commitID(7) || h.Run != "hist-00000013" {
		t.Fatalf("the member's entry: commit %q, run %q", h.Commit, h.Run)
	}
}

// A run pruned leaves the index with its receipts.
func TestAPrunedRunLeavesTheIndex(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000014", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000014", 1, 1, "TestPruned")
	histTx(t, ld, "hist-00000014", func(tx *ledger.Tx) error {
		row, _, err := tx.Run()
		if err != nil {
			return err
		}
		row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeFailed, histNow
		return tx.PutRun(row)
	})
	ix := histSettle(t, e)
	receipt := hCheckRef("hist-00000014", "build", 1, "unit", 1)
	histEntry(t, ix, receipt)
	if err := ld.Prune(t.Context(), "hist-00000014"); err != nil {
		t.Fatal(err)
	}
	ix = histSettle(t, e)
	histGone(t, ix, "run:hist-00000014")
	histGone(t, ix, receipt)
}

// Past its bound the dirty set is emptied and a full catch-up asked
// instead: a source marked before the overflow is indexed all the same.
func TestADirtySetPastItsBoundCatchesUp(t *testing.T) {
	e := newTestEnv(t, nil)
	var overflow bool
	var queued int
	freshIndexer(t, e, func(h *historyIndexer) {
		// Before it starts: the record saved now reaches no hook of it.
		saveRecord(t, e.srv, crew.Run{ID: "hist-00000015", Label: "before the overflow", Cwd: e.root, StartedAt: histNow})
		h.mark("run:hist-00000015")
		for i := range maxHistoryDirty {
			h.mark(fmt.Sprintf("session:%016x", i))
		}
		overflow, queued = h.overflow, len(h.queue)
	})
	if !overflow || queued != 0 {
		t.Fatalf("past the bound: overflow %v, %d queued", overflow, queued)
	}
	if got := histEntry(t, histSettle(t, e), "run:hist-00000015"); got.Title != "before the overflow" {
		t.Fatalf("the run: %+v", got.Entry)
	}
}

// The records ride in an export: restored on a fresh directory, listed,
// and indexed at the next start.
func TestTheRecordsRideInAnExport(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	e.stopAndWait(id)
	endedRecord(t, e, id)
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d", resp.StatusCode)
	}
	dir := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := export.Restore(t.Context(), bytes.NewReader(b), int64(len(b)), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, journal.Dir, history.File)); err == nil {
		t.Fatal("the index rode in the export")
	}
	e2 := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	resp, list := e2.do("GET", "/api/history/sessions", adminToken, nil)
	if recs, _ := list["records"].([]any); resp.StatusCode != http.StatusOK || len(recs) != 1 || recs[0].(map[string]any)["id"] != id {
		t.Fatalf("the restored list: %d %v", resp.StatusCode, list)
	}
	histEntry(t, histSettle(t, e2), hSessionRef(id))
}

// A server without history/owner.lock writes nothing to the index: it
// opens it to read, and what it does is indexed by the lock's holder only
// when that one reconciles.
func TestTheReadOnlyServerIndexesNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	noTick(t, e)
	ix := histSettle(t, e)
	other := otherServer(t, e)
	if ro, _ := other.srv.sessionRecords.ReadOnly(); !ro {
		t.Fatal("the other server owns the session records")
	}
	until(t, "the other's index opened to read", func() bool {
		oix := other.srv.history.index()
		return oix != nil && oix.ReadOnly()
	})
	if got := other.srv.history.indexing()["state"]; got != indexReadOnly {
		t.Fatalf("the other's indexing: %v", got)
	}
	before, err := ix.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saveRecord(t, other.srv, crew.Run{ID: "hist-00000016", Label: "other", Cwd: e.root, StartedAt: histNow})
	ix = histSettle(t, e)
	if after, _ := ix.Stats(t.Context()); after.Sources != before.Sources {
		t.Fatalf("the index changed by the other server: %d sources, %d before", after.Sources, before.Sources)
	}
	histEntry(t, histReconcile(t, e), "run:hist-00000016")
}

// An index its last owner did not close (here: an owner that never
// closed it) is opened at once and checked on a goroutine of its own,
// off the server's start path; once checked it answers as before.
func TestAnIndexNotClosedIsCheckedBesideTheIndexing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	crashed, err := history.Open(t.Context(), filepath.Join(dir, journal.Dir, history.File), history.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crashed.Close() })
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	id := e.createSession("cat")
	e.stopAndWait(id)
	ix := histSettle(t, e)
	until(t, "the index checked", func() bool {
		u, err := ix.Unchecked(t.Context())
		return err == nil && !u && e.srv.history.indexing()["state"] == indexReady
	})
	histEntry(t, ix, hSessionRef(id))
}

// POST /api/history/reindex clears the index in place and catches up from
// nothing: 202 with the indexing state; the admin's only.
func TestHistoryReindex(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	e.stopAndWait(id)
	ix := histSettle(t, e)
	before, _ := ix.Stats(t.Context())
	if resp, _ := e.do("POST", "/api/history/reindex", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	resp, out := e.do("POST", "/api/history/reindex", adminToken, nil)
	if resp.StatusCode != http.StatusAccepted || out["indexing"] == nil {
		t.Fatalf("reindex: %d %v", resp.StatusCode, out)
	}
	ix = histSettle(t, e)
	after, _ := ix.Stats(t.Context())
	if after.Generation == before.Generation {
		t.Fatalf("the index was not cleared: generation %d", after.Generation)
	}
	histEntry(t, ix, hSessionRef(id))
}

// BenchmarkTransitionWhileIndexing measures a ledger transition's latency
// at the median and the 99th percentile with the indexer idle, then while
// it indexes a run of 128 tasks whose failed checks have 1 MiB logs (the
// run's bound drops the logs past 64 MiB, as the ledger does).
//
//	go test -run '^$' -bench TransitionWhileIndexing -benchtime 1x ./internal/api
func BenchmarkTransitionWhileIndexing(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		b.Fatal(err)
	}
	defer ld.Close()
	j, err := journal.Open(st, journal.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatal(err)
	}
	defer j.Close()
	tx := func(run string, fn func(*ledger.Tx) error) {
		if _, err := ld.Transition(ctx, run, fn); err != nil {
			b.Fatal(err)
		}
	}
	c := histCrew(dir)
	c.Tasks = nil
	for i := range 128 {
		c.Tasks = append(c.Tasks, crew.Task{ID: fmt.Sprintf("t%03d", i), Member: "lead", Kind: crew.TaskWork})
	}
	cj, _ := json.Marshal(c)
	tx("big-00000001", func(t *ledger.Tx) error {
		return t.CreateRun(ledger.RunRow{ID: "big-00000001", CrewID: "hist", Crew: string(cj), Base: commitID(0), State: ledger.RunRunning, CreatedAt: histNow})
	})
	log := strings.Repeat(strings.Repeat("z", 1023)+"\n", 1024)
	for _, task := range c.Tasks {
		tx("big-00000001", func(t *ledger.Tx) error {
			if err := t.PutTask(ledger.TaskRow{ID: task.ID, Member: "lead", Kind: crew.TaskWork, State: ledger.TaskFailed, Attempts: 1}); err != nil {
				return err
			}
			if err := t.PutAttempt(histAttempt(task.ID, 1, ledger.AttemptFailed, "check unit", commitID(0), commitID(1))); err != nil {
				return err
			}
			return t.PutCheck(histReceipt(task.ID, 1, "unit", 1, commitID(1), 1, log))
		})
	}
	tx("probe-00000001", func(t *ledger.Tx) error {
		return t.CreateRun(ledger.RunRow{ID: "probe-00000001", CrewID: "probe", Crew: "{}", Base: commitID(0), State: ledger.RunRunning, CreatedAt: histNow})
	})
	h := newHistoryIndexer(st, j, func() indexLedger { return ld }, nil, "bench", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ix, err := history.Open(ctx, h.path, history.Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer ix.Close()
	measure := func(n int) []time.Duration {
		out := make([]time.Duration, 0, n)
		for i := range n {
			t0 := time.Now()
			tx("probe-00000001", func(t *ledger.Tx) error { return t.Event("probe", map[string]any{"i": i}) })
			out = append(out, time.Since(t0))
			time.Sleep(2 * time.Millisecond)
		}
		slices.Sort(out)
		return out
	}
	b.ResetTimer()
	idle := measure(300)
	var stop atomic.Bool
	var passes atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			if err := h.removeRun(ctx, ix, "big-00000001"); err != nil {
				b.Error(err)
				return
			}
			// Each pass the first of the run: nothing remembered.
			clear(h.readAt)
			clear(h.endedChecks)
			if err := h.indexRun(ctx, ix, "big-00000001"); err != nil {
				b.Error(err)
				return
			}
			passes.Add(1)
		}
	}()
	busy := measure(300)
	stop.Store(true)
	<-done
	b.StopTimer()
	q := func(d []time.Duration, p float64) float64 {
		return float64(d[min(len(d)-1, int(p*float64(len(d))))].Microseconds()) / 1000
	}
	b.ReportMetric(q(idle, 0.5), "idle-p50-ms")
	b.ReportMetric(q(idle, 0.99), "idle-p99-ms")
	b.ReportMetric(q(busy, 0.5), "indexing-p50-ms")
	b.ReportMetric(q(busy, 0.99), "indexing-p99-ms")
	b.ReportMetric(float64(passes.Load()), "passes")
}

// histFind is the keys a search for line answers.
func histFind(t *testing.T, ix *history.Index, line string) []string {
	t.Helper()
	q, err := history.ParseQuery(line)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ix.Search(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range page.Entries {
		out = append(out, h.Key)
	}
	return out
}

// forgettable is an ended server session's record, named by word and
// holding a chat line of it, indexed and found by a search for it.
func forgettable(t *testing.T, e *testEnv, word string) (string, journal.Instance) {
	t.Helper()
	id := session.NewID()
	r := journal.Record{ID: id, Name: word + " session", Kind: string(session.KindServer), Agent: journal.Agent{ID: "cat"}, Where: "native", Cwd: e.root, StartedAt: histNow}
	in, err := e.srv.sessionRecords.Begin(r)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.sessionRecords.Chat(in, journal.ChatLine{Kind: "message", Text: "the " + word + " line", ByName: "nate", At: histNow})
	e.srv.sessionRecords.Settle(in, journal.State{Name: r.Name, Crew: r.Crew, End: &journal.Ending{How: "exited"}, Final: true})
	endedRecord(t, e, id)
	if got := histFind(t, histSettle(t, e), word); len(got) < 2 {
		t.Fatalf("the record's words before it is forgotten: %v", got)
	}
	return id, in
}

// forgotten says the record id and its words are nowhere a search reaches.
func forgotten(t *testing.T, ix *history.Index, id, word string) {
	t.Helper()
	histGone(t, ix, hSessionRef(id))
	if got := histFind(t, ix, word); len(got) != 0 {
		t.Fatalf("the forgotten record's words are found: %v", got)
	}
}

// A pass that runs inside a Forget, as its file is removed (an observer
// after the indexer's, on the file's removal, waits for the indexer to
// settle), finds the record gone (the journal hides a record it removes
// from Get before its file goes) and removes it: never left until a
// restart.
func TestAForgetIsGoneFromTheIndexAtOnce(t *testing.T) {
	e := newTestEnv(t, nil)
	id, _ := forgettable(t, e, "zforgetinside")
	path := journal.Dir + "/" + journal.SessionsDir + "/" + id + ".json"
	var inside atomic.Bool
	e.srv.commits.add(commitObserver{data: func(ev store.Event) {
		if ev.Op == store.OpDeleted && ev.Path == path {
			ctx, cancel := context.WithTimeout(context.Background(), 2*testWait)
			defer cancel()
			if err := e.srv.history.settle(ctx, false); err != nil {
				t.Errorf("the indexer did not settle inside the forget: %v", err)
			}
			inside.Store(true)
		}
	}})
	if err := e.srv.sessionRecords.Forget(id); err != nil {
		t.Fatal(err)
	}
	if !inside.Load() {
		t.Fatal("the forget's removal was never seen")
	}
	forgotten(t, histSettle(t, e), id, "zforgetinside")
}

// A Forget made while a pass holds the record read (between its mark and
// its Replace, the afterMark seam) is not undone: the pass's write is
// followed by the pass the forget's commit asked, which removes it; the
// same during a reindex's catch-up, and for ForgetAll.
func TestAForgetDuringAPassIsNotWrittenBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reindex bool
		all     bool
	}{{"a pass", false, false}, {"a reindex", true, false}, {"ForgetAll", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			id, in := forgettable(t, e, "zforgetduring")
			var held atomic.Bool
			h := freshIndexer(t, e, func(h *historyIndexer) {
				h.afterMark = func(key string) {
					if key != hSessionRef(id) || held.Swap(true) {
						return
					}
					if tc.all {
						if _, _, err := e.srv.sessionRecords.ForgetAll(); err != nil {
							t.Error(err)
						}
					} else if err := e.srv.sessionRecords.Forget(id); err != nil {
						t.Error(err)
					}
				}
			})
			// The fresh indexer's catch-up finds the mark as it was: the
			// record is read again only once it changes, or once the index
			// is cleared.
			histSettle(t, e)
			if !held.Load() {
				if !tc.reindex {
					// A write of the record (its Seq) makes the pass read it.
					e.srv.sessionRecords.Chat(in, journal.ChatLine{Kind: "message", Text: "one more", ByName: "nate", At: histNow})
					e.srv.sessionRecords.Flush()
				} else {
					h.askReindex()
				}
			}
			ix := histSettle(t, e)
			if !held.Load() {
				t.Fatal("no pass read the record while it was forgotten")
			}
			forgotten(t, ix, id, "zforgetduring")
		})
	}
}

// A record whose file went while no server ran (a forget whose index
// write never happened) is removed by the next start's catch-up.
func TestARecordForgottenWhileStoppedLeavesTheIndex(t *testing.T) {
	e := newTestEnv(t, nil)
	id, _ := forgettable(t, e, "zforgetstopped")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	if err := os.Remove(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")); err != nil {
		t.Fatal(err)
	}
	e2 := e.restart()
	forgotten(t, histSettle(t, e2), id, "zforgetstopped")
}

// A run record deleted (a legacy run's, with no ledger rows) leaves the
// index with its words.
func TestADeletedRunRecordLeavesTheIndex(t *testing.T) {
	e := newTestEnv(t, nil)
	saveRecord(t, e.srv, crew.Run{ID: "hist-00000030", Label: "zrunrecordword", Cwd: e.root, StartedAt: histNow})
	if got := histFind(t, histSettle(t, e), "zrunrecordword"); len(got) != 1 {
		t.Fatalf("the run's words: %v", got)
	}
	if err := e.srv.records.Delete("hist-00000030"); err != nil {
		t.Fatal(err)
	}
	ix := histSettle(t, e)
	histGone(t, ix, "run:hist-00000030")
	if got := histFind(t, ix, "zrunrecordword"); len(got) != 0 {
		t.Fatalf("the deleted run's words: %v", got)
	}
}

// A run that the ledger's owner (another process: no hook of this one's
// sees it) prunes leaves the index at the next reconcile, its receipts
// and their words with it.
func TestALedgerRunPrunedByAnotherOwnerLeavesTheIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer ld.Close()
	histCreate(t, ld, "hist-00000031", "", histCrew(dir))
	failedAt(t, ld, "hist-00000031", 1, 1, "TestPrunedElsewhere")
	histTx(t, ld, "hist-00000031", func(tx *ledger.Tx) error {
		row, _, err := tx.Run()
		if err != nil {
			return err
		}
		row.State, row.Outcome, row.ClosedAt = ledger.RunFinished, ledger.OutcomeFailed, histNow
		return tx.PutRun(row)
	})
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = dir })
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server holds the ledger's lock")
	}
	noTick(t, e)
	ix := histSettle(t, e)
	receipt := hCheckRef("hist-00000031", "build", 1, "unit", 1)
	histEntry(t, ix, receipt)
	if got := histFind(t, ix, "TestPrunedElsewhere"); len(got) != 1 {
		t.Fatalf("the receipt's words: %v", got)
	}
	if err := ld.Prune(t.Context(), "hist-00000031"); err != nil {
		t.Fatal(err)
	}
	ix = histReconcile(t, e)
	histGone(t, ix, "run:hist-00000031")
	histGone(t, ix, receipt)
	if got := histFind(t, ix, "TestPrunedElsewhere"); len(got) != 0 {
		t.Fatalf("the pruned run's words: %v", got)
	}
}

// A run's receipts are read an attempt at a time, each read naming its
// task and attempt; a settled attempt's (ended by its own outcome, every
// receipt finished) are not read again on a later pass, a running one's
// are.
func TestARunsReceiptsAreReadAnAttemptAtATime(t *testing.T) {
	e := newTestEnv(t, nil)
	c := countLedger(t, e, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000032", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000032", 1, 1, "TestSettled")
	histTx(t, ld, "hist-00000032", func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 2, ledger.AttemptRunning, "", commitID(1), ""))
	})
	histSettle(t, e)
	read := c.checksRead(0)
	if len(read) == 0 {
		t.Fatal("no receipt read")
	}
	for _, a := range read {
		if a.task == "" || a.n == 0 {
			t.Fatalf("a read of a run's receipts names no attempt: %+v", a)
		}
	}
	from := len(c.checksRead(0))
	histTx(t, ld, "hist-00000032", func(tx *ledger.Tx) error { return tx.Event("note", map[string]any{"x": 1}) })
	histSettle(t, e)
	later := c.checksRead(from)
	if slices.Contains(later, attemptID{"hist-00000032", "build", 1}) {
		t.Fatalf("the settled attempt's receipts read again: %v", later)
	}
	if !slices.Contains(later, attemptID{"hist-00000032", "build", 2}) {
		t.Fatalf("the running attempt's receipts not read again: %v", later)
	}
}

// A receipt put after its attempt's end (a cancel commits the end first
// and ends its check after) is indexed: a cancelled attempt's rows are
// never taken as settled.
func TestAReceiptPutAfterItsAttemptEndedIsIndexed(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000033", "", histCrew(e.root))
	histTx(t, ld, "hist-00000033", func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 1, ledger.AttemptCancelled, "", commitID(0), commitID(1)))
	})
	histSettle(t, e)
	histTx(t, ld, "hist-00000033", func(tx *ledger.Tx) error {
		r := histReceipt("build", 1, "unit", 1, commitID(1), -1, "cut short\n")
		r.State = ledger.CheckInterrupted
		return tx.PutCheck(r)
	})
	if h := histEntry(t, histSettle(t, e), hCheckRef("hist-00000033", "build", 1, "unit", 1)); h.Outcome != ledger.CheckInterrupted {
		t.Fatalf("the late receipt: %+v", h.Entry)
	}
}

// A run's transition that leaves its member's start commit as it was
// reads no member's session again (no Attempts read but the run's own);
// a start commit that changes does, and reaches the member's entry.
func TestARunsTransitionReadsNoMemberAgain(t *testing.T) {
	e := newTestEnv(t, nil)
	c := countLedger(t, e, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000034", "", histCrew(e.root))
	id := session.NewID()
	r := journal.Record{ID: id, Name: "lead", Kind: string(session.KindServer), Agent: journal.Agent{ID: "cat"}, Where: "native", Cwd: e.root, StartedAt: histNow,
		StartFrom: journal.StartFromAttempt, Crew: &journal.Crew{RunID: "hist-00000034", CrewID: "hist", Member: "lead", Task: "build", Attempt: 1}}
	in, err := e.srv.sessionRecords.Begin(r)
	if err != nil {
		t.Fatal(err)
	}
	put := func(start string) {
		histTx(t, ld, "hist-00000034", func(tx *ledger.Tx) error {
			a := histAttempt("build", 1, ledger.AttemptRunning, "", start, "")
			a.SessionID = id
			return tx.PutAttempt(a)
		})
	}
	put(commitID(7))
	// The run's record as the engine saves it: the member's session is in
	// it too, with no start commit of its own.
	saveRecord(t, e.srv, crew.Run{ID: "hist-00000034", CrewID: "hist", Name: "Hist", Cwd: e.root, Isolation: crew.IsolationWorktree, State: crew.RunRunning, StartedAt: histNow,
		Members: []crew.MemberState{{Name: "lead", AgentID: "cat", Status: "running", SessionID: id, Task: "build", Attempt: 1}}})
	// Ended and written: its record changes no more while the run moves.
	e.srv.sessionRecords.Settle(in, journal.State{Name: r.Name, Crew: r.Crew, End: &journal.Ending{How: "exited"}, Final: true})
	endedRecord(t, e, id)
	if h := histEntry(t, histSettle(t, e), hSessionRef(id)); h.Commit != commitID(7) {
		t.Fatalf("the member's commit: %q", h.Commit)
	}
	attempts, passes := c.attempts.Load(), c.passes.Load()
	for n := range 3 {
		histTx(t, ld, "hist-00000034", func(tx *ledger.Tx) error { return tx.Event("note", map[string]any{"n": n}) })
		histSettle(t, e)
	}
	if da, dp := c.attempts.Load()-attempts, c.passes.Load()-passes; dp == 0 || da != dp {
		t.Fatalf("%d attempts' reads in %d passes of the run: a member was read again", da, dp)
	}
	attempts, passes = c.attempts.Load(), c.passes.Load()
	put(commitID(8))
	if h := histEntry(t, histSettle(t, e), hSessionRef(id)); h.Commit != commitID(8) {
		t.Fatalf("the member's new commit: %q", h.Commit)
	}
	if da, dp := c.attempts.Load()-attempts, c.passes.Load()-passes; da <= dp {
		t.Fatalf("%d attempts' reads in %d passes: the member was not read again", da, dp)
	}
}

// A member's session whose ledger read fails writes no mark: read again
// after the backoff, its entry has its attempt's start commit.
func TestASessionMarkWaitsForItsLedgerRead(t *testing.T) {
	e := newTestEnv(t, nil)
	c := countLedger(t, e, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000035", "", histCrew(e.root))
	id := session.NewID()
	histTx(t, ld, "hist-00000035", func(tx *ledger.Tx) error {
		a := histAttempt("build", 1, ledger.AttemptRunning, "", commitID(9), "")
		a.SessionID = id
		return tx.PutAttempt(a)
	})
	histSettle(t, e)
	c.failAttempts.Store(1)
	r := journal.Record{ID: id, Name: "lead", Kind: string(session.KindServer), Agent: journal.Agent{ID: "cat"}, Where: "native", Cwd: e.root, StartedAt: histNow,
		StartFrom: journal.StartFromAttempt, Crew: &journal.Crew{RunID: "hist-00000035", CrewID: "hist", Member: "lead", Task: "build", Attempt: 1}}
	if _, err := e.srv.sessionRecords.Begin(r); err != nil {
		t.Fatal(err)
	}
	if h := histEntry(t, histSettle(t, e), hSessionRef(id)); h.Commit != commitID(9) {
		t.Fatalf("the member's commit after a failed read: %q", h.Commit)
	}
	if c.failAttempts.Load() >= 0 {
		t.Fatal("no read failed")
	}
}

// A project made over a run's repository after the run was indexed places
// the run and its receipts in it, without reading a log again, and a
// restart keeps them there.
func TestAProjectChangePlacesARunAndItsReceipts(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	c := countLedger(t, e, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000036", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000036", 1, 1, "TestPlaced")
	receipt := hCheckRef("hist-00000036", "build", 1, "unit", 1)
	ix := histSettle(t, e)
	if h := histEntry(t, ix, receipt); h.Project != "" {
		t.Fatalf("a project before any: %q", h.Project)
	}
	logs := c.logs.Load()
	histProject(t, e, "HIS", e.root)
	ix = histSettle(t, e)
	for _, key := range []string{"run:hist-00000036", receipt} {
		if h := histEntry(t, ix, key); h.Project != "HIS" {
			t.Fatalf("%s's project: %q", key, h.Project)
		}
	}
	if h := histEntry(t, ix, receipt); !strings.Contains(h.Body, "TestPlaced") {
		t.Fatalf("the receipt placed again lost its body: %q", h.Body)
	}
	if c.logs.Load() != logs {
		t.Fatalf("a log read again to place a receipt: %d reads", c.logs.Load()-logs)
	}
	e2 := restartServer(t, e)
	if h := histEntry(t, histSettle(t, e2), receipt); h.Project != "HIS" {
		t.Fatalf("the receipt's project after a restart: %q", h.Project)
	}
}

// A failed setup step indexes its words of failure, never its output.
func TestAFailedStepIndexesItsErrorAlone(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000037", "", histCrew(e.root))
	histTx(t, ld, "hist-00000037", func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptFailed, "setup install", commitID(0), "")); err != nil {
			return err
		}
		r := histReceipt("build", 1, "install", 1, commitID(0), 1, "token=zstepoutputword\n")
		r.Phase, r.Error = ledger.PhaseSetup, "the step exited with status 1"
		return tx.PutCheck(r)
	})
	ix := histSettle(t, e)
	if h := histEntry(t, ix, hCheckRef("hist-00000037", "build", 1, "install", 1)); h.Kind != "step" || h.Body != "the step exited with status 1" {
		t.Fatalf("the step's entry: %+v", h.Entry)
	}
	if got := histFind(t, ix, "zstepoutputword"); len(got) != 0 {
		t.Fatalf("the step's output is indexed: %v", got)
	}
}

// A step of the tree is judged by its words alone, as Dispatch judges its
// phase: a setup step that succeeded (stable unset), a readiness probe
// that passed and a service stopped in order (exit -1, a signal) are no
// failed step; the setup step, the probe and the service that failed with
// words are.
func TestATreeStepIsJudgedByItsWords(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	run := "hist-00000040"
	histCreate(t, ld, run, "", histCrew(e.root))
	step := func(phase, id string, exit int, signal, words string) ledger.CheckRow {
		r := histReceipt("build", 1, id, 1, commitID(0), exit, "")
		r.Phase, r.Stable, r.Signal, r.Error = phase, false, signal, words
		return r
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 1, ledger.AttemptFailed, "ready web", commitID(0), "")); err != nil {
			return err
		}
		for _, r := range []ledger.CheckRow{
			step(ledger.PhaseSetup, "install", 0, "", ""),
			step(ledger.PhaseReady, "probe", 0, "", ""),
			step(ledger.PhaseService, "db", -1, "terminated", ""),
			step(ledger.PhaseSetup, "migrate", 1, "", "the step exited with status 1"),
			step(ledger.PhaseReady, "web", -1, "", "the URL did not answer in time"),
			step(ledger.PhaseService, "queue", -1, "", "the service could not start"),
		} {
			if err := tx.PutCheck(r); err != nil {
				return err
			}
		}
		return nil
	})
	ix := histSettle(t, e)
	for _, id := range []string{"install", "probe", "db"} {
		histGone(t, ix, hCheckRef(run, "build", 1, id, 1))
	}
	for _, id := range []string{"migrate", "web", "queue"} {
		if h := histEntry(t, ix, hCheckRef(run, "build", 1, id, 1)); h.Kind != "step" || h.Outcome != "failed" {
			t.Fatalf("the failed step %s: %+v", id, h.Entry)
		}
	}
}

// A review of a result a person accepted anyway (crew's acceptTask: the
// task succeeds with its failed attempt's result) reviews that attempt,
// and its needs_changes reopens the repair; an earlier attempt that
// succeeded with the same commit is not the subject.
func TestAReviewOfAnAcceptedResultIsLinked(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	run := "hist-00000041"
	histCreate(t, ld, run, "", histReviewCrew(e.root))
	same := commitID(6)
	result, err := json.Marshal(crew.Result{Summary: "Done.", Commit: same})
	if err != nil {
		t.Fatal(err)
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		first := histAttempt("other", 1, ledger.AttemptSucceeded, "", commitID(0), same)
		first.Result, first.ClosedAt = string(result), histNow.Add(90*time.Second)
		// The first review asked for changes; the repair failed its checks
		// with the same commit, and a person accepted it anyway.
		rv1 := histAttempt("review", 1, ledger.AttemptSucceeded, "", same, "")
		rv1.Member, rv1.Result, rv1.ReservedAt, rv1.ClosedAt = "reviewer", needsChanges(t), histNow.Add(100*time.Second), histNow.Add(150*time.Second)
		accepted := histAttempt("other", 2, ledger.AttemptFailed, "check unit", same, same)
		accepted.Result, accepted.ClosedAt = string(result), histNow.Add(170*time.Second)
		rv2 := histAttempt("review", 2, ledger.AttemptSucceeded, "", same, "")
		rv2.Member, rv2.Result, rv2.ReservedAt = "reviewer", needsChanges(t), histNow.Add(180*time.Second)
		for _, a := range []ledger.AttemptRow{first, rv1, accepted, rv2, histAttempt("other", 3, ledger.AttemptRunning, "", same, "")} {
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		return tx.PutTask(ledger.TaskRow{ID: "other", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskRunning, Attempts: 3, ResultAttempt: 2, ResultCommit: same})
	})
	ix := histSettle(t, e)
	rv := attemptKey(run, "review", 2)
	if got := histOut(t, ix, rv, relReviews); !slices.Equal(got, []string{attemptKey(run, "other", 2)}) {
		t.Fatalf("the review of the accepted result reviews: %v", got)
	}
	if got := histOut(t, ix, rv, relReopened); !slices.Equal(got, []string{attemptKey(run, "other", 3)}) {
		t.Fatalf("its repair: %v", got)
	}
	if got := histOut(t, ix, attemptKey(run, "review", 1), relReviews); !slices.Equal(got, []string{attemptKey(run, "other", 1)}) {
		t.Fatalf("the first review reviews: %v", got)
	}
}

// The server without history/owner.lock refuses a reindex (409
// history_read_only), and the owner's index is not cleared.
func TestTheReadOnlyServerRefusesAReindex(t *testing.T) {
	e := newTestEnv(t, nil)
	before, err := histSettle(t, e).Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	other := otherServer(t, e)
	resp, out := other.do("POST", "/api/history/reindex", adminToken, nil)
	if code, _ := out["error"].(map[string]any)["code"].(string); resp.StatusCode != http.StatusConflict || code != "history_read_only" {
		t.Fatalf("a reindex on the other server: %d %v", resp.StatusCode, out)
	}
	if after, _ := histSettle(t, e).Stats(t.Context()); after.Generation != before.Generation {
		t.Fatalf("the owner's index was cleared: generation %d, %d before", after.Generation, before.Generation)
	}
}

// A Check that clears the index while a run's pass is between its receipts
// and its own Replace leaves no mark over missing receipts: the indexer
// clears the index again on its own goroutine and catches up from nothing,
// so the receipt is indexed.
func TestACheckThatClearsDuringARunsPassIsCaughtUp(t *testing.T) {
	e := newTestEnv(t, nil)
	checked := make(chan struct{})
	var once sync.Once
	h := freshIndexer(t, e, func(h *historyIndexer) {
		h.checkIndex = func(ctx context.Context, ix *history.Index) (string, error) {
			if err := ix.Clear(ctx); err != nil { // as a Check's clear in place
				return "", err
			}
			return history.CheckCleared, nil
		}
		h.beforeRunReplace = func(key string) {
			if key != "run:hist-00000038" {
				return
			}
			once.Do(func() {
				// The check runs to its end here, beside the pass.
				go func() {
					h.check(context.Background(), h.index())
					close(checked)
				}()
				<-checked
			})
		}
	})
	_ = h
	ld := e.srv.ledger
	histCreate(t, ld, "hist-00000038", "", histCrew(e.root))
	failedAt(t, ld, "hist-00000038", 1, 1, "TestAcrossTheClear")
	ix := histSettle(t, e)
	select {
	case <-checked:
	default:
		t.Fatal("the check never ran inside the run's pass")
	}
	histEntry(t, ix, "run:hist-00000038")
	histEntry(t, ix, hCheckRef("hist-00000038", "build", 1, "unit", 1))
}

// histReviewCrew is two work tasks and a review of the second.
func histReviewCrew(dir string) crew.Crew {
	c := histCrew(dir)
	c.Tasks = []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build."},
		{ID: "other", Member: "lead", Kind: crew.TaskWork, Instructions: "Other."},
		{ID: "review", Member: "reviewer", Kind: crew.TaskReview, Instructions: "Review.", Needs: []string{"other"}}}
	return c
}

func needsChanges(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(crew.Result{Verdict: crew.VerdictNeedsChanges, Summary: "Not yet."})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A review is linked to its subject by the task it needs, never by a
// commit another task's or another attempt's result shares: build and
// other both return the same commit, other's attempt 1 failed with it and
// its attempt 2 succeeded with it; the review of other reviews other #2
// and reopens other #3.
func TestAReviewIsLinkedByItsSubject(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	run := "hist-00000039"
	histCreate(t, ld, run, "", histReviewCrew(e.root))
	same := commitID(5)
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		for _, a := range []ledger.AttemptRow{
			histAttempt("build", 1, ledger.AttemptSucceeded, "", commitID(0), same),
			histAttempt("other", 1, ledger.AttemptFailed, "check unit", commitID(0), same),
			histAttempt("other", 2, ledger.AttemptSucceeded, "", commitID(0), same),
		} {
			if err := tx.PutAttempt(a); err != nil {
				return err
			}
		}
		rv := histAttempt("review", 1, ledger.AttemptSucceeded, "", same, "")
		rv.Member, rv.Result = "reviewer", needsChanges(t)
		if err := tx.PutAttempt(rv); err != nil {
			return err
		}
		return tx.PutAttempt(histAttempt("other", 3, ledger.AttemptRunning, "", same, ""))
	})
	ix := histSettle(t, e)
	rv := attemptKey(run, "review", 1)
	if got := histOut(t, ix, rv, relReviews); !slices.Equal(got, []string{attemptKey(run, "other", 2)}) {
		t.Fatalf("reviews: %v", got)
	}
	if got := histOut(t, ix, rv, relReopened); !slices.Equal(got, []string{attemptKey(run, "other", 3)}) {
		t.Fatalf("reopened: %v", got)
	}
}

// A review's needs_changes in a run that stopped before the repair began:
// the continuation that makes the repair writes the reopened link.
func TestAReopenedLinkIsTheRepairingContinuations(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	src, cont := "hist-0000003a", "hist-0000003b"
	c := histReviewCrew(e.root)
	histCreate(t, ld, src, "", c)
	histTx(t, ld, src, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("other", 1, ledger.AttemptSucceeded, "", commitID(0), commitID(1))); err != nil {
			return err
		}
		rv := histAttempt("review", 1, ledger.AttemptSucceeded, "", commitID(1), "")
		rv.Member, rv.Result = "reviewer", needsChanges(t)
		return tx.PutAttempt(rv)
	})
	histSettle(t, e)
	histCreate(t, ld, cont, src, c)
	histTx(t, ld, cont, func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("other", 2, ledger.AttemptRunning, "", commitID(1), ""))
	})
	ix := histSettle(t, e)
	if got := histOut(t, ix, attemptKey(src, "review", 1), relReopened); !slices.Equal(got, []string{attemptKey(cont, "other", 2)}) {
		t.Fatalf("reopened across the continuation: %v", got)
	}
}

// A run whose attempts at the task are all uncounted (a setup that failed)
// is walked past, as the engine carries the receipt through it: A's
// attempt 1 fails its check, B (continuing A) fails attempt 2 at setup, C
// (continuing B) makes attempt 3, handed A's receipt, as B's attempt 2
// was at its launch.
func TestHandedThroughARunOfUncountedAttempts(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	c := histCrew(e.root)
	a, b, cc := "hist-0000003c", "hist-0000003d", "hist-0000003e"
	histCreate(t, ld, a, "", c)
	failedAt(t, ld, a, 1, 1, "TestCarried")
	histCreate(t, ld, b, a, c)
	histTx(t, ld, b, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 2, ledger.AttemptFailed, "setup install", commitID(1), "")); err != nil {
			return err
		}
		return histTask(tx, ledger.TaskFailed, 2)
	})
	histCreate(t, ld, cc, b, c)
	histTx(t, ld, cc, func(tx *ledger.Tx) error {
		return tx.PutAttempt(histAttempt("build", 3, ledger.AttemptRunning, "", commitID(1), ""))
	})
	ix := histSettle(t, e)
	if got := histOut(t, ix, hCheckRef(a, "build", 1, "unit", 1), relHanded); !slices.Equal(got, []string{attemptKey(b, "build", 2), attemptKey(cc, "build", 3)}) {
		t.Fatalf("handed through the uncounted run: %v", got)
	}
}

// A handoff made before a withdrawal stays: attempt 1 failed its check and
// attempt 2 was handed its receipt; an upstream review then withdraws both
// (Withdrawn 2), and the link the index wrote is kept.
func TestAHandoffSurvivesALaterWithdrawal(t *testing.T) {
	e := newTestEnv(t, nil)
	ld := e.srv.ledger
	run := "hist-0000003f"
	histCreate(t, ld, run, "", histCrew(e.root))
	failedAt(t, ld, run, 1, 1, "TestWithdrawn")
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 2, ledger.AttemptRunning, "", commitID(1), "")); err != nil {
			return err
		}
		return histTask(tx, ledger.TaskRunning, 2)
	})
	failed := hCheckRef(run, "build", 1, "unit", 1)
	if got := histOut(t, histSettle(t, e), failed, relHanded); !slices.Equal(got, []string{attemptKey(run, "build", 2)}) {
		t.Fatalf("handed before the withdrawal: %v", got)
	}
	histTx(t, ld, run, func(tx *ledger.Tx) error {
		if err := tx.PutAttempt(histAttempt("build", 2, ledger.AttemptCancelled, "withdrawn", commitID(1), "")); err != nil {
			return err
		}
		return tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: crew.TaskWork, State: ledger.TaskBlocked, Attempts: 2, Extra: 1, Withdrawn: 2})
	})
	if got := histOut(t, histSettle(t, e), failed, relHanded); !slices.Equal(got, []string{attemptKey(run, "build", 2)}) {
		t.Fatalf("handed after the withdrawal: %v", got)
	}
}

// A clear that lands between a run's receipts and its own Replace (a
// Check's, its request to catch up lost with the process: a crash) leaves
// no mark over what it took: the run's Replace, of the generation its pass
// began in, is refused and the run read again, so the receipt is indexed,
// and a restart finds it there.
func TestAClearInsideARunsPassLeavesNoMarkOverWhatItTook(t *testing.T) {
	e := newTestEnv(t, nil)
	run := "hist-00000043"
	receipt := hCheckRef(run, "build", 1, "unit", 1)
	var once sync.Once
	var cleared atomic.Bool
	freshIndexer(t, e, func(h *historyIndexer) {
		h.beforeRunReplace = func(key string) {
			if key != hRunRef(run) {
				return
			}
			if _, held, _ := h.index().Mark(context.Background(), receipt); !held {
				return
			}
			once.Do(func() {
				if err := h.index().Clear(context.Background()); err != nil {
					t.Error(err)
				}
				cleared.Store(true)
			})
		}
	})
	ld := e.srv.ledger
	histCreate(t, ld, run, "", histCrew(e.root))
	failedAt(t, ld, run, 1, 1, "TestAcrossACrash")
	ix := histSettle(t, e)
	if !cleared.Load() {
		t.Fatal("the index was never cleared inside the run's pass")
	}
	histEntry(t, ix, hRunRef(run))
	if h := histEntry(t, ix, receipt); !strings.Contains(h.Body, "TestAcrossACrash") {
		t.Fatalf("the receipt: %+v", h.Entry)
	}
	if h := histEntry(t, histSettle(t, restartServer(t, e)), receipt); !strings.Contains(h.Body, "TestAcrossACrash") {
		t.Fatalf("the receipt after a restart: %+v", h.Entry)
	}
}

// A dirty queue that is never empty defers the reconcile by one pass at
// most: with a source marked again at every pass, the reconcile's timer
// fires and a run record the other server wrote (no hook of this one) is
// indexed.
func TestABusyQueueStillReconciles(t *testing.T) {
	e := newTestEnv(t, nil)
	tick := make(chan time.Time, 1)
	busy := hSessionRef("zbusy") // no record: its pass removes nothing, at once
	var spinning atomic.Bool
	var passes atomic.Int64
	h := freshIndexer(t, e, func(h *historyIndexer) {
		h.after = func(d time.Duration) <-chan time.Time {
			if d == h.reconcileTick {
				return tick
			}
			return time.After(d)
		}
		h.beforePass = func(key string) {
			if key == busy && spinning.Load() {
				passes.Add(1)
				h.mark(busy)
			}
		}
	})
	t.Cleanup(func() { spinning.Store(false) })
	histSettle(t, e)
	other := otherServer(t, e)
	saveRecord(t, other.srv, crew.Run{ID: "hist-00000044", Label: "the busy server's run", Cwd: e.root, StartedAt: histNow})
	spinning.Store(true)
	h.mark(busy)
	until(t, "the queue busy", func() bool { return passes.Load() > 2 })
	tick <- histNow
	ix := h.index()
	until(t, "the other server's run indexed while the queue is busy", func() bool {
		_, err := ix.Entry(t.Context(), "run:hist-00000044")
		return err == nil
	})
	spinning.Store(false)
}

// A repository read that fails for a moment is not written down as none:
// the pass fails and is read again, and the run is indexed with its
// repository. One that keeps failing past the retries is indexed without
// it and a mark that says so, and the reconcile after the read recovers
// writes it whole.
func TestARepositoryNotKnownIsReadAgain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retries int
	}{{"a moment", maxRepoRetries}, {"past the retries", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.gitRepo(t)
			var fails atomic.Int64
			fails.Store(2)
			freshIndexer(t, e, func(h *historyIndexer) {
				h.repoRetries = tc.retries
				h.after = func(d time.Duration) <-chan time.Time {
					if d == h.reconcileTick {
						return nil
					}
					return time.After(d)
				}
				h.repoOf = func(ctx context.Context, dir string) (string, string, error) {
					if dir == e.root && fails.Add(-1) >= 0 {
						return "", "", errors.New("git rev-parse: timed out")
					}
					return gitcli.RepoOf(ctx, dir)
				}
			})
			run := "hist-00000045"
			histCreate(t, e.srv.ledger, run, "", histCrew(e.root))
			ix := histSettle(t, e)
			if fails.Load() > 0 {
				t.Fatal("the repository's reads did not fail twice")
			}
			h := histEntry(t, ix, hRunRef(run))
			if tc.retries > 1 {
				if h.Repo == "" || strings.HasSuffix(histMark(t, ix, hRunRef(run)), "/U") {
					t.Fatalf("the run after a failed read: repo %q, mark %s", h.Repo, histMark(t, ix, hRunRef(run)))
				}
				return
			}
			if h.Repo != "" || !strings.HasSuffix(histMark(t, ix, hRunRef(run)), "/U") {
				t.Fatalf("the run past its retries: repo %q, mark %s", h.Repo, histMark(t, ix, hRunRef(run)))
			}
			ix = histReconcile(t, e)
			if h := histEntry(t, ix, hRunRef(run)); h.Repo == "" || strings.HasSuffix(histMark(t, ix, hRunRef(run)), "/U") {
				t.Fatalf("the run after the read recovered: repo %q, mark %s", h.Repo, histMark(t, ix, hRunRef(run)))
			}
		})
	}
}

// A forgotten record's rows are gone from the index before Forget
// answers, and no pass publishes a record its source no longer answers
// for: with the indexer held on another source, a search made as the
// Forget (or Forget all) answers finds nothing; with a pass of the record
// held between its read and its publish, the same, and the held pass,
// released, publishes nothing.
func TestAForgetAnswersOnceTheIndexHoldsNothingOfIt(t *testing.T) {
	forget := func(t *testing.T, e *testEnv, id string, all bool) {
		t.Helper()
		if all {
			if resp, out := e.do("DELETE", "/api/history/sessions", adminToken, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("forget all: %d %v", resp.StatusCode, out)
			}
			return
		}
		if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forget: %d %v", resp.StatusCode, out)
		}
	}
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("held on another source, all %t", all), func(t *testing.T) {
			e := newTestEnv(t, nil)
			id, _ := forgettable(t, e, "zforgetheld")
			gate, reached := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := sync.OnceFunc(func() { close(gate) })
			h := freshIndexer(t, e, func(h *historyIndexer) {
				h.beforePass = func(key string) {
					if key == "run:hist-00000046" {
						once.Do(func() {
							close(reached)
							<-gate
						})
					}
				}
			})
			t.Cleanup(release) // a failed check leaves no pass held
			histSettle(t, e)
			saveRecord(t, e.srv, crew.Run{ID: "hist-00000046", Label: "the held run", Cwd: e.root, StartedAt: histNow})
			reachGate(t, reached)
			forget(t, e, id, all)
			forgotten(t, h.index(), id, "zforgetheld")
			release()
			forgotten(t, histSettle(t, e), id, "zforgetheld")
		})
	}
	t.Run("a pass of the record held", func(t *testing.T) {
		e := newTestEnv(t, nil)
		id, in := forgettable(t, e, "zforgetpass")
		key := hSessionRef(id)
		held, heldAt := make(chan struct{}), make(chan struct{})
		next, nextAt := make(chan struct{}), make(chan struct{})
		var armed atomic.Bool
		var heldOnce, nextOnce sync.Once
		var pass atomic.Int64
		releaseHeld, releaseNext := sync.OnceFunc(func() { close(held) }), sync.OnceFunc(func() { close(next) })
		h := freshIndexer(t, e, func(h *historyIndexer) {
			h.afterMark = func(k string) {
				if k == key && armed.Load() {
					heldOnce.Do(func() {
						close(heldAt)
						<-held
					})
				}
			}
			h.beforePass = func(k string) {
				// The pass after the held one: the index is read before it
				// runs.
				if k == key && armed.Load() && pass.Add(1) == 2 {
					nextOnce.Do(func() {
						close(nextAt)
						<-next
					})
				}
			}
		})
		t.Cleanup(func() { releaseHeld(); releaseNext() }) // a failed check leaves no pass held
		histSettle(t, e)
		armed.Store(true)
		e.srv.sessionRecords.Chat(in, journal.ChatLine{Kind: "message", Text: "one more", ByName: "nate", At: histNow})
		e.srv.sessionRecords.Flush()
		reachGate(t, heldAt)
		forget(t, e, id, false)
		forgotten(t, h.index(), id, "zforgetpass")
		releaseHeld()
		reachGate(t, nextAt)
		forgotten(t, h.index(), id, "zforgetpass")
		releaseNext()
		forgotten(t, histSettle(t, e), id, "zforgetpass")
	})
}

// reachGate waits for a seam's gate to be reached, bounding a hang.
func reachGate(t *testing.T, at <-chan struct{}) {
	t.Helper()
	select {
	case <-at:
	case <-time.After(2 * testWait):
		t.Fatal("the gate was never reached")
	}
}

// A Forget the index cannot take (the indexer stopped: its index not
// open) says so (503 history_index_failed) with the record gone from the
// journal, and the next start's catch-up drops its rows.
func TestAForgetTheIndexCannotTakeSaysSo(t *testing.T) {
	e := newTestEnv(t, nil)
	id, _ := forgettable(t, e, "zforgetunopened")
	e.srv.stopHistory()
	if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "history_index_failed" {
		t.Fatalf("forget with the index closed: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("GET", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the record after the forget: %d", resp.StatusCode)
	}
	forgotten(t, histSettle(t, restartServer(t, e)), id, "zforgetunopened")
}
