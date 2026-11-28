//go:build unix

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// heldProbe is a probe's program of the test's own, under way: it starts a
// helper in its process group, says both pids, and both wait on a FIFO that
// nothing in the test ever writes. The probes' timeout is made an hour, so
// only what the test calls can end them.
type heldProbe struct {
	e                  *testEnv
	pid, helper        int
	fifo, late, tmpdir string
}

func startHeldProbe(t *testing.T) *heldProbe {
	t.Helper()
	dir := t.TempDir()
	h := &heldProbe{tmpdir: filepath.Join(dir, "tmp"), fifo: filepath.Join(dir, "release"), late: filepath.Join(dir, "late")}
	if err := os.Mkdir(h.tmpdir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The probe's own temporary folder is made under TMPDIR: it must be gone
	// too once the stop returns.
	t.Setenv("TMPDIR", h.tmpdir)
	if err := syscall.Mkfifo(h.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	prog := filepath.Join(dir, "held-agent")
	wait := "read line < " + shellQuote(h.fifo) + "; echo late >> " + shellQuote(h.late)
	body := "#!/bin/sh\n" +
		"sh -c " + shellQuote(wait) + " &\n" +
		"echo $$ $! > " + shellQuote(started+".new") + " && mv " + shellQuote(started+".new") + " " + shellQuote(started) + "\n" +
		wait + "\n" +
		"echo '2.1.287 (Claude Code)'\n"
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h.e = newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "held", Name: "Held", Command: []string{prog}, Adapter: "claude"},
	})
	h.e.srv.probes.timeout = time.Hour
	// The catalog answers at once, the probe pending; the probe goes on.
	h.e.srv.probes.wait = 10 * time.Millisecond
	h.e.do("GET", "/api/catalog", adminToken, nil)
	pids := readPids(t, started)
	h.pid, h.helper = pids[0], pids[1]
	// Whatever the stop does, the test ends what it started before its
	// folder goes.
	t.Cleanup(func() { _ = syscall.Kill(-h.pid, syscall.SIGKILL) })
	return h
}

// readPids waits, within a bound that only bounds a hang, for the pids a
// program wrote to path.
func readPids(t *testing.T, path string) []int {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var pids []int
		for _, f := range strings.Fields(string(b)) {
			pid, err := strconv.Atoi(f)
			if err != nil || pid <= 0 {
				t.Fatalf("pid file %q", b)
			}
			pids = append(pids, pid)
		}
		return pids
	}
	t.Fatalf("no pids in %s", path)
	return nil
}

// processGone reports whether pid is no process any more (an orphan is
// reaped by whoever it was handed to).
func processGone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// endedBy checks, once a stop returned, that the probe was ended: its
// program reaped, the helper it left in its group sent its end (it exits,
// handed to init, within a bound that only bounds a hang), nothing left to
// read the FIFO and so nothing that could write after, and its temporary
// folder removed.
func (h *heldProbe) endedBy(t *testing.T, what string) {
	t.Helper()
	if err := syscall.Kill(h.pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the probe's program (pid %d) is still there after %s: kill 0 said %v", h.pid, what, err)
	}
	for deadline := time.Now().Add(30 * time.Second); !processGone(h.helper); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the helper in the probe's group (pid %d) still runs after %s", h.helper, what)
		}
	}
	if w, err := os.OpenFile(h.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); !errors.Is(err, syscall.ENXIO) {
		if err == nil {
			w.Close()
		}
		t.Fatalf("something still reads the probe's FIFO after %s: open for writing said %v", what, err)
	}
	if _, err := os.Stat(h.late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe wrote after it was released: %v", err)
	}
	left, err := os.ReadDir(h.tmpdir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range left {
		t.Errorf("the probe left %s in its TMPDIR", f.Name())
	}
}

// A probe under way when the server stops is ended by the stop and waited
// for (#961): an e2e server stopped soon after it started left a real CLI's
// version probe running, which wrote into the test's HOME after the test
// had removed it. The program and the helper it left in its group are gone
// when the stop returns.
func TestShutdownEndsTheProbesUnderWay(t *testing.T) {
	h := startHeldProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.e.srv.Shutdown(ctx)
	if ctx.Err() != nil {
		t.Fatal("the stop ran past its bound")
	}
	h.endedBy(t, "the stop")
}

// The Go tests' servers end by WaitBackground (endAtCleanup), not
// Shutdown: it ends the probes under way too, before the test's folders go.
func TestWaitBackgroundEndsTheProbesUnderWay(t *testing.T) {
	h := startHeldProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.e.srv.WaitBackground(ctx)
	if ctx.Err() != nil {
		t.Fatal("WaitBackground ran past its bound")
	}
	h.endedBy(t, "WaitBackground")
}

