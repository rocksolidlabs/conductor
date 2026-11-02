package ledger

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openLedger(t *testing.T, dir string) *Ledger {
	t.Helper()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func newRun(t *testing.T, l *Ledger, id string) {
	t.Helper()
	ctx := context.Background()
	rev, err := l.Transition(ctx, id, func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: id, CrewID: "team", Crew: `{"id":"team"}`, Base: "abc123", State: RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Event("run.created", map[string]any{"base": "abc123"})
	})
	if err != nil || rev != 1 {
		t.Fatalf("new run %s: rev %d err %v", id, rev, err)
	}
}

func TestOpenMakesTheSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	for _, name := range []string{File, Lock} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == File && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", name, fi.Mode().Perm())
		}
	}
	rows, err := l.db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if want := "attempts checks commands events messages prerequisites runs schema_version tasks trees"; strings.Join(tables, " ") != want {
		t.Fatalf("tables %v", tables)
	}
	newRun(t, l, "team-00000001")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Closed with its write-ahead log checkpointed, and opened again whole.
	if fi, err := os.Stat(filepath.Join(dir, File+"-wal")); err == nil && fi.Size() != 0 {
		t.Fatalf("the WAL holds %d bytes after Close", fi.Size())
	}
	again := openLedger(t, dir)
	if r, ok, err := again.Run(context.Background(), "team-00000001"); err != nil || !ok || r.Revision != 1 || r.State != RunRunning || r.Base != "abc123" {
		t.Fatalf("after reopen: %+v %v %v", r, ok, err)
	}
}

func TestANewerSchemaOpensReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	if _, err := l.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
		t.Fatal(err)
	}
	l.Close()
	again := openLedger(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "newer") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if _, err := again.Transition(context.Background(), "team-00000001", func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write: %v", err)
	}
	if _, err := again.Recover(context.Background()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a recover: %v", err)
	}
	if runs, err := again.Runs(context.Background()); err != nil || len(runs) != 1 {
		t.Fatalf("a read: %v %v", runs, err)
	}
}

func TestADamagedDatabaseIsNeverWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	for i := range 40 {
		newRun(t, l, fmt.Sprintf("team-%08x", i))
	}
	l.Close()
	path := filepath.Join(dir, File)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Garbage over the third page, a table's root: the file header stays,
	// the content does not.
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 4096)), 2*4096); err != nil {
		t.Fatal(err)
	}
	f.Close()
	again, err := Open(dir)
	if err != nil {
		// Refused outright is as good: nothing is written to it.
		return
	}
	defer again.Close()
	if ro, _ := again.ReadOnly(); !ro {
		t.Fatal("a damaged database opened for writing")
	}
}

// The helper holds the owner lock of the directory LEDGER_HOLD until its
// stdin closes.
func TestHelperHoldsTheOwnerLock(t *testing.T) {
	dir := os.Getenv("LEDGER_HOLD")
	if dir == "" {
		t.Skip("a helper")
	}
	l, err := Open(dir)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("held")
	bufio.NewReader(os.Stdin).ReadString('\n')
	l.Close()
}

func TestTheOwnerLockHoldsAcrossProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsTheOwnerLock$", "-test.v")
	helper.Env = append(os.Environ(), "LEDGER_HOLD="+dir)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		helper.Wait()
	}()
	sc := bufio.NewScanner(stdout)
	held := false
	for sc.Scan() {
		if sc.Text() == "held" {
			held = true
			break
		}
		if strings.HasPrefix(sc.Text(), "error:") {
			t.Fatal(sc.Text())
		}
	}
	if !held {
		t.Fatal("the helper did not take the lock")
	}
	old := LockWait
	LockWait = 300 * time.Millisecond
	t.Cleanup(func() { LockWait = old })
	// Open does not wait: the ledger is read-only at once, waiting for the
	// lock, and says who holds it; past LockWait it stops waiting.
	began := time.Now()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 200*time.Millisecond {
		t.Fatalf("Open waited %v for a held lock", took)
	}
	ro, why := l.ReadOnly()
	if !ro || !strings.HasPrefix(why, "waiting for the owner lock: another server holds") || !strings.Contains(why, "pid") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a transition while waiting: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ro, why = l.ReadOnly()
		if !strings.HasPrefix(why, "waiting") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still waiting after LockWait: %s", why)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ro || !strings.HasPrefix(why, "another server holds") {
		t.Fatalf("after LockWait: %v %s", ro, why)
	}
	l.Close()
	// Let go within the wait, the lock is taken in the background: the
	// ledger turns the owner's, and OnOwned's function runs once.
	LockWait = 5 * time.Second
	l, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ro, why := l.ReadOnly(); !ro || !strings.HasPrefix(why, "waiting") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	owned := make(chan struct{}, 2)
	l.OnOwned(func() { owned <- struct{}{} })
	stdin.Close()
	helper.Wait()
	select {
	case <-owned:
	case <-time.After(4 * time.Second):
		ro, why := l.ReadOnly()
		t.Fatalf("the lock was not taken once let go: read-only %v: %s", ro, why)
	}
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("still read-only: %s", why)
	}
	if _, err := l.Transition(context.Background(), "team-00000001", func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: "team-00000001", CrewID: "team", State: RunRunning, CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return tx.Event("run.created", nil)
	}); err != nil {
		t.Fatalf("a transition as the late owner: %v", err)
	}
	l.OnOwned(func() { owned <- struct{}{} })
	select {
	case <-owned:
		t.Fatal("OnOwned ran twice")
	case <-time.After(200 * time.Millisecond):
	}
	l.Close()
	// A new Open takes it at once.
	l = openLedger(t, dir)
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("still read-only: %s", why)
	}
}

