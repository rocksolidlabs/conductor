//go:build unix

package ledger

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/store"
)

// whyOf checks what a LockView promises of its reasons: Why only while
// owned, and while not owned exactly one of WaitingSince, GaveUpAt,
// NoLocks and Closed, so that a page never draws "held by no server"
// without saying why.
func whyOf(t *testing.T, v LockView) {
	t.Helper()
	if v.At.IsZero() {
		t.Errorf("a view with no moment: %+v", v)
	}
	if v.Owned {
		if !v.WaitingSince.IsZero() || !v.GaveUpAt.IsZero() || v.NoLocks || v.Closed {
			t.Errorf("an owned lock with a reason for not owning it: %+v", v)
		}
		if !v.ReadOnly && v.Why != "" {
			t.Errorf("a writable owner with a why: %+v", v)
		}
		return
	}
	if v.Why != "" {
		t.Errorf("a ledger that does not own the lock gave a why: %+v", v)
	}
	if !v.ReadOnly {
		t.Errorf("a ledger that does not own the lock is writable: %+v", v)
	}
	n := 0
	for _, set := range []bool{!v.WaitingSince.IsZero(), !v.GaveUpAt.IsZero(), v.NoLocks, v.Closed} {
		if set {
			n++
		}
	}
	if n != 1 {
		t.Errorf("not owned, with %d reasons in place of one: %+v", n, v)
	}
}

// triesSeen replaces takeLock with one that tells, on the channel it
// returns, each try once it has been made: the ledger's state is set
// under its lock after the try returns, so a Lock() made after the word
// sees what the try concluded.
func triesSeen(t *testing.T) <-chan struct{} {
	t.Helper()
	tried := make(chan struct{}, 16)
	old := takeLock
	takeLock = func(path string) (*ownerLock, string, bool, error) {
		lock, why, held, err := old(path)
		tried <- struct{}{}
		return lock, why, held, err
	}
	t.Cleanup(func() { takeLock = old })
	return tried
}

func TestLockView(t *testing.T) {
	t.Run("an owner writable", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		v := l.Lock()
		whyOf(t, v)
		if !v.Owned || v.ReadOnly || v.Why != "" {
			t.Fatalf("an owner: %+v", v)
		}
	})
	t.Run("an owner whose database a newer schema wrote", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "runs")
		l := openLedger(t, dir)
		newRun(t, l, "team-00000001")
		if _, err := l.db.Exec("UPDATE schema_version SET version = ?", Version+1); err != nil {
			t.Fatal(err)
		}
		l.Close()
		v := openLedger(t, dir).Lock()
		whyOf(t, v)
		if !v.Owned || !v.ReadOnly || !strings.Contains(v.Why, "newer") {
			t.Fatalf("an owner of a newer database: %+v", v)
		}
	})
	t.Run("a wait that ended", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "runs")
		openLedger(t, dir) // holds the lock throughout
		tried := triesSeen(t)
		ticks := make(chan time.Time)
		l := openWaiting(t, dir, waitSeams{lockWait: time.Nanosecond, ticks: ticks})
		<-tried // Open's
		before := l.Lock()
		whyOf(t, before)
		if before.WaitingSince.IsZero() {
			t.Fatalf("a ledger waiting for the lock: %+v", before)
		}
		ticks <- time.Now() // its try, past its wait
		<-tried
		v := l.Lock()
		whyOf(t, v)
		if v.Owned || v.GaveUpAt.IsZero() || !v.WaitingSince.IsZero() || v.GaveUpAt.Before(before.WaitingSince) {
			t.Fatalf("a wait that ended: %+v", v)
		}
		// ReadOnly keeps its words for its readers; the view quotes no
		// holder.
		if _, why := l.ReadOnly(); !strings.HasPrefix(why, "another server holds") {
			t.Fatalf("ReadOnly's reason: %s", why)
		}
	})
	t.Run("a wait under way, then the lock taken", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "runs")
		owner, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { owner.Close() })
		tried := triesSeen(t)
		ticks := make(chan time.Time)
		l := openWaiting(t, dir, waitSeams{lockWait: time.Hour, ticks: ticks})
		<-tried
		first := l.Lock()
		whyOf(t, first)
		if first.WaitingSince.IsZero() || first.Owned {
			t.Fatalf("a wait under way: %+v", first)
		}
		ticks <- time.Now() // a try that finds the lock held, within the wait
		<-tried
		if v := l.Lock(); !v.WaitingSince.Equal(first.WaitingSince) || !v.GaveUpAt.IsZero() {
			t.Fatalf("a try within the wait changed it: %+v", v)
		}
		owner.Close()
		ticks <- time.Now() // the lock is free now
		<-tried
		v := l.Lock()
		whyOf(t, v)
		if !v.Owned || v.ReadOnly || !v.WaitingSince.IsZero() || !v.GaveUpAt.IsZero() || v.NoLocks {
			t.Fatalf("a lock taken: %+v", v)
		}
	})
	t.Run("a filesystem without locks", func(t *testing.T) {
		old := takeLock
		takeLock = func(path string) (*ownerLock, string, bool, error) {
			return nil, "the filesystem of " + path + " does not support locks (test)", false, nil
		}
		t.Cleanup(func() { takeLock = old })
		v := openLedger(t, filepath.Join(t.TempDir(), "runs")).Lock()
		whyOf(t, v)
		if v.Owned || !v.NoLocks {
			t.Fatalf("no locks: %+v", v)
		}
	})
	t.Run("a lock file that cannot be opened", func(t *testing.T) {
		old := takeLock
		takeLock = func(string) (*ownerLock, string, bool, error) {
			return nil, "", false, fs.ErrPermission
		}
		t.Cleanup(func() { takeLock = old })
		v := openLedger(t, filepath.Join(t.TempDir(), "runs")).Lock()
		whyOf(t, v)
		if v.Owned || v.GaveUpAt.IsZero() {
			t.Fatalf("a lock file that cannot be opened: %+v", v)
		}
	})
	t.Run("closed", func(t *testing.T) {
		l := openLedger(t, filepath.Join(t.TempDir(), "runs"))
		l.Close()
		v := l.Lock()
		whyOf(t, v)
		if v.Owned || !v.Closed {
			t.Fatalf("closed: %+v", v)
		}
	})
}

