package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// recordedPipes is ext.Pipes that records, in order, its input's close and
// its Stop.
type recordedPipes struct {
	mu    sync.Mutex
	calls []string
	done  chan struct{}
}

func (p *recordedPipes) note(c string) {
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
}
func (p *recordedPipes) Stdin() io.WriteCloser { return recordedStdin{p} }
func (p *recordedPipes) Stdout() io.Reader     { return strings.NewReader("") }
func (p *recordedPipes) Done() <-chan struct{} { return p.done }
func (p *recordedPipes) Exit() ext.ExitStatus {
	return ext.ExitStatus{Code: 143, Signal: "terminated", Err: "stopped"}
}
func (p *recordedPipes) Stop(context.Context, time.Duration) error {
	p.note("stop")
	return nil
}
func (p *recordedPipes) Process() ext.ProcessInfo { return ext.ProcessInfo{} }

type recordedStdin struct{ p *recordedPipes }

func (w recordedStdin) Write(b []byte) (int, error) { return len(b), nil }
func (w recordedStdin) Close() error                { w.p.note("close_input"); return nil }

// PipesChild is a Runtime's pipes as a protocol way's child: its end and
// exit are the pipes', the exit's words an error, and its Stop closes the
// child's input before it stops it (which cancels a permission prompt the
// child waits on).
func TestPipesChildClosesInputFirst(t *testing.T) {
	p := &recordedPipes{done: make(chan struct{})}
	c := PipesChild(p)
	if c.Done() != p.done {
		t.Fatal("Done is not the pipes'")
	}
	if st := c.Exit(); st.Code != 143 || st.Signal != "terminated" || st.Err == nil || st.Err.Error() != "stopped" {
		t.Fatalf("Exit %+v", st)
	}
	if err := c.Stop(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.calls, " ") != "close_input stop" {
		t.Fatalf("calls %v", p.calls)
	}
}

// A restart the driver asks during a turn (RestartProtocol: Claude Code's
// input past its bound) is made at that turn's end: the child ended, a new
// one started on the same conversation, its notice in the transcript, and
// a message queued during the turn written to the new child alone. Asked
// outside a turn, it is made at once.
func TestRestartProtocolAtTheTurnsEnd(t *testing.T) {
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	s.SetAgentSession(AgentSession{ID: "conv-1", Source: AgentSessionSet})
	f, again, third := sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol(), sessiontest.NewFakeProtocol()
	if err := s.enterProtocol(context.Background(), f.Start(again, third)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	_, err := s.Submit(context.Background(), Submission{Text: "first"})
	mustPending(t, err)
	waitCalls(t, f, "the first write", sends(1))
	f.Accepted(f.Sent()[0].Ref)
	f.TurnStarted("")
	_, err = s.Submit(context.Background(), Submission{Text: "second"})
	mustPending(t, err)
	if err := s.RestartProtocol("restarted", "Started again: its input reached its bound."); err != nil {
		t.Fatal(err)
	}
	// Noted for the turn's end, nothing begun.
	s.mu.Lock()
	p := s.way.Load()
	noted, begun := p.restartAtEnd, p.restarting
	s.mu.Unlock()
	if !noted || begun {
		t.Fatalf("in the turn: noted %v, restarting %v", noted, begun)
	}
	f.TurnEnded("done", Usage{})
	waitCalls(t, again, "the write to the new child", sends(1))
	if len(f.Sent()) != 1 {
		t.Fatalf("the ending child was written %d messages", len(f.Sent()))
	}
	if got := again.Sent()[0].Text; got != "second" || again.StartedOn() != "conv-1" {
		t.Fatalf("the new child on %q was written %q", again.StartedOn(), got)
	}
	if c := strings.Join(f.Calls(), " "); !strings.HasSuffix(c, "close_input stop") {
		t.Fatalf("the old child's calls %v", c)
	}
	// The turn ran to its own end: the restart waited for it.
	if !slices.ContainsFunc(allEntries(t, s), func(e TranscriptEntry) bool {
		return e.Kind == EntryTurn && e.Phase == "end" && e.Outcome == "done"
	}) {
		t.Fatal("the turn did not end done: the restart cut it")
	}
	notice := slices.IndexFunc(allEntries(t, s), func(e TranscriptEntry) bool {
		return e.Kind == EntryNotice && e.Code == "restarted" && e.Words == "Started again: its input reached its bound."
	})
	if notice < 0 {
		t.Fatal("no notice of the restart")
	}
	// Outside a turn: at once.
	again.Accepted(again.Sent()[0].Ref)
	again.TurnStarted("")
	again.TurnEnded("done", Usage{})
	if err := s.RestartProtocol("restarted", "again"); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, third, "the third child", func(c []string) bool { return sessiontest.Count(c, "begin") == 1 })
	if st := s.Info().Status; st != StatusRunning {
		t.Fatalf("the session is %s", st)
	}
	// On a session in its terminal there is none.
	other, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	if err := other.RestartProtocol("restarted", "x"); !errors.Is(err, ErrNotProtocol) {
		t.Fatalf("a terminal session's restart: %v", err)
	}
}