// A Close that comes while the ledger waits for the owner lock leaves no
// try for it under way (#281): each try is made under the ledger's lock
// and only while the ledger is open, so Close waits for one in flight and
// none follows it. A try opens the lock file, making it when it is
// missing, and a lock taken opens the database: once Close has returned,
// none of that may happen in the directory, which a test's cleanup may be
// removing by then (a second server's ledger, still waiting, made its
// files again under a temporary directory being removed). The wait's first
// try is held at a gate (takeLock) while Close is called.
func TestACloseLeavesNoTryForTheOwnerLockUnderWay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	openLedger(t, dir) // the owner: the next ledger waits for its lock
	var (
		w       atomic.Pointer[Ledger]
		tries   atomic.Int32
		ready   = make(chan struct{}) // w is set
		trying  = make(chan struct{}) // the wait's first try is under way
		gate    = make(chan struct{}) // lets it go on
		closed  = make(chan struct{}) // Close has returned
		outside atomic.Bool           // a try made without the ledger's lock
		late    atomic.Bool           // a try made once Close had begun, or under way when it returned
		letGo   sync.Once
	)
	old := takeLock
	takeLock = func(path string) (*ownerLock, string, bool, error) {
		n := tries.Add(1)
		if n == 1 {
			return old(path) // Open's own: held by the owner
		}
		<-ready
		l := w.Load()
		if l.mu.TryLock() {
			l.mu.Unlock()
			outside.Store(true)
		}
		select {
		case <-l.done:
			late.Store(true)
		default:
		}
		if n == 2 {
			close(trying)
			<-gate
			select {
			case <-closed:
				late.Store(true)
			default:
			}
		}
		return old(path)
	}
	t.Cleanup(func() { takeLock = old })
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		letGo.Do(func() { close(gate) })
		l.Close()
	})
	w.Store(l)
	close(ready)
	<-trying
	go func() {
		l.Close()
		close(closed)
	}()
	letGo.Do(func() { close(gate) })
	<-closed
	if outside.Load() {
		t.Fatal("a try for the owner lock was made outside the ledger's lock: a Close could return with it under way")
	}
	if late.Load() {
		t.Fatal("a try for the owner lock was made once Close had begun, or was still under way when it returned")
	}
	if ro, why := l.ReadOnly(); !ro {
		t.Fatalf("the ledger closed while it waited took the lock: %s", why)
	}
}

// A wait that comes to its try with the ledger closed makes none (#281).
// A Close can land after the wait has taken its tick and before it takes
// the ledger's lock, and the wait's select can pick a tick that is due
// over a closed done: either way the wait reaches its try with the ledger
// closed, and a try made then would open owner.lock, making it, and take
// the lock when it is free, after Close had returned. The test above
// holds a try that is under way when Close comes; this one runs the wait
// on a ledger in the state a later Close leaves it in, closed with done
// still open, so that its first tick brings it to the check.
func TestAWaitThatFindsTheLedgerClosedMakesNoTry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var tries atomic.Int32
	old := takeLock
	takeLock = func(path string) (*ownerLock, string, bool, error) {
		tries.Add(1)
		return old(path)
	}
	t.Cleanup(func() { takeLock = old })
	l := &Ledger{dir: dir, done: make(chan struct{}), closed: true}
	l.awaitLock(filepath.Join(dir, Lock), filepath.Join(dir, File)) // returns at its first tick
	if n := tries.Load(); n != 0 {
		t.Errorf("a closed ledger tried for the owner lock %d time(s)", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		var made []string
		for _, e := range entries {
			made = append(made, e.Name())
		}
		t.Errorf("a closed ledger made %s in its directory", strings.Join(made, ", "))
	}
}

func TestATransitionIsAllOrNothing(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	boom := errors.New("boom")
	_, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: "team-00000001", CrewID: "team", State: RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.Event("run.created", nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if _, ok, _ := l.Run(ctx, "team-00000001"); ok {
		t.Fatal("the run row survived the failed transition")
	}
	if ev, _ := l.Events(ctx, "team-00000001", 0, 10); len(ev) != 0 {
		t.Fatalf("events %v", ev)
	}
	// A transition on a run with no row fails too: an event of it breaks
	// the foreign key, and a transition writing nothing finds no row.
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error { return tx.Event("x", nil) }); err == nil {
		t.Fatal("an event of a run with no row")
	}
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error { return nil }); !errors.Is(err, ErrNoRun) {
		t.Fatalf("no run: %v", err)
	}
	newRun(t, l, "team-00000001")
	// A commit that fails leaves the intent unwritten, and later ones land.
	l.beforeCommit = func() error { return errors.New("disk full") }
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("dispatch", nil) }); err == nil {
		t.Fatal("a failed commit succeeded")
	}
	l.beforeCommit = nil
	if ev, _ := l.Events(ctx, "team-00000001", 0, 10); len(ev) != 1 {
		t.Fatalf("events after the failed commit: %v", ev)
	}
	if rev, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("launched", nil) }); err != nil || rev != 2 {
		t.Fatalf("after the failed commit: rev %d err %v", rev, err)
	}
	if ev, _ := l.Events(ctx, "team-00000001", 0, 10); len(ev) != 2 || ev[1].Seq != 2 || ev[1].Kind != "launched" {
		t.Fatalf("events %v", ev)
	}
}

