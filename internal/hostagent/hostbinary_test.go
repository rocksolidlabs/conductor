package hostagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// cutLink is a TCP proxy to a server that a test can cut: every connection
// it carries is closed, and those that come while it is cut are refused,
// until it is mended.
type cutLink struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	cut    bool
	conns  []net.Conn
}

func newCutLink(t *testing.T, target string) *cutLink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &cutLink{ln: ln, target: target}
	t.Cleanup(func() { ln.Close(); l.sever() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.mu.Lock()
			if l.cut {
				l.mu.Unlock()
				c.Close()
				continue
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				l.mu.Unlock()
				c.Close()
				continue
			}
			l.conns = append(l.conns, c, up)
			l.mu.Unlock()
			go func() { io.Copy(up, c); up.Close() }()
			go func() { io.Copy(c, up); c.Close() }()
		}
	}()
	return l
}

func (l *cutLink) url() string { return "http://" + l.ln.Addr().String() }

func (l *cutLink) sever() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cut = true
	for _, c := range l.conns {
		c.Close()
	}
	l.conns = nil
}

func (l *cutLink) mend() {
	l.mu.Lock()
	l.cut = false
	l.mu.Unlock()
}

// buildConductor builds this checkout's conductor into dir.
func buildConductor(t *testing.T, dir string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH to build conductor with")
	}
	bin := filepath.Join(dir, "conductor")
	cmd := exec.Command(goBin, "build", "-o", bin, "../../cmd/conductor")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// reportAgent is the stub agent the host runs: it reads lines "<how>
// <words>" from its fifo and reports each with conductor notify, then
// writes "<how> <exit> <words>" to its log; its first line there names the
// host's socket from its environment. How: done, the sender as it is;
// older, a sender with no socket named (one of an earlier release); wrong,
// another token; gone, a socket that is not there.
const reportAgent = `fifo=$1; log=$2; errs=$3
printf 'socket %s\n' "${CONDUCTOR_HOST_SOCKET-unset}" >> "$log"
while IFS= read -r line; do
  how=${line%% *}; words=${line#* }
  case $how in
    done) "$CONDUCTOR_BIN" notify --quiet=false --state done --message "$words" 2>>"$errs" ;;
    older) env -u CONDUCTOR_HOST_SOCKET "$CONDUCTOR_BIN" notify --quiet=false --state done --message "$words" 2>>"$errs" ;;
    event) "$CONDUCTOR_BIN" notify --quiet=false --event progress --message "$words" 2>>"$errs" ;;
    oldevent) env -u CONDUCTOR_HOST_SOCKET -u CONDUCTOR_HOST_EVENTS "$CONDUCTOR_BIN" notify --quiet=false --event progress --message "$words" 2>>"$errs" ;;
    wrong) CONDUCTOR_NOTIFY_TOKEN=wrong "$CONDUCTOR_BIN" notify --quiet=false --state done --message "$words" 2>>"$errs" ;;
    gone) CONDUCTOR_HOST_SOCKET=/nonexistent/report.sock "$CONDUCTOR_BIN" notify --quiet=false --state done --message "$words" 2>>"$errs" ;;
  esac
  printf '%s %s %s\n' "$how" "$?" "$words" >> "$log"
done < "$fifo"
`

// agentLog reads the stub agent's log for the line that starts with
// prefix, waiting up to bound.
func agentLog(t *testing.T, path, prefix string, bound time.Duration) string {
	t.Helper()
	for deadline := time.Now().Add(bound); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			sc := bufio.NewScanner(strings.NewReader(string(b)))
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), prefix) {
					return sc.Text()
				}
			}
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(path)
			t.Fatalf("the agent's log has no %q: %q", prefix, b)
		}
	}
}

