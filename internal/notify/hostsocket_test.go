//go:build unix

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shortDir is a folder of mode 0700 short enough for a socket's path.
func shortDir(t *testing.T) string {
	t.Helper()
	parent := os.TempDir()
	if len(parent) > 40 {
		parent = "/tmp"
	}
	d, err := os.MkdirTemp(parent, "ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// fakeHost listens on a socket in a folder of mode 0700 and answers each
// report with answer, counting the bodies it reads.
type fakeHost struct {
	path   string
	bodies chan map[string]any
	auth   chan string
	n      atomic.Int64
}

func newFakeHost(t *testing.T, answer func(n int64, w http.ResponseWriter)) *fakeHost {
	t.Helper()
	h := &fakeHost{path: filepath.Join(shortDir(t), "report.sock"), bodies: make(chan map[string]any, 16), auth: make(chan string, 16)}
	ln, err := net.Listen("unix", h.path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != HostReportPath {
			http.NotFound(w, r)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		h.bodies <- m
		h.auth <- r.Header.Get("Authorization")
		answer(h.n.Add(1), w)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return h
}

// countingServer is the server's routes, answering 200 and counting.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func ok(_ int64, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }

// The variable names the host's socket beside the server's route.
func TestTargetFromEnvNamesTheHostsSocket(t *testing.T) {
	env := map[string]string{EnvURL: "http://s/api/sessions/a/attention", EnvToken: "tok", EnvHostSocket: "/run/x/report.sock"}
	target, err := TargetFromEnv(func(k string) string { return env[k] })
	if err != nil || target != (Target{URL: env[EnvURL], Token: "tok", Socket: "/run/x/report.sock"}) {
		t.Fatalf("%+v %v", target, err)
	}
	delete(env, EnvHostSocket)
	if target, _ := TargetFromEnv(func(k string) string { return env[k] }); target.Socket != "" {
		t.Fatalf("%+v", target)
	}
	if _, err := TargetFromEnv(func(string) string { return "" }); err != ErrNotInSession {
		t.Fatal(err)
	}
}

// An attention report goes to the host's socket, with the token and the
// attention route's body, and not to the server; an event and the files a
// tool call touched go to the server's events route.
func TestAnAttentionReportGoesToTheHost(t *testing.T) {
	host := newFakeHost(t, ok)
	srv, served := countingServer(t)
	target := Target{URL: srv.URL + "/api/sessions/a/attention", Token: "tok", Socket: host.path}
	if err := target.Send(context.Background(), Request{State: "done", Message: "m", TurnID: "t1", AgentSession: "as1", Turn: true}); err != nil {
		t.Fatal(err)
	}
	if got := <-host.bodies; got["state"] != "done" || got["message"] != "m" || got["turnId"] != "t1" || got["agentSession"] != "as1" {
		t.Fatalf("the host read %v", got)
	}
	if auth := <-host.auth; auth != "Bearer tok" {
		t.Fatalf("authorization %q", auth)
	}
	if served.Load() != 0 {
		t.Fatal("the server was sent the report too")
	}
	if err := target.Send(context.Background(), Request{Event: "progress", Message: "step"}); err != nil {
		t.Fatal(err)
	}
	if err := target.Send(context.Background(), Request{Tool: "Edit", Files: []FileRef{{Op: "edit", Path: "a.go"}}}); err != nil {
		t.Fatal(err)
	}
	if host.n.Load() != 1 || served.Load() != 2 {
		t.Fatalf("the host read %d, the server %d", host.n.Load(), served.Load())
	}
}

// Whatever the host answers is its answer: a refusal is not sent to the
// server instead, and neither is a report whose connection was made and
// then lost.
func TestAHostsRefusalIsNeverSentToTheServer(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusConflict, http.StatusServiceUnavailable, http.StatusNotFound, http.StatusInternalServerError} {
		host := newFakeHost(t, func(_ int64, w http.ResponseWriter) { w.WriteHeader(code) })
		srv, served := countingServer(t)
		err := Target{URL: srv.URL, Token: "tok", Socket: host.path}.Send(context.Background(), Request{State: "done"})
		if err == nil || !strings.Contains(err.Error(), "the host returned") {
			t.Errorf("%d: %v", code, err)
		}
		if served.Load() != 0 {
			t.Errorf("%d was sent to the server", code)
		}
	}
	// A connection the host ends without an answer.
	host := newFakeHost(t, func(_ int64, w http.ResponseWriter) {
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			c.Close()
		}
	})
	srv, served := countingServer(t)
	if err := (Target{URL: srv.URL, Token: "tok", Socket: host.path}).Send(context.Background(), Request{State: "done"}); err == nil {
		t.Fatal("a lost answer was taken")
	}
	if served.Load() != 0 {
		t.Fatal("a report the host may have applied was sent to the server")
	}
}

// A 429 from the host is tried again on the host, as the server's is.
func TestAHostsRateLimitIsTriedAgainThere(t *testing.T) {
	retryWait = func(time.Duration) <-chan time.Time { return time.After(0) }
	t.Cleanup(func() { retryWait = time.After })
	host := newFakeHost(t, func(n int64, w http.ResponseWriter) {
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv, served := countingServer(t)
	if err := (Target{URL: srv.URL, Token: "tok", Socket: host.path}).Send(context.Background(), Request{State: "working"}); err != nil {
		t.Fatal(err)
	}
	if host.n.Load() != 3 || served.Load() != 0 {
		t.Fatalf("the host read %d, the server %d", host.n.Load(), served.Load())
	}
}

// A socket that cannot be reached sends the report to the server: gone,
// nothing listening on it, a path not clean or past its bound, or one that
// is not this user's alone to reach; the token never reaches such a
// socket's listener.
func TestASocketThatCannotBeReachedFallsBackToTheServer(t *testing.T) {
	gone := filepath.Join(shortDir(t), "report.sock")

	deaf := filepath.Join(shortDir(t), "report.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: deaf, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()

	open := newFakeHost(t, ok)
	if err := os.Chmod(filepath.Dir(open.path), 0o755); err != nil {
		t.Fatal(err)
	}

	linked := newFakeHost(t, ok)
	linkDir := shortDir(t)
	if err := os.Symlink(filepath.Dir(linked.path), filepath.Join(linkDir, "l")); err != nil {
		t.Fatal(err)
	}

	linkedSock := newFakeHost(t, ok)
	inner := shortDir(t)
	if err := os.Symlink(linkedSock.path, filepath.Join(inner, "report.sock")); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(shortDir(t), "report.sock")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, socket := range map[string]string{
		"gone":                  gone,
		"nothing listening":     deaf,
		"a folder others enter": open.path,
		"a link to the folder":  filepath.Join(linkDir, "l", "report.sock"),
		"a link to the socket":  filepath.Join(inner, "report.sock"),
		"a file":                file,
		"relative":              "report.sock",
		"not clean":             filepath.Dir(gone) + "/./report.sock",
		"past its bound":        "/" + strings.Repeat("a", MaxHostSocketPath),
	} {
		srv, served := countingServer(t)
		if err := (Target{URL: srv.URL, Token: "tok", Socket: socket}).Send(context.Background(), Request{State: "done"}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if served.Load() != 1 {
			t.Errorf("%s: the server was sent %d", name, served.Load())
		}
	}
	for name, h := range map[string]*fakeHost{"a folder others enter": open, "a link to the folder": linked, "a link to the socket": linkedSock} {
		if n := h.n.Load(); n != 0 {
			t.Errorf("%s: its listener was sent %d", name, n)
		}
	}
}

// A sender of this release with no socket named (a server's session, a
// host of an earlier release) reports to the server's route alone.
func TestNoSocketReportsToTheServer(t *testing.T) {
	srv, served := countingServer(t)
	if err := (Target{URL: srv.URL, Token: "tok"}).Send(context.Background(), Request{State: "done"}); err != nil {
		t.Fatal(err)
	}
	if served.Load() != 1 {
		t.Fatalf("the server was sent %d", served.Load())
	}
}

// The sender writes nothing to a socket whose peer is not its own user:
// the listener reads no byte, no request and no token, and the report goes
// to the server's route. A socket whose peer is this user (every other
// test here, the kernel's own answer) is used.
func TestTheSenderWritesNothingToASocketOfAnotherUser(t *testing.T) {
	path := filepath.Join(shortDir(t), "report.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	read := make(chan int, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c)
				read <- len(b)
			}()
		}
	}()
	peerUID = func(net.Conn) (int, error) { return os.Getuid() + 1, nil }
	t.Cleanup(func() { peerUID = socketPeerUID })
	srv, served := countingServer(t)
	if err := (Target{URL: srv.URL, Token: "tok", Socket: path}).Send(context.Background(), Request{State: "done"}); err != nil {
		t.Fatal(err)
	}
	if served.Load() != 1 {
		t.Fatalf("the server was sent %d", served.Load())
	}
	select {
	case n := <-read:
		if n != 0 {
			t.Fatalf("the listener read %d bytes", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the connection was never closed")
	}
	// A peer that cannot be read is no peer of this user's either.
	peerUID = func(net.Conn) (int, error) { return 0, errors.New("unknown") }
	if err := (Target{URL: srv.URL, Token: "tok", Socket: path}).Send(context.Background(), Request{State: "done"}); err != nil {
		t.Fatal(err)
	}
	if n := <-read; n != 0 || served.Load() != 2 {
		t.Fatalf("the listener read %d bytes, the server was sent %d", n, served.Load())
	}
}

// The kernel's answer for a socket this process listens on is this user.
func TestTheSocketsPeerIsReadFromTheKernel(t *testing.T) {
	path := filepath.Join(shortDir(t), "report.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if uid, err := socketPeerUID(c); err != nil || uid != os.Getuid() {
		t.Fatalf("peer %d %v", uid, err)
	}
}