func TestEventsAreSequencedWithoutGaps(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("tick", map[string]int{"i": i}) }); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ev, err := l.Events(ctx, "team-00000001", 0, 100)
	if err != nil || len(ev) != 51 {
		t.Fatalf("%d events %v", len(ev), err)
	}
	for i, e := range ev {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if r, _, _ := l.Run(ctx, "team-00000001"); r.Revision != 51 {
		t.Fatalf("revision %d", r.Revision)
	}
	if after, _ := l.Events(ctx, "team-00000001", 49, 100); len(after) != 2 || after[0].Seq != 50 {
		t.Fatalf("after 49: %v", after)
	}
}

func TestLastEvent(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	if seq, err := l.LastEvent(ctx, "team-00000001"); err != nil || seq != 0 {
		t.Fatalf("a run the ledger has not: %d %v", seq, err)
	}
	newRun(t, l, "team-00000001")
	newRun(t, l, "team-00000002")
	first, err := l.LastEvent(ctx, "team-00000001")
	if err != nil || first < 1 {
		t.Fatalf("a new run's last event: %d %v", first, err)
	}
	for i := range 3 {
		if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("tick", map[string]int{"i": i}) }); err != nil {
			t.Fatal(err)
		}
	}
	seq, err := l.LastEvent(ctx, "team-00000001")
	if err != nil || seq != first+3 {
		t.Fatalf("after three events: %d, want %d (%v)", seq, first+3, err)
	}
	ev, err := l.Events(ctx, "team-00000001", seq-1, 10)
	if err != nil || len(ev) != 1 || ev[0].Seq != seq {
		t.Fatalf("the newest event is the last: %v %v", ev, err)
	}
	// Another run's events are its own.
	if other, err := l.LastEvent(ctx, "team-00000002"); err != nil || other != first {
		t.Fatalf("the other run's last event: %d %v", other, err)
	}
}

func TestReceipts(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	old := maxCommands
	maxCommands = 3
	t.Cleanup(func() { maxCommands = old })
	command := func(actor, id, intent, fp string, settle string) (CommandRow, bool, error) {
		var prior CommandRow
		var found bool
		_, err := l.Transition(ctx, "team-00000001", func(tx *Tx) (err error) {
			prior, found, err = tx.Command(actor, id, intent, fp)
			if err != nil || found {
				return err
			}
			return tx.Settle(actor, id, settle, map[string]string{"task": "build"})
		})
		return prior, found, err
	}
	if _, found, err := command("alice", "c1", "task.retry", "fp1", CommandDone); err != nil || found {
		t.Fatalf("new: %v %v", found, err)
	}
	prior, found, err := command("alice", "c1", "task.retry", "fp1", CommandDone)
	if err != nil || !found || prior.Status != CommandDone || prior.Receipt != `{"task":"build"}` {
		t.Fatalf("repeat: %+v %v %v", prior, found, err)
	}
	if _, _, err := command("alice", "c1", "task.retry", "fp2", CommandDone); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("other content: %v", err)
	}
	if _, _, err := command("alice", "c1", "task.cancel", "fp1", CommandDone); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("other intent: %v", err)
	}
	if _, found, err := command("bob", "c1", "task.retry", "fp1", CommandUncertain); err != nil || found {
		t.Fatalf("another actor: %v %v", found, err)
	}
	if _, found, err := command("alice", "c2", "run.pause", "fp3", CommandFailed); err != nil || found {
		t.Fatalf("c2: %v %v", found, err)
	}
	if _, _, err := command("alice", "c3", "run.pause", "fp4", CommandDone); !errors.Is(err, ErrTooManyCommands) {
		t.Fatalf("past the bound: %v", err)
	}
	if r, ok, err := l.Receipt(ctx, "team-00000001", "bob", "c1"); err != nil || !ok || r.Status != CommandUncertain || r.Intent != "task.retry" {
		t.Fatalf("bob's receipt: %+v %v %v", r, ok, err)
	}
	if _, ok, _ := l.Receipt(ctx, "team-00000001", "carol", "c1"); ok {
		t.Fatal("carol has a receipt")
	}
	if all, _ := l.Commands(ctx, "team-00000001"); len(all) != 3 {
		t.Fatalf("commands %v", all)
	}
	// A refused transition writes nothing: the conflict left no receipt.
	if all, _ := l.Commands(ctx, "team-00000001"); len(all) != 3 {
		t.Fatalf("commands %v", all)
	}
}