// dirState is each file under dir with its size, mode and time: what a
// probe must leave as it found it.
func dirState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String()
	}
	return out
}

func sameDir(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s: the directory was %v, is %v", what, before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s: %s was %s, is %s", what, k, v, after[k])
		}
	}
}

var holderLine = regexp.MustCompile(`^pid ([0-9]{1,10}) since (\S+)$`)

func TestProbeStates(t *testing.T) {
	t.Run("held by another process, its line read in the try", func(t *testing.T) {
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
		t.Cleanup(func() {
			stdin.Close()
			helper.Wait()
		})
		sc := bufio.NewScanner(stdout)
		for sc.Scan() && sc.Text() != "held" {
			if strings.HasPrefix(sc.Text(), "error:") {
				t.Fatal(sc.Text())
			}
		}
		before := dirState(t, dir)
		r, err := Probe(dir)
		if err != nil || r.State != ProbeHeld || r.At.IsZero() {
			t.Fatalf("held: %+v %v", r, err)
		}
		m := holderLine.FindStringSubmatch(r.Line)
		if m == nil || m[1] != strconv.Itoa(helper.Process.Pid) {
			t.Fatalf("the holder's line %q, its pid %d", r.Line, helper.Process.Pid)
		}
		if _, err := time.Parse(time.RFC3339, m[2]); err != nil {
			t.Fatalf("the holder's line %q: %v", r.Line, err)
		}
		// The probe took nothing and wrote nothing: the lock is the
		// helper's still, and nothing in the directory, the database
		// among it, changed.
		sameDir(t, "a probe of a held lock", before, dirState(t, dir))
		if lock, _, held, err := store.TakeOwnerLock(filepath.Join(dir, Lock)); lock != nil || !held || err != nil {
			if lock != nil {
				lock.Release()
			}
			t.Fatalf("after the probe the helper no longer holds the lock: %v %v", held, err)
		}
	})
	t.Run("held in this process, the owner writing on", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "runs")
		owner := openLedger(t, dir)
		r, err := Probe(dir)
		if err != nil || r.State != ProbeHeld || !strings.HasPrefix(r.Line, "pid "+strconv.Itoa(os.Getpid())+" since ") {
			t.Fatalf("held: %+v %v", r, err)
		}
		newRun(t, owner, "team-00000001")
		if v := owner.Lock(); !v.Owned || v.ReadOnly {
			t.Fatalf("the owner after a probe: %+v", v)
		}
	})
	t.Run("a holder's line past the bound, or not yet written", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, Lock)
		lock, _, _, err := store.TakeOwnerLock(path)
		if err != nil || lock == nil {
			t.Fatal(err)
		}
		t.Cleanup(lock.Release)
		for _, c := range []struct{ wrote, line string }{
			{"pid 7 since 2026-11-05T13:00:00Z\nmore\n", "pid 7 since 2026-11-05T13:00:00Z"},
			{strings.Repeat("x", ProbeLineMax) + "\n", strings.Repeat("x", ProbeLineMax)},
			{strings.Repeat("x", ProbeLineMax+1) + "\n", ""},
			{"pid 7 since", ""},
			{"", ""},
		} {
			if err := os.WriteFile(path, []byte(c.wrote), 0o600); err != nil {
				t.Fatal(err)
			}
			r, err := Probe(dir)
			if err != nil || r.State != ProbeHeld || r.Line != c.line {
				t.Fatalf("the holder wrote %q: %+v %v, want the line %q", c.wrote, r, err, c.line)
			}
		}
	})
	t.Run("free, and left free", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "runs")
		openLedger(t, dir).Close()
		before := dirState(t, dir)
		r, err := Probe(dir)
		if err != nil || r.State != ProbeFree || r.Line != "" {
			t.Fatalf("free: %+v %v", r, err)
		}
		sameDir(t, "a probe of a free lock", before, dirState(t, dir))
		lock, _, _, err := store.TakeOwnerLock(filepath.Join(dir, Lock))
		if err != nil || lock == nil {
			t.Fatalf("the lock after a probe found it free: %v", err)
		}
		lock.Release()
	})
	t.Run("no lock file: not known, and none made", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "runs")
		if _, err := Probe(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("no directory: %v", err)
		}
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the probe made the directory: %v", err)
		}
		if _, err := Probe(parent); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("no lock file: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(parent, Lock)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the probe made the lock file: %v", err)
		}
	})
	t.Run("a lock file that cannot be opened", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root opens any file")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, Lock)
		if err := os.WriteFile(path, nil, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := Probe(dir); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("a lock file it may not read: %v", err)
		}
	})
	t.Run("no locks, through the seam", func(t *testing.T) {
		old := probeLock
		probeLock = func(string) (bool, string, bool, error) { return false, "", true, nil }
		t.Cleanup(func() { probeLock = old })
		r, err := Probe(t.TempDir())
		if err != nil || r.State != ProbeNoLocks || r.Line != "" {
			t.Fatalf("no locks: %+v %v", r, err)
		}
	})
}