// A real conductor host, built from this checkout, running the stub agent:
// the agent is handed the host's socket, in a folder of the user's alone;
// its sender reports there, never on the server's route, while the host's
// connection is up and while it is down, and the server's copy shows the
// report once the host is back; a sender with no socket named, and one
// whose socket is not there, report on the server's route; a refused
// token is a refusal there; and the host removes its socket as it ends.
// A server's own session, the variable in the server's environment and
// in its agent's catalog entry, is given none.
func TestARealHostTakesItsAgentsReportsOnItsSocket(t *testing.T) {
	work := socketBase(t)
	bin := buildConductor(t, t.TempDir())

	printed := filepath.Join(t.TempDir(), "env")
	cat, err := catalog.Load(catalog.File{Agents: []catalog.Agent{{ID: "printer", Name: "printer",
		Command: []string{"/bin/sh", "-c", `printf '%s\n' "${CONDUCTOR_HOST_SOCKET-unset}" > "$0"; exec cat`, printed},
		Env:     map[string]string{notify.EnvHostSocket: "/tmp/from-the-catalog/report.sock"}, EnvPassthrough: []string{notify.EnvHostSocket}}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.WorkbenchToken = adminToken
	cfg.HostTokens = []string{hostToken}
	root := t.TempDir()
	cfg.AllowedRoots = []string{root}
	cfg.DefaultCwd = root
	cfg.Dev = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(cfg, cat, discardLog, nil, st)
	if err != nil {
		t.Fatal(err)
	}
	route := &routeCount{}
	ts := httptest.NewServer(route.wrap(srv.Handler()))
	t.Cleanup(func() {
		ts.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	link := newCutLink(t, ts.Listener.Addr().String())

	fifo := filepath.Join(work, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath, errsPath := filepath.Join(work, "log"), filepath.Join(work, "errs")
	script := filepath.Join(work, "agent.sh")
	if err := os.WriteFile(script, []byte(reportAgent), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	host := exec.Command(bin, "host", "--server", link.url(), "--token", hostToken, "--no-local", "--relay-only", "--name", "socket e2e", "--log-level", "debug", "--cwd", root, "--", "/bin/sh", script, fifo, logPath, errsPath)
	// No agent program on PATH; the socket's folder in the test's own.
	host.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"), "TMPDIR=" + work, "XDG_RUNTIME_DIR="}
	hostLog, err := os.Create(filepath.Join(work, "host.log"))
	if err != nil {
		t.Fatal(err)
	}
	host.Stdout, host.Stderr = hostLog, hostLog
	host.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- host.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		_ = syscall.Kill(-host.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	// The fifo's writer opens once the agent opens its end.
	opened := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			opened <- f
		}
	}()
	var w *os.File
	select {
	case w = <-opened:
	case err := <-exited:
		b, _ := os.ReadFile(hostLog.Name())
		t.Fatalf("the host ended: %v\n%s", err, b)
	case <-time.After(60 * time.Second):
		b, _ := os.ReadFile(hostLog.Name())
		t.Fatalf("the agent never started\n%s", b)
	}
	t.Cleanup(func() { w.Close() })
	say := func(line string) {
		t.Helper()
		if _, err := w.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}

	socket := strings.TrimPrefix(agentLog(t, logPath, "socket ", 10*time.Second), "socket ")
	if err := notify.CheckHostSocket(socket); err != nil || !strings.HasPrefix(socket, filepath.Join(work, reportDirPrefix)) {
		t.Fatalf("the agent was handed %q: %v", socket, err)
	}
	var hs session.Driver
	for deadline := time.Now().Add(30 * time.Second); hs == nil; time.Sleep(10 * time.Millisecond) {
		for _, i := range srv.Registry().List() {
			if i.Kind == session.KindHosted && i.Name == "socket e2e" {
				hs, _ = srv.Registry().Get(i.ID)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the host never registered")
		}
	}
	shows := func(what, words string) {
		t.Helper()
		// The host's next connection comes within its reconnect backoff
		// (30 s at most).
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if i := hs.Info(); i.Status == session.StatusRunning && i.Attention.State == session.AttentionDone && i.Attention.Message == words {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the server's copy shows %+v", what, hs.Info())
			}
		}
	}

	say("done on the socket")
	if got := agentLog(t, logPath, "done ", 15*time.Second); got != "done 0 on the socket" {
		t.Fatalf("the agent logged %q", got)
	}
	shows("the host connected", "on the socket")
	progress := func(what string, want ...string) {
		t.Helper()
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if got := recordProgress(t, ts.URL, hs.Info().ID); slices.Equal(got, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the record holds %q, want %q", what, recordProgress(t, ts.URL, hs.Info().ID), want)
			}
		}
	}
	say("event up")
	if got := agentLog(t, logPath, "event 0 up", 15*time.Second); got != "event 0 up" {
		t.Fatalf("the agent logged %q", got)
	}
	progress("an event with the link up", "up")

	link.sever()
	untilListed(t, hs, "the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
	say("done while the host is away")
	if got := agentLog(t, logPath, "done 0 while", 15*time.Second); got != "done 0 while the host is away" {
		t.Fatalf("the agent logged %q", got)
	}
	say("event away")
	if got := agentLog(t, logPath, "event 0 away", 15*time.Second); got != "event 0 away" {
		t.Fatalf("the agent logged %q", got)
	}
	if got := recordProgress(t, ts.URL, hs.Info().ID); !slices.Equal(got, []string{"up"}) {
		t.Fatalf("the record changed while the link was cut: %q", got)
	}
	link.mend()
	shows("the host back", "while the host is away")
	progress("the event made while the link was cut", "up", "away")
	if n := route.n.Load(); n != 0 {
		t.Fatalf("the server's routes were asked %d times", n)
	}

	say("older a sender of an earlier release")
	agentLog(t, logPath, "older 0 ", 15*time.Second)
	shows("an older sender", "a sender of an earlier release")
	if n := route.n.Load(); n != 1 {
		t.Fatalf("the server's routes were asked %d times", n)
	}
	say("oldevent by an earlier sender")
	agentLog(t, logPath, "oldevent 0 ", 15*time.Second)
	progress("an older sender's event", "up", "away", "by an earlier sender")
	if n := route.n.Load(); n != 2 {
		t.Fatalf("the server's routes were asked %d times", n)
	}

	say("wrong refused")
	if got := agentLog(t, logPath, "wrong ", 15*time.Second); got != "wrong 1 refused" {
		t.Fatalf("the agent logged %q", got)
	}
	if b, _ := os.ReadFile(errsPath); !strings.Contains(string(b), "the host returned 401") {
		t.Fatalf("the sender said %q", b)
	}
	if n := route.n.Load(); n != 2 {
		t.Fatalf("a refusal was sent to the server: %d", n)
	}

	say("gone through the server")
	agentLog(t, logPath, "gone 0 ", 15*time.Second)
	shows("the socket gone", "through the server")
	if n := route.n.Load(); n != 3 {
		t.Fatalf("the server's route was asked %d times", n)
	}

	// Stopped as a person stops it: the host ends its agent and itself.
	stopped = true
	_ = host.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(-host.Process.Pid, syscall.SIGKILL)
		<-exited
		t.Fatal("the host did not stop")
	}
	if _, err := os.Lstat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the host left its socket's folder: %v", err)
	}

	// A server's own session: the variable is in the server's environment
	// and passed through by its agent's entry, and still not given.
	t.Setenv(notify.EnvHostSocket, socket)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"agentId":"printer"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: %d %v", resp.StatusCode, err)
	}
	if d, ok := srv.Registry().Get(created.ID); ok {
		t.Cleanup(func() { _ = d.Stop(context.Background()) })
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(printed); err == nil && strings.HasSuffix(string(b), "\n") {
			if string(b) != "unset\n" {
				t.Fatalf("a server's session was given the host's socket: %q", b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server's session never printed its environment")
		}
	}
}

// recordProgress is the progress events the server's record of session id
// holds, in order.
func recordProgress(t *testing.T, base, id string) []string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/api/history/sessions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct{ Record journal.Record }
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the record: %d %v", resp.StatusCode, err)
	}
	var out []string
	for _, e := range got.Record.Entries {
		if e.Event == session.ActivityProgress {
			out = append(out, e.Message)
		}
	}
	return out
}
