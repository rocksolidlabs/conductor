//go:build unix

package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// slowHelp gives a help read of a test the time a loaded machine may need
// to start a shell: the product's 5 s bound is not what these tests prove.
func slowHelp(t *testing.T) {
	t.Helper()
	was := helpTimeout
	helpTimeout = 30 * time.Second
	t.Cleanup(func() { helpTimeout = was })
}

// The help goes to one regular file, never a pipe, standard error with it,
// and the argv is the command, the rest of its argv, then the help's args.
func TestHelpReadsAFileOfStdoutAndStderr(t *testing.T) {
	slowHelp(t)
	p := script(t, `if [ -f /dev/stdout ]; then echo file; else echo pipe; fi
if [ -f /dev/stderr ]; then echo "err file" >&2; else echo "err pipe" >&2; fi
echo "args: $*"`)
	text, res := new(ProbeProcess).Help(context.Background(), []string{p, "first"}, []string{"session", "--help"}, nil)
	if !res.Ran || res.Error != "" || res.TooLong {
		t.Fatalf("%+v", res)
	}
	if text != "file\nerr file\nargs: first session --help\n" {
		t.Fatalf("read %q", text)
	}
}

// Any exit status counts: a help printed by a program that exits 1 is read.
func TestHelpTakesANonZeroExit(t *testing.T) {
	slowHelp(t)
	p := script(t, `echo "Usage of x:" >&2; echo "  -a  words" >&2; exit 2`)
	text, res := new(ProbeProcess).Help(context.Background(), []string{p}, []string{"--help"}, nil)
	if !res.Ran || text != "Usage of x:\n  -a  words\n" {
		t.Fatalf("%+v %q", res, text)
	}
}

// A help of MaxHelp bytes is read; one byte more and none of it is.
func TestHelpBound(t *testing.T) {
	slowHelp(t)
	for _, c := range []struct {
		n       int
		tooLong bool
	}{{MaxHelp, false}, {MaxHelp + 1, true}} {
		p := script(t, "head -c "+strconv.Itoa(c.n)+" /dev/zero | tr '\\0' x")
		text, res := new(ProbeProcess).Help(context.Background(), []string{p}, []string{"--help"}, nil)
		if res.TooLong != c.tooLong {
			t.Fatalf("%d bytes: %+v", c.n, res)
		}
		if c.tooLong && (text != "" || res.Error != ErrHelpTooLong.Error()) {
			t.Fatalf("%d bytes: %d read, %+v", c.n, len(text), res)
		}
		if !c.tooLong && len(text) != c.n {
			t.Fatalf("%d bytes: %d read", c.n, len(text))
		}
	}
}

// A program that does not answer is ended at the run's bound (the Timeout
// field: the bound is the test's, not a clock it races), with the helper it
// left in its group, and nothing it printed is read.
func TestHelpEndsAHangAtItsBound(t *testing.T) {
	_, late, wait := heldHelper(t)
	helper := filepath.Join(t.TempDir(), "helper")
	p := script(t, "echo partial\nsh -c "+quote(wait)+" </dev/null >/dev/null 2>&1 &\n"+
		"echo $! > "+quote(helper+".new")+" && mv "+quote(helper+".new")+" "+quote(helper)+"\n"+wait)
	text, res := (&ProbeProcess{Timeout: 200 * time.Millisecond}).Help(context.Background(), []string{p}, []string{"--help"}, nil)
	if res.Ran || !strings.HasPrefix(res.Error, "timeout") || text != "" {
		t.Fatalf("%+v %q", res, text)
	}
	if b, err := os.ReadFile(helper); err == nil {
		// The helper started before the bound: it is ended with the group.
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			t.Fatalf("pid file %q", b)
		}
		waitGone(t, pid)
	}
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper went on: %v", err)
	}
}

// End ends a help read on the caller's goroutine, and the helper its
// program left in its group, whatever the run's bound (an hour here).
func TestHelpEndEndsTheRunAndItsGroup(t *testing.T) {
	_, late, wait := heldHelper(t)
	helper := filepath.Join(t.TempDir(), "helper")
	p := script(t, "sh -c "+quote(wait)+" </dev/null >/dev/null 2>&1 &\n"+
		"echo $! > "+quote(helper+".new")+" && mv "+quote(helper+".new")+" "+quote(helper)+"\n"+wait)
	r := &ProbeProcess{Timeout: time.Hour}
	type answer struct {
		text string
		res  ProbeResult
	}
	ran := make(chan answer, 1)
	go func() {
		text, res := r.Help(context.Background(), []string{p}, []string{"--help"}, nil)
		ran <- answer{text, res}
	}()
	pid := pidIn(t, helper)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	r.End()
	select {
	case a := <-ran:
		if a.res.Ran || a.res.Error != errProbeEnded || a.text != "" {
			t.Fatalf("%+v %q", a.res, a.text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("End did not end the help read")
	}
	waitGone(t, pid)
	if _, err := os.Stat(late); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper went on: %v", err)
	}
}