func TestRecoverConcludesOnce(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	newRun(t, l, "team-00000002")
	states := []string{AttemptReserved, AttemptLaunched, AttemptRunning, AttemptSubmitting, AttemptVerifying, AttemptSucceeded, AttemptFailed}
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		for i, st := range states {
			id := fmt.Sprintf("t%d", i)
			if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: id, Member: "lead", Kind: "work", State: TaskRunning}); err != nil {
				return err
			}
			if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: id, N: 1, Member: "lead", State: st, ReservedAt: tx.Now()}); err != nil {
				return err
			}
		}
		for k, st := range []string{CheckStarted, CheckRunning, CheckFinished} {
			if err := tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "t4", N: 1, ID: "unit", K: k, Argv: []string{"go", "test"}, State: st, StartedAt: tx.Now()}); err != nil {
				return err
			}
		}
		if _, _, err := tx.Command("alice", "c1", "task.retry", "fp"); err != nil {
			return err
		}
		if _, _, err := tx.Command("alice", "c2", "task.retry", "fp"); err != nil {
			return err
		}
		return tx.Settle("alice", "c2", CommandDone, nil)
	}); err != nil {
		t.Fatal(err)
	}
	// The second run was stopped: nothing to conclude there.
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		return tx.PutRun(RunRow{ID: "team-00000002", CrewID: "team", State: RunStopped, CreatedAt: tx.Now(), ClosedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := l.Recover(ctx)
	if err != nil || len(rec) != 1 || rec[0].RunID != "team-00000001" || !rec[0].Interrupted {
		t.Fatalf("%+v %v", rec, err)
	}
	if rec[0].Attempts[AttemptUncertain] != 1 || rec[0].Attempts[AttemptInterrupted] != 4 || rec[0].Checks != 2 || rec[0].Commands != 1 {
		t.Fatalf("%+v", rec[0])
	}
	attempts, _ := l.Attempts(ctx, "team-00000001")
	want := map[string]string{"t0": AttemptUncertain, "t1": AttemptInterrupted, "t2": AttemptInterrupted, "t3": AttemptInterrupted, "t4": AttemptInterrupted, "t5": AttemptSucceeded, "t6": AttemptFailed}
	for _, a := range attempts {
		if a.State != want[a.TaskID] || (a.State != AttemptSucceeded && a.State != AttemptFailed && a.ClosedAt.IsZero()) {
			t.Fatalf("attempt %s: %+v", a.TaskID, a)
		}
	}
	checks, _ := l.Checks(ctx, "team-00000001", "t4", 1, false)
	if len(checks) != 3 || checks[0].State != CheckInterrupted || checks[1].State != CheckInterrupted || checks[2].State != CheckFinished || checks[0].Argv[0] != "go" {
		t.Fatalf("checks %+v", checks)
	}
	if c, _, _ := l.Receipt(ctx, "team-00000001", "alice", "c1"); c.Status != CommandUncertain {
		t.Fatalf("c1 %+v", c)
	}
	if c, _, _ := l.Receipt(ctx, "team-00000001", "alice", "c2"); c.Status != CommandDone {
		t.Fatalf("c2 %+v", c)
	}
	r, _, _ := l.Run(ctx, "team-00000001")
	ev, _ := l.Events(ctx, "team-00000001", 0, 100)
	if r.State != RunInterrupted || len(ev) != 2 || ev[1].Kind != "recovered" || !strings.Contains(ev[1].Payload, `"Checks":2`) {
		t.Fatalf("run %+v events %+v", r, ev)
	}
	// Again: nothing open, nothing written.
	if rec, err := l.Recover(ctx); err != nil || len(rec) != 0 {
		t.Fatalf("second recover: %+v %v", rec, err)
	}
	if ev2, _ := l.Events(ctx, "team-00000001", 0, 100); len(ev2) != 2 {
		t.Fatalf("events after the second recover: %d", len(ev2))
	}
	if r2, _, _ := l.Run(ctx, "team-00000001"); r2.Revision != r.Revision {
		t.Fatalf("revision moved from %d to %d", r.Revision, r2.Revision)
	}
}

func TestPrune(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	close := func(id string) {
		if _, err := l.Transition(ctx, id, func(tx *Tx) error {
			r, _, err := tx.Run()
			if err != nil {
				return err
			}
			r.State, r.Outcome, r.ClosedAt = RunFinished, OutcomeSucceeded, tx.Now()
			return tx.PutRun(r)
		}); err != nil {
			t.Fatal(err)
		}
	}
	newRun(t, l, "team-00000001")
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: "build", Member: "lead", Kind: "work", State: TaskSucceeded}); err != nil {
			return err
		}
		if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptSucceeded, ReservedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, Argv: []string{"true"}, State: CheckFinished, StartedAt: tx.Now(), Log: []byte("ok")}); err != nil {
			return err
		}
		_, _, err := tx.Command("alice", "c1", "task.retry", "fp")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); !errors.Is(err, ErrNotPrunable) {
		t.Fatalf("an open run pruned: %v", err)
	}
	close("team-00000001")
	// A continuation names it: it stays.
	newRun(t, l, "team-00000002")
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		r, _, _ := tx.Run()
		r.ContinuedFrom = "team-00000001"
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); !errors.Is(err, ErrNotPrunable) {
		t.Fatalf("a cited run pruned: %v", err)
	}
	// Another run imports its result: it stays.
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		r, _, _ := tx.Run()
		r.ContinuedFrom = ""
		if err := tx.PutRun(r); err != nil {
			return err
		}
		return tx.PutTask(TaskRow{RunID: "team-00000002", ID: "build", Member: "lead", Kind: "work", State: TaskSucceeded, ImportedFrom: "team-00000001/build/1"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); !errors.Is(err, ErrNotPrunable) {
		t.Fatalf("an imported run pruned: %v", err)
	}
	// Nothing cites it: its rows go, every table's.
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error {
		return tx.PutTask(TaskRow{RunID: "team-00000002", ID: "build", Member: "lead", Kind: "work", State: TaskSucceeded})
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Prune(ctx, "team-00000001"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Run(ctx, "team-00000001"); ok {
		t.Fatal("the run row stayed")
	}
	for table, want := range map[string]int{"tasks": 1, "attempts": 0, "checks": 0, "commands": 0, "events": 1} {
		var n int
		if err := l.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: %d rows (%v), want %d", table, n, err, want)
		}
	}
	if err := l.Prune(ctx, "team-00000001"); err != nil {
		t.Fatalf("pruning a run that is gone: %v", err)
	}
}

func TestBounds(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: "build", Member: "lead", Kind: "work", State: TaskRunning}); err != nil {
			return err
		}
		if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptVerifying, ReservedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "unit", K: 1, State: CheckFinished, StartedAt: tx.Now(), Log: make([]byte, MaxLog+1)})
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a log past the bound: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.Event("big", strings.Repeat("x", MaxPayload))
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a payload past the bound: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		r, _, _ := tx.Run()
		r.Crew = strings.Repeat("x", MaxCrew+1)
		return tx.PutRun(r)
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a crew past the bound: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutRun(RunRow{ID: "team-00000009", State: RunRunning, CreatedAt: tx.Now()})
	}); err == nil {
		t.Fatal("another run's row put in this run's transition")
	}
}