// The probes are the first thing the stop closes, before any of its waits:
// a catalog read while the stop is held at its first wait (Brakeman's acts,
// held here by one the test counts) starts no probe.
func TestShutdownStopsProbesBeforeItsWaits(t *testing.T) {
	prog := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho '2.1.287 (Claude Code)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "late", Name: "Late", Command: []string{prog}, Adapter: "claude"},
	})
	if e.srv.brakeman == nil {
		t.Fatal("the test server has no Brakeman desk to hold the stop at")
	}
	var runs atomic.Int32
	e.srv.probes.wait = time.Hour
	e.srv.lookups.wait = time.Hour
	e.srv.probes.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		runs.Add(1)
		return agents.ProbeResult{Ran: true, Identified: true, Version: "2.1.287"}
	}
	e.srv.brakeman.acting.Add(1)
	var held sync.Once
	release := func() { held.Do(e.srv.brakeman.acting.Done) }
	t.Cleanup(release)
	stopped := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		e.srv.Shutdown(ctx)
		close(stopped)
	}()
	// The stop has begun: Brakeman's desk is closing, and the stop waits
	// for the act the test holds.
	for deadline := time.Now().Add(30 * time.Second); !e.srv.brakeman.closing.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stop never reached Brakeman's desk")
		}
	}
	_, out := e.do("GET", "/api/catalog", adminToken, nil)
	asked := false
	for _, a := range out["agents"].([]any) {
		if m := a.(map[string]any); m["id"] == "late" {
			id, _ := m["identity"].(map[string]any)
			asked = id["error"] == errProbeStopping
		}
	}
	if !asked {
		t.Errorf("the catalog read during the stop was not answered that the server is stopping: %v", out["agents"])
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("%d probes started once the stop had begun", n)
	}
	release()
	<-stopped
}

// doneSeen is a context that says when its Done is first asked for: close
// asks for it only as it waits for its probes, so a test sees close
// waiting without a clock.
type doneSeen struct {
	context.Context
	once sync.Once
	seen chan struct{}
}

func (d *doneSeen) Done() <-chan struct{} {
	d.once.Do(func() { close(d.seen) })
	return d.Context.Done()
}

// close waits for the probes it ended: a run that, once its context ends,
// waits at a gate the test holds keeps close from returning, and when the
// gate is let go the run has ended before close returns. A close that only
// ended the runs returns while the gate is held, and fails every run.
func TestProbesCloseWaitsForTheRunsItEnded(t *testing.T) {
	c := newProbeCache()
	c.wait = time.Millisecond
	started, gate := make(chan struct{}), make(chan struct{})
	var ended atomic.Bool
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		close(started)
		<-ctx.Done()
		<-gate
		ended.Store(true)
		return agents.ProbeResult{Ran: true}
	}
	var open sync.Once
	t.Cleanup(func() { open.Do(func() { close(gate) }) })
	p := agents.Probe{Args: []string{"--version"}}
	c.identity(context.Background(), []string{"/bin/held"}, "claude", "Claude Code", p, nil, false)
	<-started
	bound, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx := &doneSeen{Context: bound, seen: make(chan struct{})}
	returned := make(chan bool, 1)
	go func() {
		c.close(ctx)
		returned <- ended.Load()
	}()
	select {
	case <-returned:
		t.Fatal("close returned while the probe it ended still ran")
	case <-ctx.seen:
	}
	open.Do(func() { close(gate) })
	if !<-returned {
		t.Fatal("the probe had not ended when close returned")
	}
	if bound.Err() != nil {
		t.Fatal("close ran past its bound")
	}
}

