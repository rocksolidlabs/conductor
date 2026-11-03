//go:build unix

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// panicFirst is a Runtime whose first StartTerminal starts natively, then
// ends and reaps what it started and panics, as the contract has a Runtime
// do (sessiontest's PanicAfterStart); every later call is the plain fake's.
type panicFirst struct {
	*sessiontest.FakeRuntime
	panicker *sessiontest.FakeRuntime
	n        atomic.Int32
}

func (p *panicFirst) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	if p.n.Add(1) == 1 {
		return p.panicker.StartTerminal(ctx, s)
	}
	return p.FakeRuntime.StartTerminal(ctx, s)
}

// A start that panics once its process exists is refused as a start that
// failed: the guard logs the panic at error, naming the Runtime, and by
// then the process the Runtime started is gone and reaped. The server
// serves on, and the attempt after it starts in the same Runtime.
func TestAStartThatPanicsAfterItsProcessIsRefusedWithNoneLeft(t *testing.T) {
	takesAnswered(t)
	rt := &panicFirst{FakeRuntime: &sessiontest.FakeRuntime{Base: local.Native()},
		panicker: &sessiontest.FakeRuntime{Base: local.Native(), PanicAfterStart: true}}
	seam := nativeSeam()
	logs := &logBuffer{}
	e := runtimeEnv(t, seam, slog.New(slog.NewTextHandler(logs, nil)), withRuntime(rt))
	e.gitRepo(t)
	runID := launchOne(t, e, oneTaskCrew("panics", e.root))
	sid := runningAttempt(t, e, runID, "build", 2)["sessionId"].(string)
	started := rt.panicker.Asked("StartTerminal")
	if len(started) != 1 || started[0].Process.PID <= 0 {
		t.Fatalf("the panicking start: %+v", started)
	}
	// Reaped, not a zombie: a zombie still answers signal 0.
	if err := syscall.Kill(started[0].Process.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the process of the start that panicked, %d: %v", started[0].Process.PID, err)
	}
	if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "the Runtime panicked") || !strings.Contains(l, "provider=fake call=StartTerminal") {
		t.Fatalf("the guard's log:\n%s", l)
	}
	_, out := e.do("GET", "/api/runs/"+runID+"/tasks/build", adminToken, nil)
	first := out["attempts"].([]any)[0].(map[string]any)
	if first["state"] != "failed" || first["stage"] != "launch" {
		t.Fatalf("the first attempt: %v", first)
	}
	if got := terminalRefs(rt.FakeRuntime); len(got) != 1 || got[0] != attemptRef(sid, runID, 2) {
		t.Fatalf("the second attempt's session: %+v", got)
	}
	if n := len(seam.Calls()); n != 0 {
		t.Fatalf("the native Runtime was asked %v", methodsOf(seam))
	}
}

// panicsIn is a Terminal that panics in method, Read or Process; the rest
// is its terminal's.
type panicsIn struct {
	ext.Terminal
	method string
}

func (p panicsIn) Read(b []byte) (int, error) {
	if p.method == "Read" {
		panic("the fake's terminal panics in Read")
	}
	return p.Terminal.Read(b)
}

func (p panicsIn) Process() ext.ProcessInfo {
	if p.method == "Process" {
		panic("the fake's terminal panics in Process")
	}
	return p.Terminal.Process()
}

// terminalPanics is a Runtime whose terminals start natively and panic in
// method.
type terminalPanics struct {
	*sessiontest.FakeRuntime
	method string
}

func (r terminalPanics) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	t, err := r.FakeRuntime.StartTerminal(ctx, s)
	if err != nil {
		return nil, err
	}
	return panicsIn{Terminal: t, method: r.method}, nil
}

// A registered Runtime's terminal that panics, in the session's pump's
// Read or in the Process the start's log line reads, ends its session and
// not the server: the guard logs the panic at error, naming the Runtime
// and the method, and ends the process through the Runtime's Stop, killed
// and reaped by the time the session's process is done; the server serves
// on.
func TestATerminalThatPanicsEndsItsSessionNotTheServer(t *testing.T) {
	for _, method := range []string{"Read", "Process"} {
		t.Run(method, func(t *testing.T) {
			rt := terminalPanics{FakeRuntime: &sessiontest.FakeRuntime{Base: local.Native()}, method: method}
			logs := &logBuffer{}
			e := runtimeEnv(t, nativeSeam(), slog.New(slog.NewTextHandler(logs, nil)), withRuntime(rt))
			l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "cat", where: rt.Where()}, nil)
			if aerr != nil {
				t.Fatalf("the start: %+v", aerr)
			}
			<-l.Ended()
			<-l.ProcessDone()
			started := rt.Asked("StartTerminal")
			if len(started) != 1 || started[0].Process.PID <= 0 {
				t.Fatalf("the start: %+v", started)
			}
			// Reaped, not a zombie: a zombie still answers signal 0.
			if err := syscall.Kill(started[0].Process.PID, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("the process of the terminal that panicked, %d: %v", started[0].Process.PID, err)
			}
			if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, "provider=fake call=Terminal."+method+" failure=panic") {
				t.Fatalf("the guard's log:\n%s", l)
			}
			if resp, out := e.do("GET", "/api/sessions", adminToken, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("the server after the panic: %d %v", resp.StatusCode, out)
			}
		})
	}
}
