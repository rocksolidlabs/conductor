package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// fakeProc is an in-memory Process: bytes written to out appear as PTY output.
type fakeProc struct {
	out    io.ReadCloser
	outW   io.WriteCloser
	input  chan []byte
	done   chan struct{}
	cols   uint16
	rows   uint16
	resize chan [2]uint16
	// onWrite, when set, runs in Write before the bytes arrive: what a
	// process does as it is written to.
	onWrite func()
}

func newFakeProc() *fakeProc {
	r, w := io.Pipe()
	return &fakeProc{out: r, outW: w, input: make(chan []byte, 64), done: make(chan struct{}), resize: make(chan [2]uint16, 16)}
}

func (f *fakeProc) Read(b []byte) (int, error) { return f.out.Read(b) }
func (f *fakeProc) Write(b []byte) (int, error) {
	if f.onWrite != nil {
		f.onWrite()
	}
	f.input <- append([]byte(nil), b...)
	return len(b), nil
}
func (f *fakeProc) Resize(c, r uint16) error {
	f.cols, f.rows = c, r
	f.resize <- [2]uint16{c, r}
	return nil
}
func (f *fakeProc) Done() <-chan struct{} { return f.done }
func (f *fakeProc) Exit() pty.ExitStatus  { return pty.ExitStatus{Code: 7} }
func (f *fakeProc) Stop(context.Context, time.Duration) error {
	f.exit()
	return nil
}
func (f *fakeProc) exit() {
	select {
	case <-f.done:
	default:
		close(f.done)
		f.outW.Close()
	}
}

func decodeControl(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	f, err := proto.Decode(frame)
	if err != nil || f.Type != proto.TypeControl {
		t.Fatalf("expected control frame, got type %d err %v", f.Type, err)
	}
	var m map[string]any
	json.Unmarshal(f.Payload, &m)
	return m
}

// waitControl returns the first control message that sink has received, or
// receives within 3 s, for which match is true; nil on timeout. State that
// Info() already shows is not proof that its broadcast frame reached the sink:
// a subscription delivers from a queue on its own goroutine.
func waitControl(sink *chanSink, match func(m map[string]any) bool) map[string]any {
	deadline := time.After(3 * time.Second)
	for i := 0; ; {
		for ; i < sink.count(); i++ {
			f, err := proto.Decode(sink.frame(i))
			if err != nil || f.Type != proto.TypeControl {
				continue
			}
			var m map[string]any
			if json.Unmarshal(f.Payload, &m) == nil && match(m) {
				return m
			}
		}
		select {
		case <-sink.writeCh:
		case <-deadline:
			return nil
		}
	}
}

// realDir is dir with its symbolic links resolved, as the session resolves
// the paths it is given: on macOS a temporary directory is under /var, a
// link to /private/var, and a path the session reports is the resolved one.
func realDir(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func newLocal(t *testing.T, cwd string) (*Local, *fakeProc) {
	t.Helper()
	cwd = realDir(t, cwd)
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Cwd: cwd, Cols: 80, Rows: 24}, p, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { p.exit() })
	return s, p
}

// newLocalWith is newLocal with the caller's Options and a temporary
// working directory.
func newLocalWith(t *testing.T, opts Options) (*Local, *fakeProc) {
	t.Helper()
	p := newFakeProc()
	s := NewLocal(Info{ID: "sess", Cwd: realDir(t, t.TempDir()), Cols: 80, Rows: 24}, p, opts)
	t.Cleanup(func() { p.exit() })
	return s, p
}

