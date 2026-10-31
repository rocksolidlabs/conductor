//go:build unix

package proc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// serve starts a shell script as a service in dir: the tests' commands,
// never the product's, which takes argv arrays alone. The service is
// killed when the test ends, whatever the test did.
func serve(t *testing.T, dir, script string, opts ...func(*Spec)) *Service {
	t.Helper()
	s := Spec{Argv: []string{"sh", "-c", script}, Dir: dir}
	for _, o := range opts {
		o(&s)
	}
	svc, err := Serve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		svc.Stop(ctx)
	})
	return svc
}

// pidFile waits up to 10 s for the file at path to hold a pid, and
// returns it: a service says it runs by writing its pid there.
func pidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s after 10 s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// shortGrace makes Stop's grace d for the test.
func shortGrace(t *testing.T, d time.Duration) {
	old := serveGrace
	serveGrace = d
	t.Cleanup(func() { serveGrace = old })
}

// sent is a signal lifecycle.signal sent (signalled): which, and whether
// to the process alone.
type sent struct {
	s     sig
	alone bool
}

// watchSignals records every signal sent from now on, each told to each
// when given as it is sent, until the test ends: called before the
// service starts, so every goroutine that sends starts after the seam is
// set, and before serve, so the seam is cleared after the service's last
// stop. It returns what was sent so far.
func watchSignals(t *testing.T, each func(sent)) func() []sent {
	var mu sync.Mutex
	var all []sent
	signalled = func(s sig, alone bool) {
		mu.Lock()
		all = append(all, sent{s, alone})
		mu.Unlock()
		if each != nil {
			each(sent{s, alone})
		}
	}
	t.Cleanup(func() { signalled = nil })
	return func() []sent {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(all)
	}
}

// count is how many of sends were x.
func count(sends []sent, x sent) int {
	n := 0
	for _, y := range sends {
		if y == x {
			n++
		}
	}
	return n
}

