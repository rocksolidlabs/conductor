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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
)

// A probe under way when the server stops is ended by the stop and waited
// for (#961): an e2e server stopped soon after it started left a real CLI's
// version probe running, which wrote into the test's HOME after the test
// had removed it. The probe here is a program of the test's own that says
// it started and then waits on a FIFO, which nothing in the test ever
// writes: only the stop can end it.
func TestShutdownEndsTheProbesUnderWay(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	// The probe's own temporary folder is made under TMPDIR: it must be gone
	// too once the stop returns.
	t.Setenv("TMPDIR", tmp)
	fifo := filepath.Join(dir, "release")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	late := filepath.Join(dir, "late")
	prog := filepath.Join(dir, "held-agent")
	body := "#!/bin/sh\n" +
		"echo $$ > " + shellQuote(started+".new") + " && mv " + shellQuote(started+".new") + " " + shellQuote(started) + "\n" +
		"read line < " + shellQuote(fifo) + "\n" +
		"echo late > " + shellQuote(late) + "\n" +
		"echo '2.1.287 (Claude Code)'\n"
	if err := os.WriteFile(prog, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []catalog.Agent{
		{ID: "held", Name: "Held", Command: []string{prog}, Adapter: "claude"},
	})
	// The catalog answers at once, the probe pending; the probe goes on.
	e.srv.probes.wait = 10 * time.Millisecond
	e.do("GET", "/api/catalog", adminToken, nil)

	// The program's own word that it runs, then its pid.
	pid := 0
	for pid == 0 {
		if b, err := os.ReadFile(started); err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil {
				t.Fatalf("pid file %q: %v", b, err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Whatever the stop does, the test ends what it started before its
	// folder goes.
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	if ctx.Err() != nil {
		t.Fatal("the stop ran past its bound")
	}

	// The stop returned: the probe's program is gone (killed and reaped),
	// nothing reads the FIFO any more, so nothing can let the program go on
	// to write, and its temporary folder has been removed.
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the probe's program (pid %d) is still there after the stop: kill 0 said %v", pid, err)
	}
	if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); !errors.Is(err, syscall.ENXIO) {
		if err == nil {
			w.Close()
		}
		t.Fatalf("something still reads the probe's FIFO after the stop: open for writing said %v", err)
	}
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe wrote after it was released: %v", err)
	}
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range left {
		t.Errorf("the probe left %s in its TMPDIR", f.Name())
	}
}

// After the stop no probe starts, and one that waited for a slot when it
// came is not run: its askers are answered that the server is stopping.
func TestProbesAfterCloseRunNothing(t *testing.T) {
	c := newProbeCache()
	c.wait = time.Hour
	var runs atomic.Int32
	c.run = func(ctx context.Context, command []string, p agents.Probe, env map[string]string) agents.ProbeResult {
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