// A damaged header, which the database will not open at all, leaves the
// ledger with nothing to read and the server running: Open does not fail.
func TestADamagedHeaderDoesNotFailOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	l.Close()
	path := filepath.Join(dir, File)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 100)), 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed on a damaged header: %v", err)
	}
	defer again.Close()
	if ro, why := again.ReadOnly(); !ro || why == "" {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if _, err := again.Runs(context.Background()); err == nil {
		t.Fatal("a read of a database that will not open")
	}
	if _, err := again.Transition(context.Background(), "team-00000001", func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write: %v", err)
	}
}

// A database in a rollback journal's mode, as another tool or a restore
// can leave it, that the server cannot write opens read-only and is read
// (#417): the read-only connection sets no journal mode, since a switch to
// WAL writes the file's header, which read-only mode refuses, and a
// refused open would leave the ledger nothing to read. The file stays in
// the mode it was found in; written again by its owner, it is back in WAL.
func TestARollbackJournalDatabaseItCannotWriteIsRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a file whatever its mode")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	newRun(t, l, "team-00000002")
	if rev, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("launched", nil) }); err != nil || rev != 2 {
		t.Fatalf("a transition: rev %d err %v", rev, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, File)
	// Another tool's connection, which sets no journal mode, leaves the
	// file in a rollback journal's mode.
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	err = other.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&mode)
	if cerr := other.Close(); err != nil || cerr != nil || mode != "delete" {
		t.Fatalf("the rollback journal's mode: %q %v %v", mode, err, cerr)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	again := openLedger(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "readonly database") || strings.Contains(why, "nor read") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	// It reads what the file holds, the writable open still failing.
	if runs, err := again.Runs(ctx); err != nil || len(runs) != 2 || runs[0].ID != "team-00000001" || runs[1].ID != "team-00000002" {
		t.Fatalf("the runs: %+v %v", runs, err)
	}
	if r, ok, err := again.Run(ctx, "team-00000001"); err != nil || !ok || r.Revision != 2 || r.State != RunRunning || r.Base != "abc123" {
		t.Fatalf("a run: %+v %v %v", r, ok, err)
	}
	events, err := again.Events(ctx, "team-00000001", 0, 0)
	if err != nil || len(events) != 2 || events[0].Seq != 1 || events[0].Kind != "run.created" || events[1].Seq != 2 || events[1].Kind != "launched" {
		t.Fatalf("the events: %+v %v", events, err)
	}
	if _, err := again.Transition(ctx, "team-00000001", func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write: %v", err)
	}
	if err := again.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("the journal mode the read-only ledger left: %q %v", mode, err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}

	// Writable again, the file is the owner's, whose connection sets WAL.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	owner := openLedger(t, dir)
	if ro, why := owner.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	if err := owner.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("the owner's journal mode: %q %v", mode, err)
	}
	if runs, err := owner.Runs(ctx); err != nil || len(runs) != 2 {
		t.Fatalf("the runs: %+v %v", runs, err)
	}
}