func TestLateAttachReceivesScrollbackThenLive(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	p.outW.Write([]byte("early\n"))
	time.Sleep(50 * time.Millisecond)
	sink := newChanSink(false)
	sub, err := s.Attach("", RoleView, "", 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	sink.waitFrames(t, 3)
	if m := decodeControl(t, sink.frame(0)); m["t"] != proto.CtlWelcome || m["role"] != "view" {
		t.Fatalf("welcome: %v", m)
	}
	if f, _ := proto.Decode(sink.frame(1)); f.Type != proto.TypeScrollback || string(f.Payload) != "early\n" {
		t.Fatalf("scrollback: %+v", f)
	}
	if m := decodeControl(t, sink.frame(2)); m["t"] != proto.CtlReady {
		t.Fatalf("ready: %v", m)
	}
	// The viewers broadcast, the join activity entry and the chat's join line
	// reach the subscriber too, in their own time; live output must follow
	// the ready marker, never precede it. The live frame is waited for by
	// what it is, not by a count: one of those frames landing between the
	// count and the write would otherwise stand in for it.
	sink.waitFrames(t, 5)
	p.outW.Write([]byte("live"))
	var live bool
	for deadline := time.Now().Add(5 * time.Second); !live && time.Now().Before(deadline); {
		for i := 3; i < sink.count(); i++ {
			if f, _ := proto.Decode(sink.frame(i)); f.Type == proto.TypeOutput && string(f.Payload) == "live" {
				live = true
			}
		}
		if !live {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !live {
		t.Fatalf("live output not received after ready (%d frames)", sink.count())
	}
	if s.Info().Viewers != 1 {
		t.Fatal("viewer count")
	}
	s.Detach(sub)
	if s.Info().Viewers != 0 {
		t.Fatal("viewer count after detach")
	}
}

func TestReadOnlyInputAndResize(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	sub, _ := s.Attach("", RoleView, "", 80, 24, sink)
	if err := s.Input(sub, []byte("x")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("input: %v", err)
	}
	if err := s.Resize(sub, 10, 10); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("resize: %v", err)
	}
}

// One viewer sizes the terminal (round 14): one of the owner's own windows
// (a controller with no link) as it attaches; another of the owner's
// windows and a control link follow, their passive resizes refused; Fit to
// my window (take) hands the size over, at the same size too; the sizer's
// leaving hands it to the owner's window that attached last, at that
// window's size, else to nobody. A coworker's laptop never shrinks the
// session on the owner's screen.
func TestOneViewerSizesTheTerminal(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	noResize := func(what string) {
		t.Helper()
		select {
		case got := <-p.resize:
			t.Fatalf("%s resized the terminal to %v", what, got)
		case <-time.After(50 * time.Millisecond):
		}
	}
	a, b, c := newChanSink(false), newChanSink(false), newChanSink(false)
	subA, _ := s.Attach("", RoleControl, "", 100, 30, a)
	if got := <-p.resize; got != [2]uint16{100, 30} {
		t.Fatalf("the owner's window: %v", got)
	}
	if w := firstControl(t, a, proto.CtlWelcome); w["sizer"] != true || w["sizedBy"] != subA.ID {
		t.Fatalf("the owner's welcome: %v", w)
	}
	subB, _ := s.Attach("", RoleControl, "", 120, 40, b)
	noResize("the owner's second window")
	if w := firstControl(t, b, proto.CtlWelcome); w["sizedBy"] != subA.ID || w["cols"] != float64(100) {
		t.Fatalf("the second window's welcome: %v", w)
	}
	subC, _ := s.Attach("", RoleControl, "link1", 80, 24, c)
	noResize("a control link joining")
	if err := s.Resize(subB, 130, 40); !errors.Is(err, ErrNotSizer) {
		t.Fatalf("the second window's resize: %v", err)
	}
	if err := s.Resize(subC, 70, 20); !errors.Is(err, ErrNotSizer) {
		t.Fatalf("the link's resize: %v", err)
	}
	noResize("a viewer that does not size it")
	// The sizer's own window sizes it.
	if err := s.Resize(subA, 90, 20); err != nil {
		t.Fatal(err)
	}
	if got := <-p.resize; got != [2]uint16{90, 20} {
		t.Fatalf("the sizer's resize: %v", got)
	}
	waitResize(t, c, subA.ID, 90)
	// Fit to my window: the link takes it.
	if err := s.ResizeWith(subC, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	if got := <-p.resize; got != [2]uint16{80, 24} {
		t.Fatalf("the link's take: %v", got)
	}
	waitResize(t, a, subC.ID, 80)
	if err := s.Resize(subA, 95, 25); !errors.Is(err, ErrNotSizer) {
		t.Fatalf("the owner's passive resize while the link holds it: %v", err)
	}
	// Taken back at the very same size: no resize, but everyone hears who sizes it.
	if err := s.ResizeWith(subA, 80, 24, true); err != nil {
		t.Fatal(err)
	}
	noResize("a take at the same size")
	waitResize(t, c, subA.ID, 80)
	// The sizer leaves: the owner's window that attached last takes it, at its size.
	s.Detach(subA)
	if got := <-p.resize; got != [2]uint16{130, 40} {
		t.Fatalf("handed to the owner's other window: %v", got)
	}
	waitResize(t, c, subB.ID, 130)
	// And when it leaves, nobody holds the size (a link is not the owner): the size stays.
	s.Detach(subB)
	noResize("the last of the owner's windows leaving")
	waitResize(t, c, "", 130)
	if info := s.Info(); info.Cols != 130 || info.Rows != 40 {
		t.Fatalf("info size %dx%d", info.Cols, info.Rows)
	}
	if err := s.Resize(subC, 0, 20); !errors.Is(err, ErrBadDimension) {
		t.Fatalf("bad dimension: %v", err)
	}
	if err := s.Input(subC, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got := <-p.input; string(got) != "hi" {
		t.Fatalf("input %q", got)
	}
}

// firstControl is the first control frame of type typ the sink received.
func firstControl(t *testing.T, sink *chanSink, typ string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		frames := append([][]byte(nil), sink.frames...)
		sink.mu.Unlock()
		for _, fr := range frames {
			f, _ := proto.Decode(fr)
			if f.Type != proto.TypeControl {
				continue
			}
			var m map[string]any
			json.Unmarshal(f.Payload, &m)
			if m["t"] == typ {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s frame", typ)
	return nil
}

// waitResize waits for the sink's latest resize frame to name by (absent
// for "") at cols.
func waitResize(t *testing.T, sink *chanSink, by string, cols float64) {
	t.Helper()
	var last map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		frames := append([][]byte(nil), sink.frames...)
		sink.mu.Unlock()
		last = nil
		for _, fr := range frames {
			f, _ := proto.Decode(fr)
			if f.Type != proto.TypeControl {
				continue
			}
			var m map[string]any
			json.Unmarshal(f.Payload, &m)
			if m["t"] == proto.CtlResize {
				last = m
			}
		}
		got, _ := last["by"].(string)
		if last != nil && got == by && last["cols"] == cols {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the last resize %v, wanted by %q at %v columns", last, by, cols)
}

func TestExitBroadcastsStatus(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	sub, _ := s.Attach("", RoleControl, "", 80, 24, sink)
	p.exit()
	select {
	case <-s.Ended():
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end")
	}
	info := s.Info()
	if info.Status != StatusExited || info.ExitCode == nil || *info.ExitCode != 7 {
		t.Fatalf("info %+v", info)
	}
	if err := s.Input(sub, []byte("x")); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("input after exit: %v", err)
	}
	// late attach still works for scrollback
	late := newChanSink(false)
	if _, err := s.Attach("", RoleView, "", 80, 24, late); err != nil {
		t.Fatal(err)
	}
	late.waitFrames(t, 1)
	if m := decodeControl(t, late.frame(0)); m["status"] != string(StatusExited) {
		t.Fatalf("welcome status %v", m)
	}
}

func TestSessionEndedLogShowsTheExitCode(t *testing.T) {
	t.Run("exited", func(t *testing.T) {
		logs := &syncBuffer{}
		status := make(chan struct{}, 1)
		_, p := newLocalWith(t, Options{
			Log: slog.New(slog.NewTextHandler(logs, nil)),
			// The status row is recorded after the log line, in the same goroutine.
			OnActivity: func(_ string, e ActivityEntry, _ AttentionState) {
				if e.Type == ActivityStatus {
					status <- struct{}{}
				}
			},
		})
		p.exit()
		select {
		case <-status:
		case <-time.After(3 * time.Second):
			t.Fatal("session did not end")
		}
		if line := logs.String(); !strings.Contains(line, "status=exited") || !strings.Contains(line, "exitCode=7") {
			t.Fatalf("want the exit code in the log line, got %q", line)
		}
	})
	t.Run("no exit status", func(t *testing.T) {
		logs := &syncBuffer{}
		s, _ := newLocalWith(t, Options{Log: slog.New(slog.NewTextHandler(logs, nil))})
		s.markEnded(StatusStopped) // ended before the process reported an exit status
		if line := logs.String(); !strings.Contains(line, "status=stopped") || strings.Contains(line, "exitCode") {
			t.Fatalf("want no exitCode in the log line, got %q", line)
		}
	})
}

func TestDisconnectLink(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a, b := newChanSink(false), newChanSink(false)
	s.Attach("", RoleView, "link-1", 80, 24, a)
	s.Attach("", RoleView, "link-2", 80, 24, b)
	s.DisconnectLink("link-1")
	select {
	case r := <-a.closed:
		if !errors.Is(r, ErrRevoked) {
			t.Fatalf("reason %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("link-1 not disconnected")
	}
	select {
	case <-b.closed:
		t.Fatal("link-2 must stay attached")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMaxViewers(t *testing.T) {
	p := newFakeProc()
	defer p.exit()
	s := NewLocal(Info{ID: "s"}, p, Options{MaxViewers: 1})
	if _, err := s.Attach("", RoleView, "", 0, 0, newChanSink(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach("", RoleView, "", 0, 0, newChanSink(false)); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("expected ErrTooManyViewers, got %v", err)
	}
}

func TestFileGetPolicyAndResponse(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600)
	s, _ := newLocal(t, dir)
	sink := newChanSink(false)
	sub, _ := s.Attach("", RoleView, "", 80, 24, sink)
	sink.waitFrames(t, 3)
	before := sink.count()
	if err := s.FileGet(sub, proto.FileGet{ReqID: "r1", Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	// Presence and activity frames may interleave; find the file frame.
	var f proto.Frame
	deadline := time.Now().Add(2 * time.Second)
	for f.Type != proto.TypeFile && time.Now().Before(deadline) {
		sink.waitFrames(t, before+1)
		for i := before; i < sink.count(); i++ {
			if fr, _ := proto.Decode(sink.frame(i)); fr.Type == proto.TypeFile {
				f = fr
			}
		}
		before = sink.count()
	}
	if f.Type != proto.TypeFile {
		t.Fatalf("expected a file frame, got none")
	}
	h, body, err := proto.DecodeFile(f.Payload)
	if err != nil || h.ReqID != "r1" || h.Kind != "file" || string(body) != "hello" {
		t.Fatalf("file response %+v %q %v", h, body, err)
	}
	if err := s.FileGet(sub, proto.FileGet{ReqID: "", Path: "a.txt"}); err == nil {
		t.Fatal("empty reqId must fail")
	}

	restricted := NewLocal(Info{ID: "r", Cwd: dir}, newFakeProc(), Options{FileView: "control"})
	vs := newChanSink(false)
	vsub, _ := restricted.Attach("", RoleView, "", 80, 24, vs)
	if err := restricted.FileGet(vsub, proto.FileGet{ReqID: "r2", Path: "a.txt"}); !errors.Is(err, ErrFileDenied) {
		t.Fatalf("expected denial, got %v", err)
	}
	vs.waitFrames(t, 1)
	if m := decodeControl(t, vs.frame(0)); m["fileView"] != false {
		t.Fatalf("welcome should advertise fileView=false: %v", m)
	}
}

func TestAttentionFromBellAndClearOnInput(t *testing.T) {
	var changes []Attention
	var cmu sync.Mutex
	p := newFakeProc()
	s := NewLocal(Info{ID: "att", Cwd: t.TempDir(), Cols: 80, Rows: 24}, p, Options{OnChange: func(i Info) {
		cmu.Lock()
		changes = append(changes, i.Attention)
		cmu.Unlock()
	}})
	t.Cleanup(p.exit)
	viewer := newChanSink(false)
	vsub, _ := s.Attach("", RoleView, "", 80, 24, viewer)
	ctl := newChanSink(false)
	csub, _ := s.Attach("", RoleControl, "", 80, 24, ctl)
	viewer.waitFrames(t, 3)

	p.outW.Write([]byte("prompt> \x1b]9;need approval\a"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Info().Attention.State != AttentionNeedsInput {
		time.Sleep(10 * time.Millisecond)
	}
	att := s.Info().Attention
	if att.State != AttentionNeedsInput || att.Message != "need approval" || att.Source != SourceOSC || att.Since == nil {
		t.Fatalf("attention %+v", att)
	}
	// The broadcast reaches the viewer after Info() already shows the state,
	// so wait for the frame instead of scanning what has arrived so far.
	if waitControl(viewer, func(m map[string]any) bool {
		return m["t"] == proto.CtlAttention && m["state"] == "needs_input"
	}) == nil {
		t.Fatal("attention control frame not broadcast")
	}
	// view-role input is rejected and does not clear
	if err := s.Input(vsub, []byte("x")); !errors.Is(err, ErrReadOnly) || s.Info().Attention.State != AttentionNeedsInput {
		t.Fatalf("view input: %v %+v", err, s.Info().Attention)
	}
	// Keys without an Enter leave a notification's wait; an Enter withdraws it.
	if err := s.Input(csub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	<-p.input
	if a := s.Info().Attention; a.State != AttentionNeedsInput {
		t.Fatalf("keys without an Enter cleared it: %+v", a)
	}
	if err := s.Input(csub, []byte("\r")); err != nil {
		t.Fatal(err)
	}
	<-p.input
	if a := s.Info().Attention; a.State != AttentionNone || a.Since != nil || a.Source != SourceInput {
		t.Fatalf("not cleared: %+v", a)
	}
	// a late attach sees the current attention immediately
	s.SetAttention(AttentionNeedsInput, "again", SourceAPI)
	late := newChanSink(false)
	s.Attach("", RoleView, "", 80, 24, late)
	// The current attention is the last thing a late attach queues, behind the
	// scrollback and the activity replay.
	if waitControl(late, func(m map[string]any) bool {
		return m["t"] == proto.CtlAttention && m["message"] == "again"
	}) == nil {
		t.Fatal("late attach did not receive attention state")
	}
	cmu.Lock()
	n := len(changes)
	cmu.Unlock()
	if n == 0 {
		t.Fatal("OnChange never called")
	}
	if !s.AgentTokenOK("x") {
		s.SetAgentToken("secret")
		if !s.AgentTokenOK("secret") || s.AgentTokenOK("nope") || s.AgentTokenOK("") {
			t.Fatal("agent token check")
		}
	}
}

// rosterOf returns the list from the last viewers message a sink received.
func rosterOf(t *testing.T, sink *chanSink) (map[string]any, []map[string]any) {
	t.Helper()
	var last map[string]any
	for i := 0; i < sink.count(); i++ {
		f, err := proto.Decode(sink.frame(i))
		if err != nil || f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		if json.Unmarshal(f.Payload, &m) == nil && m["t"] == proto.CtlViewers {
			last = m
		}
	}
	if last == nil {
		t.Fatal("no viewers message received")
	}
	raw, _ := last["list"].([]any)
	list := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		list = append(list, v.(map[string]any))
	}
	return last, list
}

func TestViewersRosterCarriesNamesAndTyping(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a, b := newChanSink(false), newChanSink(false)
	subA, err := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Priya", LinkLabel: "pairing", Cols: 80, Rows: 24}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttachWith(AttachOptions{Role: RoleView, Cols: 80, Rows: 24}, b); err != nil {
		t.Fatal(err)
	}
	// B receives welcome, ready, A's replayed join, the roster and its own
	// join, one at a time from its queue: wait for the roster itself.
	if waitControl(b, func(m map[string]any) bool { return m["t"] == proto.CtlViewers && m["count"] == float64(2) }) == nil {
		t.Fatal("no viewers message listing both clients received")
	}
	msg, list := rosterOf(t, b)
	if msg["count"].(float64) != 2 || len(list) != 2 {
		t.Fatalf("roster: %v", msg)
	}
	byName := map[string]map[string]any{}
	for _, v := range list {
		byName[v["name"].(string)] = v
	}
	if byName["Priya"] == nil || byName["guest"] == nil {
		t.Fatalf("names: %v", list)
	}
	if byName["Priya"]["role"] != "control" || byName["Priya"]["link"] != "pairing" || byName["Priya"]["since"] == nil {
		t.Fatalf("Priya entry: %v", byName["Priya"])
	}
	if byName["guest"]["link"] != nil {
		t.Fatalf("guest should have no link label: %v", byName["guest"])
	}
	// Typing: input from A stamps lastInputAt and rebroadcasts the roster. Wait
	// for that roster, not for one more frame: B's own join may still be queued.
	if err := s.Input(subA, []byte("x")); err != nil {
		t.Fatal(err)
	}
	typed := waitControl(b, func(m map[string]any) bool {
		if m["t"] != proto.CtlViewers {
			return false
		}
		raw, _ := m["list"].([]any)
		for _, v := range raw {
			if e, ok := v.(map[string]any); ok && e["name"] == "Priya" && e["lastInputAt"] != nil {
				return true
			}
		}
		return false
	})
	if typed == nil {
		_, list = rosterOf(t, b)
		t.Fatalf("expected lastInputAt on Priya after input: %v", list)
	}
}

func TestAttachRejectsOverlongName(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	if _, err := s.AttachWith(AttachOptions{Role: RoleView, Name: strings.Repeat("n", 500), Cols: 80, Rows: 24}, sink); err != nil {
		t.Fatalf("attach should clean rather than reject: %v", err)
	}
	sink.waitFrames(t, 3)
	_, list := rosterOf(t, sink)
	if got := list[0]["name"].(string); len([]rune(got)) != 40 {
		t.Fatalf("name not capped: %d runes", len([]rune(got)))
	}
}

func TestSetAttentionFullBroadcastsOptionsAndInputClears(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	sub, _ := s.Attach("", RoleControl, "", 80, 24, sink)
	sink.waitFrames(t, 3)
	n := sink.count()
	s.SetAttentionFull(AttentionNeedsInput, "Allow Bash?", SourceAPI, "permission", []Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}})
	var m map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for m == nil && time.Now().Before(deadline) {
		sink.waitFrames(t, n+1)
		for i := n; i < sink.count(); i++ {
			if c := decodeControl(t, sink.frame(i)); c["t"] == proto.CtlAttention {
				m = c
			}
		}
		n = sink.count()
	}
	if m == nil || m["kind"] != "permission" || len(m["options"].([]any)) != 2 {
		t.Fatalf("attention: %v", m)
	}
	if got := s.Info().Attention; got.Kind != "permission" || len(got.Options) != 2 || got.Options[1].Input != "3" {
		t.Fatalf("info: %+v", got)
	}
	_ = s.AnswerInput(t.Context(), sub, s.Info().Attention.ID, []byte("1"))
	deadline = time.Now().Add(time.Second)
	for s.Info().Attention.State != AttentionNone && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if a := s.Info().Attention; a.State != AttentionNone || a.Kind != "" || a.Options != nil {
		t.Fatalf("not cleared: %+v", a)
	}
}

func TestCleanOptionsBounds(t *testing.T) {
	in := make([]Option, 10)
	for i := range in {
		in[i] = Option{Label: strings.Repeat("l", 100), Input: strings.Repeat("i", 100)}
	}
	out := CleanOptions(in)
	if len(out) != MaxAttentionOptions || len([]rune(out[0].Label)) != MaxOptionLabel || len(out[0].Input) != MaxOptionInput {
		t.Fatalf("%d options, label %d, input %d", len(out), len([]rune(out[0].Label)), len(out[0].Input))
	}
	if CleanOptions([]Option{{Label: "", Input: "1"}, {Label: "x", Input: ""}}) != nil {
		t.Fatal("empty label or input must be dropped")
	}
	if CleanOptions(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

func activityTypes(s *Local) map[string]int {
	out := map[string]int{}
	for _, e := range s.Activity() {
		out[e.Type]++
	}
	return out
}

func TestInputDuringNeedsInputRecordsOneAnswer(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	sink := newChanSink(false)
	sub, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Priya", Cols: 80, Rows: 24}, sink)
	s.SetAttention(AttentionNeedsInput, "Apply edit?", SourceAPI)
	_ = s.AnswerInput(t.Context(), sub, s.Info().Attention.ID, []byte("1"))
	_ = s.Input(sub, []byte("\r"))
	info := s.Info()
	if info.LastAnswer == nil || info.LastAnswer.ByName != "Priya" || info.LastAnswer.Message != "Apply edit?" || info.LastAnswer.At.IsZero() {
		t.Fatalf("lastAnswer: %+v", info.LastAnswer)
	}
	if got := activityTypes(s); got["input"] != 1 || got["join"] != 1 || got["attention"] < 1 {
		t.Fatalf("activity types: %v", got)
	}
}

// reactingProc answers a write at once, before Write returns, the way an echo
// does.
type reactingProc struct {
	*fakeProc
	react func()
}

func (r *reactingProc) Write(b []byte) (int, error) {
	n, err := r.fakeProc.Write(b)
	r.react()
	return n, err
}

// Typing answers the prompt that was on the screen when it was typed. A process
// that reacts before the write returns, cat echoing a bell for one, may raise
// the next prompt in that time, and that one is not answered yet.
func TestInputDoesNotClearAPromptItsOwnOutputRaised(t *testing.T) {
	fp := newFakeProc()
	sink := newChanSink(false)
	reacted := false // Input, and so react, runs on this goroutine only
	p := &reactingProc{fakeProc: fp}
	p.react = func() {
		if reacted {
			return
		}
		reacted = true
		fp.outW.Write([]byte("\a")) // returns once the pump has read it
		// The prompt the bell raises reaches the viewer as a frame: the
		// session has applied it before Input goes on.
		if waitControl(sink, func(m map[string]any) bool {
			return m["t"] == proto.CtlAttention && m["state"] == string(AttentionNeedsInput)
		}) == nil {
			t.Error("the bell raised no prompt")
		}
	}
	s := NewLocal(Info{ID: "sess", Cwd: t.TempDir(), Cols: 80, Rows: 24}, p, Options{})
	t.Cleanup(fp.exit)
	sub, err := s.Attach("", RoleControl, "", 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Input(sub, []byte("\n")); err != nil {
		t.Fatal(err)
	}
	if att := s.Info().Attention; att.State != AttentionNeedsInput || att.Source != SourceBell {
		t.Fatalf("typing cleared the prompt its own output raised: %+v", att)
	}
	// The next line entered withdraws the bell's wait.
	if err := s.Input(sub, []byte("y\r")); err != nil {
		t.Fatal(err)
	}
	if att := s.Info().Attention; att.State != AttentionNone {
		t.Fatalf("the line left %+v", att)
	}
}

func TestLinkViewersCountsPerLink(t *testing.T) {
	s, _ := newLocal(t, t.TempDir())
	a, b, c := newChanSink(false), newChanSink(false), newChanSink(false)
	s.Attach("", RoleView, "L1", 80, 24, a)
	s.Attach("", RoleView, "L1", 80, 24, b)
	sub, _ := s.Attach("", RoleControl, "", 80, 24, c)
	got := s.LinkViewers()
	if got["L1"] != 2 || len(got) != 1 {
		t.Fatalf("LinkViewers: %v", got)
	}
	s.Detach(sub)
	if got := s.LinkViewers(); got["L1"] != 2 {
		t.Fatalf("after detaching the owner: %v", got)
	}
}

func TestConcurrentAnswersRecordExactlyOne(t *testing.T) {
	for round := 0; round < 20; round++ {
		s, _ := newLocal(t, t.TempDir())
		a, b := newChanSink(false), newChanSink(false)
		subA, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Priya", Cols: 80, Rows: 24}, a)
		subB, _ := s.AttachWith(AttachOptions{Role: RoleControl, Name: "Marco", Cols: 80, Rows: 24}, b)
		s.SetAttention(AttentionNeedsInput, "Apply edit?", SourceAPI)
		q := s.Info().Attention.ID
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; _ = s.AnswerInput(t.Context(), subA, q, []byte("1")) }()
		go func() { defer wg.Done(); <-start; _ = s.AnswerInput(t.Context(), subB, q, []byte("1")) }()
		close(start)
		wg.Wait()
		if got := activityTypes(s)["input"]; got != 1 {
			t.Fatalf("round %d: %d input entries, want exactly one", round, got)
		}
		info := s.Info()
		if info.LastAnswer == nil || info.Attention.State != AttentionNone {
			t.Fatalf("round %d: lastAnswer %+v state %q", round, info.LastAnswer, info.Attention.State)
		}
		// The recorded answer names the same person as lastAnswer.
		for _, e := range s.Activity() {
			if e.Type == ActivityInput && e.ByName != info.LastAnswer.ByName {
				t.Fatalf("round %d: entry by %q but lastAnswer by %q", round, e.ByName, info.LastAnswer.ByName)
			}
		}
	}
}

// inputEntries returns the input entries of the activity log, oldest first.
func inputEntries(s *Local) []ActivityEntry {
	var out []ActivityEntry
	for _, e := range s.Activity() {
		if e.Type == ActivityInput {
			out = append(out, e)
		}
	}
	return out
}

// LastOutputAt is when the process last wrote output: zero before it has.
func TestLastOutputAtFollowsTheOutput(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	if at := s.LastOutputAt(); !at.IsZero() {
		t.Fatalf("no output yet, but LastOutputAt %v", at)
	}
	waitAfter := func(after time.Time) time.Time {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if at := s.LastOutputAt(); at.After(after) {
				return at
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("LastOutputAt stayed at %v", s.LastOutputAt())
		return time.Time{}
	}
	before := time.Now()
	p.outW.Write([]byte("hello"))
	first := waitAfter(before.Add(-time.Nanosecond))
	if first.Before(before) || first.After(time.Now()) {
		t.Fatalf("LastOutputAt %v, output written after %v", first, before)
	}
	time.Sleep(20 * time.Millisecond)
	p.outW.Write([]byte(" again"))
	if second := waitAfter(first); second.Sub(first) < 20*time.Millisecond {
		t.Fatalf("second output at %v, first at %v", second, first)
	}
}

// A hello's size: a controller's two dimensions in range set the PTY's, as
// they always did; (0, 0) follows it, and so does an out-of-range pair; a
// viewer's never changes it. The welcome carries the size that holds.
func TestAHelloOfZeroFollowsTheSize(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	for _, tc := range []struct {
		role       Role
		cols, rows uint16
		resized    bool
	}{
		{RoleControl, 0, 0, false},
		{RoleControl, 0, 40, false},
		{RoleControl, 501, 40, false},
		{RoleView, 148, 57, false},
		{RoleControl, 148, 57, true},
		{RoleControl, 0, 0, false},
	} {
		sink := newChanSink(false)
		if _, err := s.Attach("", tc.role, "", tc.cols, tc.rows, sink); err != nil {
			t.Fatal(err)
		}
		sink.waitFrames(t, 1)
		w := decodeControl(t, sink.frame(0))
		select {
		case got := <-p.resize:
			if !tc.resized || got != [2]uint16{tc.cols, tc.rows} {
				t.Fatalf("%s %dx%d resized the PTY to %v", tc.role, tc.cols, tc.rows, got)
			}
		case <-time.After(20 * time.Millisecond):
			if tc.resized {
				t.Fatalf("%s %dx%d did not resize the PTY", tc.role, tc.cols, tc.rows)
			}
		}
		info := s.Info()
		if w["cols"] != float64(info.Cols) || w["rows"] != float64(info.Rows) {
			t.Fatalf("welcome %v, session %dx%d", w, info.Cols, info.Rows)
		}
	}
	if info := s.Info(); info.Cols != 148 || info.Rows != 57 {
		t.Fatalf("size %dx%d", info.Cols, info.Rows)
	}
}

// A link's viewers are evicted as revoked by a revoke, while the session
// runs and once it has ended (a run's link keeps its viewers on a member
// that ended, until it is revoked); the session's end evicts the viewers of
// its own links as its end (EndLink), and the close must say so.
func TestDisconnectLinkSaysWhyOnceTheSessionEnded(t *testing.T) {
	s, p := newLocal(t, t.TempDir())
	live := newChanSink(false)
	subLive, _ := s.AttachWith(AttachOptions{Role: RoleView, LinkID: "l1", Cols: 80, Rows: 24}, live)
	s.DisconnectLink("l1")
	if !errors.Is(subLive.Reason(), ErrRevoked) {
		t.Fatalf("while running: %v", subLive.Reason())
	}
	ended := newChanSink(false)
	subEnded, _ := s.AttachWith(AttachOptions{Role: RoleView, LinkID: "l2", Cols: 80, Rows: 24}, ended)
	revoked := newChanSink(false)
	subRevoked, _ := s.AttachWith(AttachOptions{Role: RoleView, LinkID: "run", Cols: 80, Rows: 24}, revoked)
	p.exit()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !s.Info().Status.Ended() {
		time.Sleep(10 * time.Millisecond)
	}
	s.EndLink("l2")
	if !errors.Is(subEnded.Reason(), ErrSessionEnded) {
		t.Fatalf("the end's: %v", subEnded.Reason())
	}
	s.DisconnectLink("run")
	if !errors.Is(subRevoked.Reason(), ErrRevoked) {
		t.Fatalf("a revoke once ended: %v", subRevoked.Reason())
	}
}

// The welcome names the session's working directory, from the owner itself,
// to a viewer whose role may read files: a guest's Files pane roots there,
// whatever a switchyard lists. A viewer who may read none is not told, and a
// path past the protocol's bound is left out.
func TestTheWelcomeNamesTheSessionsFolder(t *testing.T) {
	welcome := func(fileView string, role Role) map[string]any {
		t.Helper()
		s, _ := newLocalWith(t, Options{FileView: fileView})
		sink := newChanSink(false)
		if _, err := s.Attach("", role, "", 80, 24, sink); err != nil {
			t.Fatal(err)
		}
		sink.waitFrames(t, 1)
		m := decodeControl(t, sink.frame(0))
		if m["t"] != proto.CtlWelcome {
			t.Fatalf("first frame %v", m)
		}
		m["want"] = s.Info().Cwd
		return m
	}
	if m := welcome("view", RoleView); m["cwd"] != m["want"] || m["cwd"] == "" {
		t.Fatalf("file view for all, a view link: %v", m)
	}
	if m := welcome("control", RoleView); m["cwd"] != nil {
		t.Fatalf("file view for control, a view link: %v", m)
	}
	if m := welcome("control", RoleControl); m["cwd"] != m["want"] {
		t.Fatalf("file view for control, a control link: %v", m)
	}
	if m := welcome("off", RoleControl); m["cwd"] != nil {
		t.Fatalf("file view off: %v", m)
	}
	if got := welcomeCwd("/"+strings.Repeat("a", proto.MaxCwd), true); got != "" {
		t.Fatalf("an over-long folder went in the welcome: %d bytes", len(got))
	}
	// The bound is on the folder as the welcome encodes it: two quotes, and
	// six bytes for each `&`.
	if got := welcomeCwd("/srv/"+strings.Repeat("a", proto.MaxCwd-7), true); len(got) != proto.MaxCwd-2 {
		t.Fatalf("a folder at the bound was left out: %d bytes", len(got))
	}
	amps := "/srv/" + strings.Repeat(strings.Repeat("&", 200)+"/", 7)
	if len(amps) > 1500 || welcomeCwd(amps, true) != "" {
		t.Fatalf("a %d-byte folder that encodes past the bound went in the welcome", len(amps))
	}
	// The longest folder a welcome carries keeps it within one CONTROL frame.
	widest := "/" + strings.Repeat("a", proto.MaxCwd-3)
	s, _ := newLocalWith(t, Options{FileView: "view"})
	s.info.Cwd = widest
	sink := newChanSink(false)
	if _, err := s.Attach(strings.Repeat("v", 16), RoleControl, "", 80, 24, sink); err != nil {
		t.Fatal(err)
	}
	sink.waitFrames(t, 1)
	f, err := proto.Decode(sink.frame(0))
	if err != nil || len(f.Payload) > proto.MaxControl {
		t.Fatalf("the widest welcome: %d bytes, %v", len(f.Payload), err)
	}
	if m := decodeControl(t, sink.frame(0)); m["cwd"] != widest {
		t.Fatalf("the widest folder was left out: %v", m["t"])
	}
}