// appears waits up to within for a file at path, and says whether it came.
func appears(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// A stop's SIGTERM reaches the service's first process once (Codex's
// review of #441): in its group, it has the group's and is sent no
// second, which, come after its handler had put the default back while it
// cleans up, would end it before its grace. Its handler here puts the
// default back first, then cleans up until a gate opens, and exits 0. The
// group's SIGTERM is held at the seam until the handler has run, the gate
// opened then, so a second SIGTERM would come while it cleans up, as one
// can when the process runs between two sends. A second Stop beside the
// first sends none either. A first process that left its group (it joined
// its parent's) is sent its own, and ends at it before the grace.
func TestAStopsSIGTERMReachesTheServiceOnce(t *testing.T) {
	const cleans = `trap 'trap - TERM; echo $$ > trapped; while [ ! -e gate ]; do sleep 0.05; done; echo done > done; exit 0' TERM; echo $$ > pid; while :; do sleep 0.05; done`
	t.Run("its first process, in its group", func(t *testing.T) {
		shortGrace(t, time.Hour)
		dir := t.TempDir()
		var held atomic.Bool
		sends := watchSignals(t, func(x sent) {
			if x != (sent{sigTerm, false}) || held.Swap(true) {
				return
			}
			// The handler has run, the default back, before anything more
			// is sent; then its cleanup may finish.
			if !appears(filepath.Join(dir, "trapped"), 10*time.Second) {
				t.Errorf("the service's handler did not run within 10 s of the group's SIGTERM")
			}
			if err := os.WriteFile(filepath.Join(dir, "gate"), nil, 0o600); err != nil {
				t.Error(err)
			}
		})
		svc := serve(t, dir, cleans)
		pidFile(t, filepath.Join(dir, "pid"))
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		res := svc.Result()
		if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" || res.Exit != 0 || res.Signal != "" || res.Killed {
			t.Fatalf("the service was ended before its cleanup finished (%q %v): %+v; sent %v", b, err, res, sends())
		}
		if got := sends(); count(got, sent{sigTerm, false}) != 1 || count(got, sent{sigTerm, true}) != 0 {
			t.Fatalf("sent %v: the stop's one SIGTERM is the group's", got)
		}
	})
	t.Run("a second Stop beside the first", func(t *testing.T) {
		shortGrace(t, time.Hour)
		dir := t.TempDir()
		sends := watchSignals(t, nil)
		svc := serve(t, dir, cleans)
		pidFile(t, filepath.Join(dir, "pid"))
		first := make(chan error, 1)
		go func() { first <- svc.Stop(context.Background()) }()
		// The first's SIGTERM handled, the default back: the second, its
		// context ended, kills the service, and asks nothing more of it.
		pidFile(t, filepath.Join(dir, "trapped"))
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		if err := svc.Stop(ended); err != nil {
			t.Fatal(err)
		}
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if got := sends(); count(got, sent{sigTerm, false}) != 1 || count(got, sent{sigTerm, true}) != 0 {
			t.Fatalf("sent %v: two Stops sent more than the group's one SIGTERM", got)
		}
		if res := svc.Result(); res.Signal != "killed" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("its first process, out of its group", func(t *testing.T) {
		shortGrace(t, time.Hour)
		dir := t.TempDir()
		sends := watchSignals(t, nil)
		svc, err := Serve(context.Background(), Spec{Argv: []string{os.Args[0], "-test.run=^TestHelperChangesGroup$"}, Dir: dir, Env: map[string]string{"PROC_HELPER": "setpgid"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			svc.Stop(ctx)
		})
		pid := svc.Result().Process.PID
		deadline := time.Now().Add(10 * time.Second)
		for {
			if pgid, err := syscall.Getpgid(pid); err == nil && pgid != pid {
				break // it has joined another group
			}
			if time.Now().After(deadline) {
				t.Fatal("the helper did not leave its group within 10 s")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Its own SIGTERM ended it: neither the grace's SIGKILL, an hour
		// away, nor its own exit 0 after 30 s.
		if res := svc.Result(); res.Signal != "terminated" {
			t.Fatalf("%+v; sent %v", res, sends())
		}
		if got := sends(); count(got, sent{sigTerm, false}) != 1 || count(got, sent{sigTerm, true}) != 1 {
			t.Fatalf("sent %v: the process out of its group is sent one SIGTERM of its own", got)
		}
	})
}

// A service runs until it exits: Serve returns once the process exists,
// Started told its pid, group and start before; Done closes at its own
// exit, and Result says what became of it, its output's head and the
// process Started was told.
func TestAServiceRunsUntilItExits(t *testing.T) {
	dir := t.TempDir()
	gate, pid := filepath.Join(dir, "gate"), filepath.Join(dir, "pid")
	var started Process
	svc := serve(t, dir, `echo $$ > pid; echo serving; while [ ! -e gate ]; do sleep 0.05; done; echo leaving; exit 3`, func(s *Spec) {
		s.Started = func(p Process) { started = p }
	})
	if started.PID == 0 || started.Group != started.PID || started.StartedAt.IsZero() {
		t.Fatalf("Started was told %+v", started)
	}
	if got := pidFile(t, pid); got != started.PID {
		t.Fatalf("the service is %d, Started was told %d", got, started.PID)
	}
	select {
	case <-svc.Done():
		t.Fatal("the service is done before it exited")
	default:
	}
	if res := svc.Result(); res.Exit != -1 || res.Process != started {
		t.Fatalf("the result while it runs: %+v", res)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-svc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the service did not end after 10 s")
	}
	res := svc.Result()
	if res.Exit != 3 || res.Signal != "" || res.Err != "" || res.Canceled || res.TimedOut || string(res.Head) != "serving\nleaving\n" || res.Process != started || res.Duration <= 0 {
		t.Fatalf("%+v", res)
	}
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("a stop after the end: %v", err)
	}
}

// Stop asks the service's group to end (SIGTERM) and kills it (SIGKILL)
// once the grace has passed: a service that ignores SIGTERM is killed
// after the grace, its child in the group with it; one that ends on
// SIGTERM ends before it, as its trap says. Stop returns once the process
// is reaped.
func TestStopEndsAServiceAndItsGroup(t *testing.T) {
	t.Run("one that ignores SIGTERM", func(t *testing.T) {
		const grace = 300 * time.Millisecond
		shortGrace(t, grace)
		dir := t.TempDir()
		svc := serve(t, dir, `trap "" TERM; sleep 300 & echo $! > child; echo $$ > pid; wait`)
		pid, child := pidFile(t, filepath.Join(dir, "pid")), pidFile(t, filepath.Join(dir, "child"))
		begin := time.Now()
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(begin); took < grace {
			t.Fatalf("Stop returned after %v, before the grace of %v", took, grace)
		}
		res := svc.Result()
		if res.Signal != "killed" || res.Exit != -1 || res.Canceled {
			t.Fatalf("%+v", res)
		}
		for _, p := range []int{pid, child} {
			if !gone(t, p, 3*time.Second) {
				t.Fatalf("%d outlived the stop", p)
			}
		}
	})
	t.Run("one that ends on SIGTERM", func(t *testing.T) {
		shortGrace(t, time.Hour)
		dir := t.TempDir()
		svc := serve(t, dir, `trap 'echo stopping; exit 0' TERM; sleep 300 & echo $! > child; echo $$ > pid; wait`)
		pid, child := pidFile(t, filepath.Join(dir, "pid")), pidFile(t, filepath.Join(dir, "child"))
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		res := svc.Result()
		if res.Exit != 0 || res.Signal != "" || !strings.Contains(string(res.Head), "stopping") {
			t.Fatalf("not ended by its trap: %+v", res)
		}
		for _, p := range []int{pid, child} {
			if !gone(t, p, 3*time.Second) {
				t.Fatalf("%d outlived the stop", p)
			}
		}
	})
	t.Run("a context that ends", func(t *testing.T) {
		shortGrace(t, time.Hour)
		dir := t.TempDir()
		svc := serve(t, dir, `trap "" TERM; echo $$ > pid; while :; do sleep 0.05; done`)
		pid := pidFile(t, filepath.Join(dir, "pid"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := svc.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if res := svc.Result(); res.Signal != "killed" {
			t.Fatalf("%+v", res)
		}
		if !gone(t, pid, 3*time.Second) {
			t.Fatalf("%d outlived the stop", pid)
		}
	})
}

// A stop's grace is the group's, not its first process's alone: a first
// process that ends on SIGTERM before what it started (a wrapper's shell)
// leaves the rest of the group the grace. A member that takes a moment to
// shut down finishes it, whether it holds the service's output or
// redirected its own, and Stop returns once it has ended, the grace an
// hour; one that ignores SIGTERM is killed when the grace ends, not when
// its first process did.
func TestStopGivesTheGraceToTheGroupNotItsFirstProcess(t *testing.T) {
	// The member, a shell the first one starts in the group: asked to end,
	// it takes a second, then says it is done.
	const slow = `trap 'sleep 1; echo done > done; exit 0' TERM; echo $$ > child; while :; do sleep 0.05; done`
	for _, tc := range []struct{ name, out string }{{"holding the service's output", ""}, {"its output redirected", " >/dev/null 2>&1"}} {
		t.Run("a member "+tc.name+" finishes its shutdown", func(t *testing.T) {
			shortGrace(t, time.Hour)
			dir := t.TempDir()
			svc := serve(t, dir, `sh -c "$1"`+tc.out+` & echo $$ > pid; wait`, func(s *Spec) { s.Argv = append(s.Argv, "sh", slow) })
			pid, child := pidFile(t, filepath.Join(dir, "pid")), pidFile(t, filepath.Join(dir, "child"))
			if err := svc.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" {
				t.Fatalf("the member was killed before it finished its shutdown: %q %v", b, err)
			}
			if res := svc.Result(); res.Signal != "terminated" || res.Exit != -1 || res.Killed {
				t.Fatalf("%+v", res)
			}
			for _, p := range []int{pid, child} {
				if !gone(t, p, 3*time.Second) {
					t.Fatalf("%d outlived the stop", p)
				}
			}
		})
	}
	t.Run("a member that ignores SIGTERM is killed at the grace", func(t *testing.T) {
		const grace = time.Second
		shortGrace(t, grace)
		dir := t.TempDir()
		svc := serve(t, dir, `sh -c "$1" & echo $$ > pid; wait`, func(s *Spec) {
			s.Argv = append(s.Argv, "sh", `trap "" TERM; echo $$ > child; while :; do sleep 0.05; done`)
		})
		pidFile(t, filepath.Join(dir, "pid"))
		child := pidFile(t, filepath.Join(dir, "child"))
		begin := time.Now()
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(begin); took < grace {
			t.Fatalf("Stop returned after %v, before the grace of %v", took, grace)
		}
		if res := svc.Result(); res.Signal != "terminated" {
			t.Fatalf("%+v", res)
		}
		if !gone(t, child, 3*time.Second) {
			t.Fatalf("%d outlived the stop", child)
		}
	})
}

// lateOutput holds the output's end back (outputRead) until settle has
// said what it saw of the output (settled), for one process the test
// starts: the end comes after the judgement, as it can on a loaded
// machine, where the goroutine that reads the output is late to its end.
// held is what settle said; late, that it had not said within 10 s of the
// output's last byte. Set before the process starts.
func lateOutput(t *testing.T) (held, late func() bool) {
	said := make(chan struct{})
	var h, l atomic.Bool
	settled = func(v bool) {
		h.Store(v)
		close(said)
	}
	outputRead = func() {
		select {
		case <-said:
		case <-time.After(10 * time.Second):
			l.Store(true)
		}
	}
	t.Cleanup(func() { settled, outputRead = nil, nil })
	return h.Load, l.Load
}

// lookAt sets looking for the test: each look at the group (numbered from
// 1, a listing or a follow) is told to each, whose answer makes a listing
// fail. Set before the process starts.
func lookAt(t *testing.T, each func(n int, listing bool) (fail bool)) {
	looking = each
	t.Cleanup(func() { looking = nil })
}

// leftGroup waits up to 10 s until none of pids is a running process of
// the group pgid (a zombie is none), and says whether it came to that.
func leftGroup(pgid int, pids ...int) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(stillMembers(pgid, pids)) == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// An output still open once the process has exited and holdGrace has
// passed is held by a member of the group only while the group has one
// (Run, and a service that exits on its own): with none of it left, the
// output's end is only read late, as it can be on a loaded machine, and
// nothing was killed that held it. A child left in the group that holds
// the output is still found, and goes with the group (Killed). One left
// that redirected its output goes with the group too, but held none of it
// (Codex's review of #511, round 3): the output's end read late is no
// writer.
func TestAnOutputReadLateIsHeldOnlyByWhatIsLeftInTheGroup(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		child, holds bool
	}{
		{"none of the group left", "echo hi; exit 0", false, false},
		{"a child holding the output", "sleep 30 & echo $!; exit 0", true, true},
		{"a child that redirected its output", "sleep 30 >/dev/null 2>&1 & echo $!; exit 0", true, false},
	} {
		t.Run("a run, "+tc.name, func(t *testing.T) {
			held, late := lateOutput(t)
			res := sh(t, time.Minute, tc.script)
			if late() {
				t.Fatal("settle had not returned 10 s after the output's last byte was read")
			}
			if held() != tc.holds || res.Killed != tc.holds || res.Exit != 0 || res.Err != "" || res.TimedOut {
				t.Fatalf("held %v: %+v", held(), res)
			}
			if tc.child {
				if pid := pidIn(t, res.Head); !gone(t, pid, 3*time.Second) {
					t.Fatalf("the sleep %d outlived the run", pid)
				}
			}
		})
	}
	t.Run("a service that exits on its own, none of the group left", func(t *testing.T) {
		held, late := lateOutput(t)
		svc := serve(t, t.TempDir(), "echo hi; exit 0")
		<-svc.Done()
		if late() {
			t.Fatal("settle had not returned 10 s after the output's last byte was read")
		}
		if res := svc.Result(); held() || res.Killed || res.Exit != 0 || res.Err != "" || string(res.Head) != "hi\n" {
			t.Fatalf("held %v: %+v", held(), res)
		}
	})
}

// The output's end may be read after the last of a stopped group is gone:
// on a loaded machine the group's listing, which reads every process's
// stat, can outlast a member's shutdown, and the goroutine that reads the
// output can be late to its end (#281: a member that had finished its
// shutdown, the output closed, was read as one that held it, Killed). With
// none of the group left, nothing of it held the output, whenever its end
// is read. The end is held back (lateOutput) until settle has said what it
// saw, so it comes after the group is known gone. The looks at the group
// are ordered against the member's end (lookAt): a member the first
// listing finds, its shutdown held until the wait follows it, and a
// member gone, with all it started, before the first listing.
func TestAStoppedGroupsOutputReadLateIsNotHeld(t *testing.T) {
	for _, tc := range []struct {
		name, gate string
		found      bool
	}{{"a member the listing finds, which ends after it", "while [ ! -e go ]; do sleep 0.01; done; ", true}, {"a member gone before the listing", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			shortGrace(t, time.Hour)
			held, late := lateOutput(t)
			dir := t.TempDir()
			var pgid atomic.Int64
			var followed, emptied atomic.Bool
			lookAt(t, func(n int, listing bool) bool {
				switch {
				case n == 1 && !tc.found:
					emptied.Store(leftGroup(int(pgid.Load()), mustMembers(int(pgid.Load()))...))
				case !listing && followed.CompareAndSwap(false, true) && tc.found:
					os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
				}
				return false
			})
			member := `trap '` + tc.gate + `echo done > done; exit 0' TERM; echo $$ > child; while :; do sleep 0.05; done`
			svc := serve(t, dir, `sh -c "$1" & echo $$ > pid; wait`, func(s *Spec) { s.Argv = append(s.Argv, "sh", member) })
			pid, child := pidFile(t, filepath.Join(dir, "pid")), pidFile(t, filepath.Join(dir, "child"))
			pgid.Store(int64(pid))
			if err := svc.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if late() {
				t.Fatal("settle had not returned 10 s after the output's last byte was read")
			}
			// The path taken: the first listing found the member and the
			// wait followed it, or it found none and the wait did not.
			if followed.Load() != tc.found || (!tc.found && !emptied.Load()) {
				t.Fatalf("followed %v, the group emptied before the listing %v", followed.Load(), emptied.Load())
			}
			if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" {
				t.Fatalf("the member was killed before it finished its shutdown: %q %v", b, err)
			}
			if res := svc.Result(); held() || res.Signal != "terminated" || res.Exit != -1 || res.Killed || res.Err != "" {
				t.Fatalf("held %v: %+v", held(), res)
			}
			for _, p := range []int{pid, child} {
				if !gone(t, p, 3*time.Second) {
					t.Fatalf("%d outlived the stop", p)
				}
			}
		})
	}
}

// mustMembers is the running processes of the group pgid, the first
// process's own left out; none when the group cannot be listed.
func mustMembers(pgid int) []int {
	pids, _ := groupMembers(pgid, pgid)
	return pids
}

// closes waits up to 10 s for the output's end of file (launched.eof) of
// the service svc will hold once Serve has returned, and says whether it
// came.
func closes(svc *atomic.Pointer[Service]) bool {
	deadline := time.Now().Add(10 * time.Second)
	for svc.Load() == nil {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-svc.Load().l.eof:
		return true
	case <-time.After(time.Until(deadline)):
		return false
	}
}

// The output's end, come before the end's judgement or held back past it
// (lateOutput), for the tests of a kill beside the wait: the judgement is
// the same either way.
var outputEnds = []struct {
	name string
	late bool
}{{"the output's end before the judgement", false}, {"the output's end after it", true}}

// A stop's kill runs beside the wait for the group (Codex's review of
// #511): the grace's end, or the stop's context, kills a member that holds
// the output between two of the wait's looks, so the next finds the group
// empty and the output closed with the member. What the kill took held
// the output (Killed), whether the output's end comes before the
// judgement or after it. The stop's context is ended at the wait's first
// follow of the member, which ignores SIGTERM, and the follow waits until
// the kill has taken it, and, in the first order, the output has closed.
func TestAStopsKillBesideTheWaitIsReadAsItWasSent(t *testing.T) {
	for _, end := range outputEnds {
		t.Run(end.name, func(t *testing.T) {
			shortGrace(t, time.Hour)
			if end.late {
				lateOutput(t)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var pgid, member atomic.Int64
			var svcp atomic.Pointer[Service]
			var followed, taken, closed atomic.Bool
			lookAt(t, func(n int, listing bool) bool {
				if !listing && followed.CompareAndSwap(false, true) {
					cancel()
					taken.Store(leftGroup(int(pgid.Load()), int(member.Load())))
					closed.Store(end.late || closes(&svcp))
				}
				return false
			})
			dir := t.TempDir()
			svc := serve(t, dir, `sh -c "$1" & echo $$ > pid; wait`, func(s *Spec) { s.Argv = append(s.Argv, "sh", `trap "" TERM; echo $$ > child; exec sleep 3600`) })
			svcp.Store(svc)
			pgid.Store(int64(pidFile(t, filepath.Join(dir, "pid"))))
			member.Store(int64(pidFile(t, filepath.Join(dir, "child"))))
			if err := svc.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if !followed.Load() || !taken.Load() || !closed.Load() {
				t.Fatalf("followed %v, the member taken by the stop's kill before the follow %v, the output closed %v", followed.Load(), taken.Load(), closed.Load())
			}
			if res := svc.Result(); res.Signal != "terminated" || res.Exit != -1 || !res.Killed || res.Err != "" {
				t.Fatalf("%+v", res)
			}
		})
	}
}

// A kill beside the wait at a service's own exit (settle's path, as a
// Run's) is read as it was sent too (Codex's review of #511, round 2):
// the service's context, or a stop come while the wait looks, kills the
// child it left holding the output before the group is listed, and the
// listing finds none of it. The child went with the group (Killed),
// whether the output's end comes before the judgement or after it. The
// kill is sent at the listing's look, which waits until it has taken the
// child, and, in the first order, until the output has closed. The child
// ignores SIGTERM, so the stop's SIGTERM does not end it before its kill.
func TestAKillBesideTheWaitAtAnExitIsReadAsItWasSent(t *testing.T) {
	for _, by := range []string{"the service's context", "a stop"} {
		for _, end := range outputEnds {
			t.Run(by+", "+end.name, func(t *testing.T) {
				shortGrace(t, time.Hour)
				if end.late {
					lateOutput(t)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stopped, stop := context.WithCancel(context.Background())
				stop()
				dir := t.TempDir()
				var pgid atomic.Int64
				var svcp atomic.Pointer[Service]
				var listed, taken, closed atomic.Bool
				stops := make(chan error, 1)
				// Serve's end runs before Serve returns: the look waits
				// until the service and its group are published.
				published := make(chan struct{})
				lookAt(t, func(n int, listing bool) bool {
					if n != 1 || !listing {
						return false
					}
					<-published
					listed.Store(true)
					if by == "a stop" {
						go func() { stops <- svcp.Load().Stop(stopped) }()
					} else {
						cancel()
					}
					b, err := os.ReadFile(filepath.Join(dir, "child"))
					child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
					taken.Store(err == nil && child > 0 && leftGroup(int(pgid.Load()), child))
					closed.Store(end.late || closes(&svcp))
					return false
				})
				svc, err := Serve(ctx, Spec{Argv: []string{"sh", "-c", `sh -c "$1" & echo $! > child; while [ ! -e ready ]; do sleep 0.01; done; exit 0`, "sh", `trap "" TERM; echo > ready; exec sleep 3600`}, Dir: dir})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { svc.Stop(stopped) })
				pgid.Store(int64(svc.Result().Process.PID))
				svcp.Store(svc)
				close(published)
				select {
				case <-svc.Done():
				case <-time.After(30 * time.Second):
					t.Fatal("the service had not ended 30 s after its exit")
				}
				if by == "a stop" {
					if err := <-stops; err != nil {
						t.Fatal(err)
					}
				}
				if !listed.Load() || !taken.Load() || !closed.Load() {
					t.Fatalf("listed %v, the child taken by the kill before the listing %v, the output closed %v", listed.Load(), taken.Load(), closed.Load())
				}
				// exec reports a command its context's end cancelled, which
				// exited 0 all the same, with the context's error.
				res, why := svc.Result(), ""
				if by != "a stop" {
					why = "context canceled"
				}
				if res.Exit != 0 || res.Signal != "" || !res.Killed || res.Err != why || res.Canceled != (by != "a stop") {
					t.Fatalf("%+v", res)
				}
			})
		}
	}
}

// A kill sent once the last of a stopped group is gone takes nothing
// (Codex's review of #511, round 2): the member that held the output has
// finished its shutdown, and its output's end is read late (lateOutput),
// so the output is still open when the stop's context ends and its kill is
// sent, before the group is listed again; the listing finds none of it.
// Nothing the kill took held the output, so the result is not Killed. The
// member's shutdown is held until the wait follows it, the follow waits
// until it is gone, and the stop's context is ended at the listing after
// it, which waits until the kill is sent.
func TestAKillOnceTheGroupIsGoneTakesNothing(t *testing.T) {
	shortGrace(t, time.Hour)
	held, late := lateOutput(t)
	var killed atomic.Bool
	watchSignals(t, func(x sent) {
		if x.s == sigKill {
			killed.Store(true)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	var pgid, member atomic.Int64
	var followed, left, sawKill atomic.Bool
	lookAt(t, func(n int, listing bool) bool {
		switch {
		case !listing && followed.CompareAndSwap(false, true):
			os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
			left.Store(leftGroup(int(pgid.Load()), int(member.Load())))
		case listing && n > 1 && followed.Load():
			cancel()
			deadline := time.Now().Add(10 * time.Second)
			for !killed.Load() && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			sawKill.Store(killed.Load())
		}
		return false
	})
	script := `trap 'while [ ! -e go ]; do sleep 0.01; done; echo done > done; exit 0' TERM; echo $$ > child; while :; do sleep 0.05; done`
	svc := serve(t, dir, `sh -c "$1" & echo $$ > pid; wait`, func(s *Spec) { s.Argv = append(s.Argv, "sh", script) })
	pgid.Store(int64(pidFile(t, filepath.Join(dir, "pid"))))
	member.Store(int64(pidFile(t, filepath.Join(dir, "child"))))
	if err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if late() {
		t.Fatal("settle had not returned 10 s after the output's last byte was read")
	}
	if !followed.Load() || !left.Load() || !sawKill.Load() {
		t.Fatalf("followed %v, the member gone %v, the kill sent before the listing %v", followed.Load(), left.Load(), sawKill.Load())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" {
		t.Fatalf("the member was killed before it finished its shutdown: %q %v", b, err)
	}
	if res := svc.Result(); held() || res.Signal != "terminated" || res.Exit != -1 || res.Killed || res.Err != "" {
		t.Fatalf("held %v: %+v", held(), res)
	}
}

// A kill's own listing of the group is slow on a loaded machine (Codex's
// review of #511, round 3): the child that held the output can finish
// during it, the output closed, while a child that redirected its output
// is still in the group, so the listing finds a member. The kill took no
// holder, so the result is not Killed. The context is ended at the end's
// listing, once holdGrace has passed with the holder running; the kill's
// listing (killScanned) lets the holder finish and waits for the output's
// end; the end's listing waits until the kill is sent.
func TestAKillWhoseListingOutlastsTheHolderTakesNoHolder(t *testing.T) {
	var killed atomic.Bool
	watchSignals(t, func(x sent) {
		if x.s == sigKill && !x.alone {
			killed.Store(true)
		}
	})
	dir := t.TempDir()
	var scanned, closed, listed, sawKill atomic.Bool
	killScanned = func(g *lifecycle) {
		if !scanned.CompareAndSwap(false, true) {
			return
		}
		os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
		select {
		case <-g.output:
			closed.Store(true)
		case <-time.After(10 * time.Second):
		}
	}
	t.Cleanup(func() { killScanned = nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lookAt(t, func(n int, listing bool) bool {
		if n != 1 || !listing {
			return false
		}
		listed.Store(true)
		cancel()
		deadline := time.Now().Add(10 * time.Second)
		for !killed.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		sawKill.Store(killed.Load())
		return false
	})
	script := `sleep 30 >/dev/null 2>&1 & echo $!; sh -c 'while [ ! -e go ]; do sleep 0.01; done; echo done > done' & exit 0`
	res := Run(ctx, Spec{Argv: []string{"sh", "-c", script}, Dir: dir, Timeout: time.Minute})
	if !listed.Load() || !scanned.Load() || !closed.Load() || !sawKill.Load() {
		t.Fatalf("listed %v, the kill's listing made %v, the output closed during it %v, the kill sent before the end's listing %v", listed.Load(), scanned.Load(), closed.Load(), sawKill.Load())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" {
		t.Fatalf("the holder was killed before it finished: %q %v", b, err)
	}
	// exec reports a command its context's end cancelled, which exited 0
	// all the same, with the context's error.
	if res.Exit != 0 || res.Signal != "" || res.Killed || !res.Canceled || res.Err != "context canceled" {
		t.Fatalf("%+v", res)
	}
	if pid := pidIn(t, res.Head); !gone(t, pid, 3*time.Second) {
		t.Fatalf("the redirected sleep %d outlived the run", pid)
	}
}

// A listing of the stopped group that fails once its first members are
// gone is no proof the group is gone: a process one of them started as it
// ended may hold the output. The output's close is waited for then, as
// when the first listing fails, and what holds it finishes. The member's
// shutdown is held until the wait follows it, so the first listing finds
// it; it starts a process that holds the output until the wait for the
// output's end has begun (awaitingOutput), then ends; every listing after
// the first fails. The process is still running when the wait begins, so
// a wait that ended at the failed listing would kill it.
func TestAStoppedGroupThatCannotBeListedAgainIsWaitedForByItsOutput(t *testing.T) {
	shortGrace(t, time.Hour)
	dir := t.TempDir()
	var pgid, member atomic.Int64
	var followed, failed, left, awaited, running atomic.Bool
	lookAt(t, func(n int, listing bool) bool {
		switch {
		case listing && n > 1:
			failed.Store(true)
			return true
		case !listing && followed.CompareAndSwap(false, true):
			os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
			left.Store(leftGroup(int(pgid.Load()), int(member.Load())))
		}
		return false
	})
	awaitingOutput = func() {
		_, err := os.Stat(filepath.Join(dir, "done"))
		running.Store(os.IsNotExist(err))
		awaited.Store(true)
		os.WriteFile(filepath.Join(dir, "release"), nil, 0o644)
	}
	t.Cleanup(func() { awaitingOutput = nil })
	script := `trap 'while [ ! -e go ]; do sleep 0.01; done; sh -c "while [ ! -e release ]; do sleep 0.01; done; echo done > done" & exit 0' TERM; echo $$ > child; while :; do sleep 0.05; done`
	svc := serve(t, dir, `sh -c "$1" & echo $$ > pid; wait`, func(s *Spec) { s.Argv = append(s.Argv, "sh", script) })
	pgid.Store(int64(pidFile(t, filepath.Join(dir, "pid"))))
	member.Store(int64(pidFile(t, filepath.Join(dir, "child"))))
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !followed.Load() || !left.Load() || !failed.Load() || !awaited.Load() || !running.Load() {
		t.Fatalf("followed %v, the member gone %v, a listing failed %v, the output's end awaited %v with what it started still running %v", followed.Load(), left.Load(), failed.Load(), awaited.Load(), running.Load())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "done")); err != nil || string(b) != "done\n" {
		t.Fatalf("what the member started as it ended was killed while it held the output: %q %v", b, err)
	}
	if res := svc.Result(); res.Signal != "terminated" || res.Exit != -1 || res.Killed || res.Err != "" {
		t.Fatalf("%+v", res)
	}
}

// Stop may be called from more than one goroutine at once: each returns
// once the service has ended, and it ends once.
func TestStopFromManyGoroutines(t *testing.T) {
	shortGrace(t, 200*time.Millisecond)
	dir := t.TempDir()
	svc := serve(t, dir, `trap "" TERM; echo $$ > pid; while :; do sleep 0.05; done`)
	pid := pidFile(t, filepath.Join(dir, "pid"))
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { svc.Stop(context.Background()) })
	}
	wg.Wait()
	select {
	case <-svc.Done():
	default:
		t.Fatal("a Stop returned before the service ended")
	}
	if !gone(t, pid, 3*time.Second) {
		t.Fatalf("%d outlived the stops", pid)
	}
}

// The context Serve is given bounds the service's life: its end kills the
// group, and the result says the context ended (Canceled).
func TestAServicesContextBoundsItsLife(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, err := Serve(ctx, Spec{Argv: []string{"sh", "-c", `trap "" TERM; echo $$ > pid; while :; do sleep 0.05; done`}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	pid := pidFile(t, filepath.Join(dir, "pid"))
	cancel()
	select {
	case <-svc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the service outlived its context by 10 s")
	}
	if res := svc.Result(); !res.Canceled || res.Signal != "killed" {
		t.Fatalf("%+v", res)
	}
	if !gone(t, pid, 3*time.Second) {
		t.Fatalf("%d outlived its context", pid)
	}
}

// A service is refused as a run is: a bad spec, a token's name in Inject,
// a CONDUCTOR_ name in Env, a context already ended; one whose program is
// not there is an error with nothing running. Conductor's own names are
// given to it. Its timeout is its start's: one too short for any start
// ends it at once.
func TestServeRefusesWhatRunRefuses(t *testing.T) {
	dir := t.TempDir()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx context.Context
		s   Spec
	}{
		"no command":       {context.Background(), Spec{Dir: dir}},
		"relative dir":     {context.Background(), Spec{Argv: []string{"true"}, Dir: "."}},
		"long timeout":     {context.Background(), Spec{Argv: []string{"true"}, Dir: dir, Timeout: MaxTimeout + 1}},
		"a token":          {context.Background(), Spec{Argv: []string{"true"}, Dir: dir, Inject: map[string]string{"CONDUCTOR_NOTIFY_TOKEN": "x"}}},
		"a CONDUCTOR_ env": {context.Background(), Spec{Argv: []string{"true"}, Dir: dir, Env: map[string]string{"CONDUCTOR_PORT_WEB": "1"}}},
		"a loader":         {context.Background(), Spec{Argv: []string{"true"}, Dir: dir, Env: map[string]string{"LD_PRELOAD": "x"}}},
		"ended":            {done, Spec{Argv: []string{"true"}, Dir: dir}},
		"not there":        {context.Background(), Spec{Argv: []string{"/nonexistent/program"}, Dir: dir}},
	} {
		if svc, err := Serve(tc.ctx, tc.s); err == nil || svc != nil {
			t.Errorf("%s: %v %v", name, svc, err)
		}
	}
	svc := serve(t, dir, `env > env.txt; echo $$ > pid; while :; do sleep 0.05; done`, func(s *Spec) {
		s.Inject = map[string]string{"CONDUCTOR_PORT_WEB": "41000", "CONDUCTOR_PREVIEW_URL": "http://127.0.0.1:41000/"}
	})
	pidFile(t, filepath.Join(dir, "pid"))
	b, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil || !strings.Contains(string(b), "CONDUCTOR_PORT_WEB=41000\n") || !strings.Contains(string(b), "CONDUCTOR_PREVIEW_URL=http://127.0.0.1:41000/\n") {
		t.Fatalf("the service's environment: %q %v", b, err)
	}
	svc.Stop(context.Background())
	// A start bound no start meets: the service is ended and Serve says so.
	var p Process
	svc, err = Serve(context.Background(), Spec{Argv: []string{"sleep", "300"}, Dir: dir, Timeout: time.Nanosecond, Started: func(got Process) { p = got }})
	if err == nil || svc != nil || !strings.Contains(err.Error(), "did not start within") {
		t.Fatalf("a start past its bound: %v %v", svc, err)
	}
	if p.PID == 0 || !gone(t, p.PID, 3*time.Second) {
		t.Fatalf("the service %d outlived a start past its bound", p.PID)
	}
}

// Where no service runner is (off unix: a service is stopped through its
// process group), Serve refuses before anything starts. proc does not build
// off unix (internal/pty does not), so the refusal is tested through the
// seam Serve consults, made the other platforms' (run_other.go).
func TestServeIsRefusedWhereNoServiceRunnerIs(t *testing.T) {
	old := noServiceRunner
	noServiceRunner = func() error { return errNoServiceRunner }
	t.Cleanup(func() { noServiceRunner = old })
	started := false
	svc, err := Serve(context.Background(), Spec{Argv: []string{"sleep", "300"}, Dir: t.TempDir(), Started: func(Process) { started = true }})
	if err == nil || svc != nil || err.Error() != "proc: no service runner on this platform" || started {
		t.Fatalf("%v %v, started %v", svc, err, started)
	}
}

// A process that left the service's group is not the service's: its stop
// leaves it alone, as Run does (TestRunLeavesAloneAChildThatLeftTheGroup),
// whether it redirected its output or holds the service's. One that holds
// it is no member of the group, gone once the first process has ended: the
// output is given WaitDelay after the reaping, as Run gives it, and the
// result says what holds it, not that the group's kill took it (Killed).
func TestAServicesStopLeavesAloneWhatLeftTheGroup(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		holds     bool
	}{{"its output redirected", " >/dev/null 2>&1", false}, {"holding the service's output", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			shortGrace(t, time.Hour)
			dir := t.TempDir()
			svc := serve(t, dir, `"$0" -test.run='^TestHelperLeavesTheGroup$'`+tc.out+` & echo $! > child; echo $$ > pid; wait`, func(s *Spec) {
				s.Argv = append(s.Argv, os.Args[0])
				s.Env = map[string]string{"PROC_HELPER": "setsid"}
			})
			pidFile(t, filepath.Join(dir, "pid"))
			child := pidFile(t, filepath.Join(dir, "child"))
			t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) })
			// The helper writes "ready" once it has left the group.
			if !appears(filepath.Join(dir, "ready"), 10*time.Second) {
				t.Fatal("the helper did not leave the group")
			}
			svc.Stop(context.Background())
			if err := syscall.Kill(child, 0); err != nil {
				t.Fatalf("the child that left the group was ended with the service: %v", err)
			}
			if res := svc.Result(); tc.holds && (res.Killed || !strings.Contains(res.Err, "left the group")) {
				t.Fatalf("%+v", res)
			}
		})
	}
}
