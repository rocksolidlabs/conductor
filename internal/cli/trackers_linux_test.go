//go:build linux

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// From a terminal, conductor trackers set reads the token with the
// terminal's echo off: nothing of what is typed comes back to the
// terminal, and the token reaches the server whole.
func TestTrackersSetReadsATerminalUnseen(t *testing.T) {
	clearConductorEnv(t)
	t.Setenv("CONDUCTOR_WORKBENCH_TOKEN", dispatchToken)
	var mu sync.Mutex
	var got string
	hs := handlerServer(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Token string }
		_ = decodeBody(r, &b)
		mu.Lock()
		got = b.Token
		mu.Unlock()
		_, _ = w.Write([]byte(`{"tracker":{"host":"github.com","login":"mona","fingerprint":"abc","scopes":[],"source":"settings","state":"ok"}}`))
	})
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no terminal: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	var echoed bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		_, _ = io.Copy(&echoed, ptmx)
	}()
	type result struct {
		code int
		err  error
		out  string
	}
	done := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		code, err := Run(context.Background(), []string{"trackers", "set", "--server", hs}, tty, &out, &errb)
		done <- result{code, err, out.String()}
	}()
	// Typed only once the read has turned the echo off: before, the
	// terminal itself would echo it, whatever the command does. A read
	// that never turns it off fails here, before the test's own deadline.
	giveUp := time.Now().Add(time.Hour)
	if d, ok := t.Deadline(); ok {
		giveUp = d.Add(-time.Until(d) / 10)
	}
	for {
		tio, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		if tio.Lflag&unix.ECHO == 0 {
			break
		}
		if time.Now().After(giveUp) {
			t.Fatal("the echo was never turned off: the token would be shown as it is typed")
		}
		runtime.Gosched()
	}
	if _, err := ptmx.Write([]byte(ghToken + "\r")); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.code != 0 || r.err != nil || !strings.Contains(r.out, "github.com: kept") {
		t.Fatalf("set from a terminal: %d %v\n%s", r.code, r.err, r.out)
	}
	mu.Lock()
	sent := got
	mu.Unlock()
	if sent != ghToken {
		t.Fatalf("the server was sent %q", sent)
	}
	tty.Close()
	ptmx.Close()
	<-read
	if strings.Contains(echoed.String(), ghToken) || strings.Contains(echoed.String(), ghToken[:8]) {
		t.Fatalf("the terminal echoed the token: %q", echoed.String())
	}
}

// The child of TestTrackersSetHoldsNoTokenInItsCommandLine: conductor
// trackers set against the server its parent names, standard input its
// parent's pipe.
func TestTrackersSetChild(t *testing.T) {
	srv := os.Getenv("CONDUCTOR_TEST_TRACKERS_CHILD")
	if srv == "" {
		t.Skip("run by TestTrackersSetHoldsNoTokenInItsCommandLine")
	}
	code, err := Run(context.Background(), []string{"trackers", "set", "--server", srv}, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

// While conductor trackers set sends the token, the process's command
// line and its environment hold none of it: the server's handler reads
// /proc/<pid>/cmdline and environ as the request arrives.
func TestTrackersSetHoldsNoTokenInItsCommandLine(t *testing.T) {
	clearConductorEnv(t)
	var mu sync.Mutex
	var pid int
	var seen, token string
	hs := handlerServer(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Token string }
		_ = decodeBody(r, &b)
		mu.Lock()
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		environ, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		seen, token = string(cmdline)+"\x00"+string(environ), b.Token
		mu.Unlock()
		_, _ = w.Write([]byte(`{"tracker":{"host":"github.com","login":"mona","fingerprint":"abc","scopes":[],"source":"settings","state":"ok"}}`))
	})
	cmd := exec.Command(os.Args[0], "-test.run=^TestTrackersSetChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CONDUCTOR_TEST_TRACKERS_CHILD="+hs, "CONDUCTOR_WORKBENCH_TOKEN="+dispatchToken)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	mu.Lock()
	if err := cmd.Start(); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	mu.Unlock()
	_, _ = io.WriteString(in, ghToken+"\n")
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the child: %v\n%s", err, out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if token != ghToken {
		t.Fatalf("the server was sent %q\n%s", token, out.String())
	}
	if !strings.Contains(seen, "TestTrackersSetChild") {
		t.Fatalf("the child's command line was not read: %q", seen)
	}
	if strings.Contains(seen, ghToken) || strings.Contains(out.String(), ghToken) {
		t.Fatal("the token is in the child's command line, its environment or its output")
	}
}