// A database in WAL mode, the mode the ledger leaves it in, that the
// server cannot write opens read-only (#444). SQLite opens a file the
// process may not write read-only without an error, and with the file
// already in WAL mode the connection's journal_mode(WAL) has nothing to
// write, so the open succeeds and only a write would show it: the ledger
// tries one at open, rolled back. It reads what the file holds, refuses a
// write as read-only rather than failing with SQLite's error, and leaves
// the file's mode as it found it; made writable again, it is the owner's.
func TestAWALDatabaseItCannotWriteIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a file whatever its mode")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	newRun(t, l, "team-00000001")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, File)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	again := openLedger(t, dir)
	ro, why := again.ReadOnly()
	if !ro || !strings.Contains(why, "readonly database") || strings.Contains(why, "nor read") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	var mode string
	if err := again.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("the journal mode: %q %v", mode, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("the file's mode after the open: %v %v", fi.Mode(), err)
	}
	if runs, err := again.Runs(ctx); err != nil || len(runs) != 1 || runs[0].ID != "team-00000001" {
		t.Fatalf("the runs: %+v %v", runs, err)
	}
	if _, err := again.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("launched", nil) }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a write: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}

	// The file made writable again, the WAL and its index beside it are
	// not: SQLite made them with the file's mode, and a connection that
	// may not write the WAL does not write. The ledger says so, as it
	// does for the file.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	half := openLedger(t, dir)
	if ro, why := half.ReadOnly(); !ro || !strings.Contains(why, "readonly database") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if err := half.Close(); err != nil {
		t.Fatal(err)
	}
	// All three writable, the ledger is the owner's, and nothing of the
	// opens' tries is in the file.
	for _, p := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	owner := openLedger(t, dir)
	if ro, why := owner.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	if rev, err := owner.Transition(ctx, "team-00000001", func(tx *Tx) error { return tx.Event("launched", nil) }); err != nil || rev != 2 {
		t.Fatalf("a write: rev %d err %v", rev, err)
	}
}

// Locks unsupported where there is no database yet: nothing to read, the
// ledger says so, and Open does not fail.
func TestUnsupportedLocksWithNoDatabaseDoNotFailOpen(t *testing.T) {
	old := takeLock
	takeLock = func(path string) (*ownerLock, string, bool, error) {
		return nil, "the filesystem of " + path + " does not support locks (test)", false, nil
	}
	t.Cleanup(func() { takeLock = old })
	l, err := Open(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ro, why := l.ReadOnly()
	if !ro || !strings.Contains(why, "does not support locks") || !strings.Contains(why, "no database to read") {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	if _, err := l.Runs(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a read: %v", err)
	}
	if _, _, err := l.Receipt(context.Background(), "team-00000001", "admin", "c1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a receipt: %v", err)
	}
}

// A data directory with '#', '?' or '%' in its name is the directory it
// is: the path travels escaped in the connection string.
func TestAPathWithURICharactersIsOpened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "odd #dir?x%2Fy", "runs")
	l := openLedger(t, dir)
	if ro, why := l.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	newRun(t, l, "team-00000001")
	if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
		t.Fatalf("the database is not where the directory is: %v", err)
	}
	l.Close()
	again := openLedger(t, dir)
	if r, ok, err := again.Run(context.Background(), "team-00000001"); err != nil || !ok || r.Revision != 1 {
		t.Fatalf("after reopen: %+v %v %v", r, ok, err)
	}
}