// noticeErr is a driver's Read error that names its own notice.
type noticeErr struct{ code, words string }

func (e noticeErr) Error() string                   { return "driver: " + e.words }
func (e noticeErr) EndNotice() (code, words string) { return e.code, e.words }

// A driver's Read error may say how the session tells its end: in the
// agent's words, or not at all where the driver told it already.
func TestReadEndNoticeIsTheDrivers(t *testing.T) {
	for _, c := range []struct {
		err   error
		code  string
		words string
	}{
		{noticeErr{"unreadable", "Claude Code sent something Conductor cannot read; the session ended."}, "unreadable", "Claude Code sent something Conductor cannot read; the session ended."},
		{noticeErr{"", ""}, "", ""},
		{errors.New("a line"), noticeUnreadable, UnreadableWords},
	} {
		s, _, f := protocolSession(t, Options{})
		f.Fail(c.err)
		if !waitFor(30*time.Second, func() bool { return s.Info().Status.Ended() }) {
			t.Fatal("the session did not end")
		}
		var notices []TranscriptEntry
		for _, e := range allEntries(t, s) {
			if e.Kind == EntryNotice {
				notices = append(notices, e)
			}
		}
		switch {
		case c.code == "" && len(notices) != 0:
			t.Errorf("%v: a notice the driver had told: %+v", c.err, notices)
		case c.code != "" && (len(notices) != 1 || notices[0].Code != c.code || notices[0].Words != c.words):
			t.Errorf("%v: notices %+v", c.err, notices)
		}
	}
}

// Each piece the transcript keeps reaches OnTranscriptPiece under its key,
// cut as kept; OnTranscriptDropped names what the bounds let go, an entry
// by its seq; OnCaptured has every byte whole.
func TestTranscriptPiecesAreKeyed(t *testing.T) {
	var mu sync.Mutex
	pieces := map[string]int{}
	var dropped []string
	whole := 0
	s, _, f := protocolSession(t, Options{
		OnCaptured: func(_ string, b []byte) { mu.Lock(); whole += len(b); mu.Unlock() },
		OnTranscriptPiece: func(_, key string, b []byte) {
			mu.Lock()
			pieces[key] += len(b)
			mu.Unlock()
		},
		OnTranscriptDropped: func(_ string, keys []string) { mu.Lock(); dropped = append(dropped, keys...); mu.Unlock() },
	})
	f.TurnStarted("")
	f.Text("w", "hello", true)
	f.ToolStarted(ToolStep{Ref: "t1", Name: "Bash", Input: []byte(`{"command":"ls"}`), Summary: "ls"})
	big := strings.Repeat("o", MaxTranscriptPart+10)
	f.ToolEnded("t1", ToolResult{Status: ToolOK, Output: []byte(big)})
	es := allEntries(t, s)
	text := slices.IndexFunc(es, func(e TranscriptEntry) bool { return e.Kind == EntryText })
	tool := slices.IndexFunc(es, func(e TranscriptEntry) bool { return e.Kind == EntryTool })
	ts, ls := pieceKey(es[text].Seq, "text"), pieceKey(es[tool].Seq, "")
	mu.Lock()
	if pieces[ts] != 5 || pieces[ls+PartInput] != len(`{"command":"ls"}`) || pieces[ls+"text"] != 2 || pieces[ls+PartOutput] != MaxTranscriptPart {
		t.Fatalf("pieces %v", pieces)
	}
	if whole != 5+len(`{"command":"ls"}`)+len(big) {
		t.Fatalf("OnCaptured had %d bytes", whole)
	}
	mu.Unlock()
	// Past the entries' bound the oldest go, by their seq.
	first := es[0].Seq
	for i := range MaxTranscriptEntries {
		f.Text(fmt.Sprintf("x-%d", i), "x", true)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(dropped, strconv.FormatUint(first, 10)) {
		t.Fatalf("the first entry's drop was not named: %v", dropped[:min(len(dropped), 5)])
	}
}

// A chat's Info says its way, its protocol and that its messages go as
// written, with no terminal size; a terminal session's says none.
func TestInfoSaysTheWay(t *testing.T) {
	plain, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	if i := plain.Info(); i.Way != "" || i.Protocol != "" || i.Verbatim {
		t.Fatalf("a terminal session's Info: %q %q %v", i.Way, i.Protocol, i.Verbatim)
	}
	s, _ := newLocalWith(t, Options{ScrollbackBytes: 4096})
	f := sessiontest.NewFakeProtocol()
	f.ProtocolName, f.IsVerbatim = "claude-stream-json", true
	if err := s.enterProtocol(context.Background(), f.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	if i := s.Info(); i.Way != WayProtocol || i.Protocol != "claude-stream-json" || !i.Verbatim || i.Cols != 0 || i.Rows != 0 {
		t.Fatalf("a chat's Info: %q %q %v %dx%d", i.Way, i.Protocol, i.Verbatim, i.Cols, i.Rows)
	}
}