// A probe that finds the lock free holds it for the moment of its try. A
// ledger of this process waiting for that lock must not lose its try to
// that moment: at the last try of its wait it would give up on a lock
// nobody keeps, and the server would stay read-only. The probe's moment
// is held open here (the seam takes the lock as a probe's try does) while
// the waiting ledger's try, whose wait has ended, is started; the try
// waits its turn (lockTries, whose waiters the test counts) and takes the
// lock once the probe has let it go.
func TestAProbeCostsAWaitingLedgerNoTry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	owner, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	tried := triesSeen(t)
	ticks := make(chan time.Time)
	w := openWaiting(t, dir, waitSeams{lockWait: time.Nanosecond, ticks: ticks})
	<-tried // Open's, held by the owner
	owner.Close()

	madeBeside := false
	old := probeLock
	probeLock = func(path string) (bool, string, bool, error) {
		lock, _, _, err := store.TakeOwnerLock(path)
		if err != nil || lock == nil {
			t.Errorf("the probe's moment: the lock is not free: %v", err)
			return false, "", false, err
		}
		defer lock.Release()
		sent := make(chan struct{})
		go func() {
			ticks <- time.Now() // the waiting ledger's try, now
			close(sent)
		}()
		<-sent
		// Until the try is held behind the probe, or made.
		deadline := time.Now().Add(time.Minute)
		for lockTries.waiting.Load() == 0 {
			select {
			case <-tried:
				madeBeside = true // the test fails below
				return false, "", false, nil
			default:
			}
			if time.Now().After(deadline) {
				t.Errorf("the waiting ledger's try neither waited nor was made")
				return false, "", false, nil
			}
			runtime.Gosched()
		}
		return false, "", false, nil
	}
	t.Cleanup(func() { probeLock = old })
	if _, err := Probe(dir); err != nil {
		t.Fatal(err)
	}
	probeLock = old
	if !madeBeside {
		select {
		case <-tried:
		case <-time.After(time.Minute):
			t.Fatal("the waiting ledger made no try")
		}
	}
	if v := w.Lock(); !v.Owned || v.ReadOnly {
		t.Fatalf("a probe's moment cost the waiting ledger its try: %+v", v)
	}
}