// With the stop's bound spent, close returns at once, without waiting: the
// probe has been sent its end all the same (its run ignores its context and
// has an hour, so only close's own signal ends it), and nothing it answers
// afterwards is kept.
func TestProbesCloseWithItsBoundSpent(t *testing.T) {
	dir := t.TempDir()
	fifo, started := filepath.Join(dir, "release"), filepath.Join(dir, "started")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "agent")
	body := "#!/bin/sh\necho $$ > " + shellQuote(started+".new") + " && mv " + shellQuote(started+".new") + " " + shellQuote(started) + "\n" +
		"read line < " + shellQuote(fifo) + "\necho '2.1.287 (Claude Code)'\n"
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newProbeCache()
	c.wait = time.Millisecond
	c.timeout = time.Hour
	ranOut, gate := make(chan agents.ProbeResult, 1), make(chan struct{})
	c.run = func(_ context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		res := r.Run(context.Background(), command, p, env)
		ranOut <- res
		<-gate
		return agents.ProbeResult{Ran: true, Identified: true, Version: "2.1.287"}
	}
	var open sync.Once
	t.Cleanup(func() { open.Do(func() { close(gate) }) })
	p := *agents.ProbeFor("claude")
	c.identity(context.Background(), []string{prog}, "claude", "Claude Code", p, nil, false)
	pid := readPids(t, started)[0]
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	spent, cancel := context.WithCancel(context.Background())
	cancel()
	c.close(spent)
	// close returned with the run held at the gate: it did not wait. The
	// program was sent its end by close itself.
	select {
	case res := <-ranOut:
		if res.Ran || res.Error == "" {
			t.Fatalf("the ended probe's run: %+v", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the probe's program was not ended by close")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the probe's program (pid %d) is still there: kill 0 said %v", pid, err)
	}
	open.Do(func() { close(gate) })
	bound, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	c.close(bound)
	if bound.Err() != nil {
		t.Fatal("the second close ran past its bound")
	}
	if _, held := c.cached([]string{prog}, "claude", "Claude Code", p, nil); held {
		t.Fatal("what the ended probe answered was kept")
	}
}

// After the stop no probe starts, and one that waited for a slot when it
// came is not run: its askers are answered that the server is stopping.
func TestProbesAfterCloseRunNothing(t *testing.T) {
	c := newProbeCache()
	c.wait = time.Hour
	var runs atomic.Int32
	c.run = func(ctx context.Context, r *agents.ProbeProcess, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
		runs.Add(1)
		return agents.ProbeResult{Ran: true}
	}
	// Every slot taken: the probe asked next waits for one.
	c.sem = make(chan struct{}, 1)
	c.sem <- struct{}{}
	p := agents.Probe{Args: []string{"--version"}}
	asked := make(chan Identity, 1)
	go func() {
		asked <- c.identity(context.Background(), []string{"/bin/waiting"}, "claude", "Claude Code", p, nil, false)
	}()
	// The probe is counted before its asker waits: close waits for it.
	for {
		c.mu.Lock()
		n := len(c.inflight)
		c.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.close(ctx)
	if ctx.Err() != nil {
		t.Fatal("close ran past its bound")
	}
	if id := <-asked; id.Ran || id.Error != errProbeStopping {
		t.Fatalf("the waiting probe's asker: %+v", id)
	}
	if id := c.identity(context.Background(), []string{"/bin/later"}, "claude", "Claude Code", p, nil, false); id.Ran || id.Pending || id.Error != errProbeStopping {
		t.Fatalf("a probe asked after the stop: %+v", id)
	}
	if n := runs.Load(); n != 0 {
		t.Fatalf("%d probes ran after the stop", n)
	}
	if _, held := c.cached([]string{"/bin/waiting"}, "claude", "Claude Code", p, nil); held {
		t.Fatal("a probe not run was kept as an answer")
	}
}

// A help read under way when the server stops (GET /api/catalog/{id}/flags)
// is ended by the stop and waited for, as a version probe is: its program
// answers --version at once and holds on --help, with a helper in its
// group; both are gone when the stop returns, and its folder is removed.
func TestShutdownEndsTheHelpReadUnderWay(t *testing.T) {
	dir := t.TempDir()
	h := &heldProbe{tmpdir: filepath.Join(dir, "tmp"), fifo: filepath.Join(dir, "release"), late: filepath.Join(dir, "late")}
	if err := os.Mkdir(h.tmpdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", h.tmpdir)
	if err := syscall.Mkfifo(h.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	prog := filepath.Join(dir, "held-agent")
	wait := "read line < " + shellQuote(h.fifo) + "; echo late >> " + shellQuote(h.late)
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = --version ]; then echo '2.1.287 (Claude Code)'; exit 0; fi\n" +
		"sh -c " + shellQuote(wait) + " &\n" +
		"echo $$ $! > " + shellQuote(started+".new") + " && mv " + shellQuote(started+".new") + " " + shellQuote(started) + "\n" +
		wait + "\n"
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h.e = newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "held", Name: "Held", Command: []string{prog}, Adapter: "claude", AllowArgs: true},
	})
	h.e.srv.probes.timeout = time.Hour
	h.e.srv.probes.wait = 10 * time.Millisecond
	// Asked until the help read has begun: the identity probe answers
	// first, then the read starts and holds.
	for deadline := time.Now().Add(30 * time.Second); ; {
		h.e.do("GET", "/api/catalog/held/flags", adminToken, nil)
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the help read never began")
		}
	}
	pids := readPids(t, started)
	h.pid, h.helper = pids[0], pids[1]
	t.Cleanup(func() { _ = syscall.Kill(-h.pid, syscall.SIGKILL) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.e.srv.Shutdown(ctx)
	if ctx.Err() != nil {
		t.Fatal("the stop ran past its bound")
	}
	h.endedBy(t, "the stop")
}