// Reads racing Close get an error, never a panic.
func TestReadsDuringCloseAreControlled(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	newRun(t, l, "team-00000001")
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if _, _, err := l.Receipt(ctx, "team-00000001", "admin", "c1"); err != nil && !errors.Is(err, ErrClosed) && !strings.Contains(err.Error(), "closed") {
					t.Errorf("a read during close: %v", err)
					return
				}
				l.Runs(ctx)
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := l.Runs(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("a read after close: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000001", func(*Tx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("a write after close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
}

// A command accepted on a run already stopped is concluded uncertain at
// the next start, the run's state kept; a second recovery writes nothing.
func TestRecoverConcludesTheOpenRowsOfAStoppedRun(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		r, _, _ := tx.Run()
		r.State, r.ClosedAt = RunStopped, tx.Now()
		if err := tx.PutRun(r); err != nil {
			return err
		}
		_, _, err := tx.Command("alice", "c1", "run.stop", "fp")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := l.Recover(ctx)
	if err != nil || len(rec) != 1 || rec[0].Interrupted || rec[0].Commands != 1 {
		t.Fatalf("%+v %v", rec, err)
	}
	if r, _, _ := l.Run(ctx, "team-00000001"); r.State != RunStopped {
		t.Fatalf("the stopped run's state moved: %+v", r)
	}
	if c, _, _ := l.Receipt(ctx, "team-00000001", "alice", "c1"); c.Status != CommandUncertain {
		t.Fatalf("c1 %+v", c)
	}
	if rec, err := l.Recover(ctx); err != nil || len(rec) != 0 {
		t.Fatalf("second recover: %+v %v", rec, err)
	}
}

// A run's logs are bounded in all: a log that would pass MaxRunLogs is not
// kept, its hash and tail are; a row's own log, replaced, counts no more.
func TestARunsLogsAreBoundedInAll(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	newRun(t, l, "team-00000001")
	// The budget scaled down: 64 logs of a KiB fill it.
	const unit = 1 << 10
	old := maxRunLogs
	maxRunLogs = 64 * unit
	t.Cleanup(func() { maxRunLogs = old })
	put := func(id string, k int, size int) CheckRow {
		var got CheckRow
		if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
			if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: "build", Member: "lead", Kind: "work", State: TaskVerifying}); err != nil {
				return err
			}
			if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptVerifying, ReservedAt: tx.Now()}); err != nil {
				return err
			}
			return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: id, K: k, Argv: []string{"true"}, State: CheckFinished, StartedAt: tx.Now(), Log: make([]byte, size), LogSHA256: "abc", Tail: "tail"})
		}); err != nil {
			t.Fatal(err)
		}
		checks, _ := l.Checks(ctx, "team-00000001", "build", 1, true)
		for _, c := range checks {
			if c.ID == id && c.K == k {
				got = c
			}
		}
		return got
	}
	// 64 logs of a unit fill the budget; the 65th is dropped, hash and tail kept.
	for i := range 64 {
		if c := put(fmt.Sprintf("c%02d", i), 1, unit); c.LogDropped || len(c.Log) != unit {
			t.Fatalf("log %d: dropped %v, %d bytes", i, c.LogDropped, len(c.Log))
		}
	}
	if c := put("c64", 1, unit); !c.LogDropped || c.Log != nil || c.LogSHA256 != "abc" || c.Tail != "tail" {
		t.Fatalf("the 65th: %+v", c)
	}
	// A row's own log replaced counts no more: the same row again fits.
	if c := put("c00", 1, unit); c.LogDropped || len(c.Log) != unit {
		t.Fatalf("a replaced log: dropped %v, %d bytes", c.LogDropped, len(c.Log))
	}
	// One byte more over the budget is dropped, one byte less kept.
	if c := put("c65", 1, 1); !c.LogDropped {
		t.Fatal("a byte past the budget was kept")
	}
	if c := put("c00", 1, unit-1); c.LogDropped {
		t.Fatal("a log within the budget was dropped")
	}
	if c := put("c65", 2, 1); c.LogDropped || len(c.Log) != 1 {
		t.Fatalf("the freed byte: %+v", c)
	}
	// A row marked dropped stores no bytes, whatever it carries.
	if _, err := l.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutCheck(CheckRow{RunID: "team-00000001", TaskID: "build", N: 1, ID: "c64", K: 1, Argv: []string{"true"}, State: CheckFinished, StartedAt: tx.Now(), Log: make([]byte, unit), LogDropped: true, LogSHA256: "abc", Tail: "tail"})
	}); err != nil {
		t.Fatal(err)
	}
	checks, _ := l.Checks(ctx, "team-00000001", "build", 1, true)
	var total int
	for _, c := range checks {
		total += len(c.Log)
		if c.ID == "c64" && (!c.LogDropped || c.Log != nil) {
			t.Fatalf("a dropped row with bytes: %+v", c)
		}
	}
	if total > 64*unit {
		t.Fatalf("%d bytes of logs kept, past the budget", total)
	}
}

// A database the first Conductor made (version 1, no result column on the
// attempts) is brought to the version this one writes as it opens.
func TestAVersion1DatabaseIsBroughtToTheVersionThisConductorWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	l := openLedger(t, dir)
	for _, stmt := range append(slices.Clone(version5Added), "ALTER TABLE attempts DROP COLUMN result", "ALTER TABLE attempts DROP COLUMN checkpoint", "ALTER TABLE tasks DROP COLUMN repair", "ALTER TABLE tasks DROP COLUMN extra", "ALTER TABLE tasks DROP COLUMN withdrawn", "DROP TABLE messages") {
		if _, err := l.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.db.Exec("UPDATE schema_version SET version = 1"); err != nil {
		t.Fatal(err)
	}
	l.Close()
	again := openLedger(t, dir)
	if ro, why := again.ReadOnly(); ro {
		t.Fatalf("read-only after the migration: %s", why)
	}
	var version int
	if err := again.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != Version {
		t.Fatalf("version %d %v", version, err)
	}
	ctx := context.Background()
	if _, err := again.Transition(ctx, "team-00000001", func(tx *Tx) error {
		if err := tx.PutRun(RunRow{ID: "team-00000001", CrewID: "team", State: RunRunning, CreatedAt: tx.Now()}); err != nil {
			return err
		}
		if err := tx.PutTask(TaskRow{RunID: "team-00000001", ID: "build", Member: "lead", Kind: "work", State: TaskRunning, Repair: `{"review":"review"}`, Extra: 2}); err != nil {
			return err
		}
		if err := tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 1, Member: "lead", State: AttemptVerifying, ReservedAt: tx.Now(), Result: `{"summary":"built"}`, Checkpoint: `{"done":"half"}`}); err != nil {
			return err
		}
		if err := tx.PutMessage(MessageRow{RunID: "team-00000001", Seq: 1, From: "lead", To: "docs", Text: "hello", CreatedAt: tx.Now()}); err != nil {
			return err
		}
		return tx.Event("result.submitted", nil)
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := again.Attempts(ctx, "team-00000001")
	if err != nil || len(attempts) != 1 || attempts[0].Result != `{"summary":"built"}` || attempts[0].Checkpoint != `{"done":"half"}` {
		t.Fatalf("attempts after the migration: %+v %v", attempts, err)
	}
	if msgs, err := again.Messages(ctx, "team-00000001"); err != nil || len(msgs) != 1 || msgs[0].From != "lead" || msgs[0].To != "docs" || msgs[0].Text != "hello" || !msgs[0].DeliveredAt.IsZero() || !msgs[0].TypingAt.IsZero() {
		t.Fatalf("messages after the migration: %+v %v", msgs, err)
	}
	if tasks, err := again.Tasks(ctx, "team-00000001"); err != nil || len(tasks) != 1 || tasks[0].Repair != `{"review":"review"}` || tasks[0].Extra != 2 {
		t.Fatalf("tasks after the migration: %+v %v", tasks, err)
	}
	if _, err := again.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutMessage(MessageRow{RunID: "team-00000001", Seq: 1, From: "lead", To: "docs", Text: "hello", CreatedAt: tx.Now(), TypingAt: tx.Now(), DeliveredAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := again.Messages(ctx, "team-00000001"); msgs[0].DeliveredAt.IsZero() || msgs[0].TypingAt.IsZero() {
		t.Fatalf("the delivery was not kept: %+v", msgs)
	}
	if _, err := again.Transition(ctx, "team-00000001", func(tx *Tx) error {
		return tx.PutAttempt(AttemptRow{RunID: "team-00000001", TaskID: "build", N: 2, Member: "lead", State: AttemptReserved, ReservedAt: tx.Now(), Result: strings.Repeat("x", MaxResult+1)})
	}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a result over the bound: %v", err)
	}
}

// A continuation names the run it continues once: the old run's row takes
// continued_by in the new run's transition, and a second continuation, or
// one of a run with no row, is refused.
func TestAContinuationIsNamedOnce(t *testing.T) {
	l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
	ctx := context.Background()
	for _, id := range []string{"team-00000001", "team-00000002", "team-00000003"} {
		if _, err := l.Transition(ctx, id, func(tx *Tx) error {
			return tx.PutRun(RunRow{ID: id, CrewID: "team", Crew: "{}", State: RunRunning, CreatedAt: tx.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Transition(ctx, "team-00000002", func(tx *Tx) error { return tx.Continued("team-00000001") }); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := l.Run(ctx, "team-00000001"); row.ContinuedBy != "team-00000002" {
		t.Fatalf("continued_by %q", row.ContinuedBy)
	}
	if _, err := l.Transition(ctx, "team-00000003", func(tx *Tx) error { return tx.Continued("team-00000001") }); !errors.Is(err, ErrContinued) {
		t.Fatalf("a second continuation: %v", err)
	}
	if _, err := l.Transition(ctx, "team-00000003", func(tx *Tx) error { return tx.Continued("team-00000009") }); !errors.Is(err, ErrContinued) {
		t.Fatalf("a run with no row: %v", err)
	}
	if row, _, _ := l.Run(ctx, "team-00000001"); row.ContinuedBy != "team-00000002" {
		t.Fatalf("continued_by after the refusals: %q", row.ContinuedBy)
	}
}

// A transaction whose context ends under it is refused with the context's
// own error, whatever database/sql said of it: its rollback of a cancelled
// transaction races the commit, which then says ErrTxDone, and the engine
// must tell a stop from a store that failed (Codex's review of #367).
func TestACancelledTransactionIsRefusedWithItsContextsError(t *testing.T) {
	l := openLedger(t, t.TempDir())
	newRun(t, l, "r")
	for i := range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		l.beforeCommit = func() error {
			cancel()
			return nil
		}
		_, err := l.Transition(ctx, "r", func(*Tx) error { return nil })
		l.beforeCommit = nil
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: a cancelled transaction's error is %v, not the context's", i, err)
		}
	}
	// Cancelled inside fn, before any statement after it: the statements' refusals, whatever database/sql says, are the context's too.
	for i := range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := l.Transition(ctx, "r", func(t *Tx) error {
			cancel()
			return t.Event("x", nil)
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: a transaction cancelled in fn fails with %v, not the context's error", i, err)
		}
	}
	// A real failure stands as itself, the context done or not.
	ctx, cancel := context.WithCancel(context.Background())
	l.beforeCommit = func() error {
		cancel()
		return errors.New("disk full")
	}
	_, err := l.Transition(ctx, "r", func(*Tx) error { return nil })
	l.beforeCommit = nil
	if err == nil || errors.Is(err, context.Canceled) || err.Error() != "disk full" {
		t.Fatalf("a real failure under a done context reads as %v", err)
	}
}
