package hostagent

import (
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/api"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// workingTitle is the window title the sessions of these tests read as
// their agent's working (Options.WorkingTitle).
var workingTitle = regexp.MustCompile(`^working`)

// awayHost is a host's session served by a test server, its connection
// ended and the server's copy showing its host gone.
type awayHost struct {
	srv   *api.Server
	base  string
	hs    *signal.HostedSession
	local *session.Local
	proc  *sessiontest.FakeProc
	// back serves the host's next connection, a resume.
	back func()
}

func hostAway(t *testing.T, before func(h *awayHost)) *awayHost {
	t.Helper()
	srv, ts := startServer(t)
	a, local, conn, registered, serve, proc := hostedOver(t, ts.URL, workingTitle)
	served := serve(conn)
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	h := &awayHost{srv: srv, base: ts.URL, hs: d.(*signal.HostedSession), local: local, proc: proc}
	h.back = func() {
		t.Helper()
		conn2, again, err := a.dialAndRegister(t.Context())
		if err != nil || !again.Resumed {
			t.Fatalf("resume: %v %+v", err, again)
		}
		t.Cleanup(func() { conn2.CloseNow() })
		serve(conn2)
	}
	if before != nil {
		before(h)
	}
	conn.CloseNow()
	<-served
	untilListed(t, d, "the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
	return h
}

// A report the server forwards while the host has no connection is kept,
// the latest one, and handed to the host as its next connection starts:
// the host's session applies it, and the server's copy shows what the
// host's session then shows, its id, words and turns.
func TestAReportForwardedWhileTheHostIsAwayReachesItsSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reports []session.AttentionState
	}{
		{"a done", []session.AttentionState{session.AttentionDone}},
		{"a working, then a done", []session.AttentionState{session.AttentionWorking, session.AttentionDone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := hostAway(t, nil)
			for _, state := range tc.reports {
				msg, kind := "", ""
				if state == session.AttentionDone {
					msg, kind = "All tests pass.", session.KindDone
				}
				if err := h.hs.SetAttentionFull(state, msg, session.SourceAPI, kind, nil, true); err != nil {
					t.Fatal(err)
				}
			}
			h.back()
			untilListed(t, h.local, "the host's session", func(i session.Info) bool {
				return i.Attention.State == session.AttentionDone && i.Attention.Message == "All tests pass." && i.Turns
			})
			id := h.local.Info().Attention.ID
			untilListed(t, h.hs, "the server's copy", func(i session.Info) bool {
				return i.Status == session.StatusRunning && i.Attention.ID == id && i.Attention.Message == "All tests pass." && i.Turns
			})
		})
	}
}

// The agent's question forwarded while the host is away reaches the
// host's session as its connection comes back, and the copy names it by
// the host's id: a page's answer to it is typed into the agent.
func TestAQuestionForwardedWhileTheHostIsAwayIsAnsweredFromAPage(t *testing.T) {
	h := hostAway(t, nil)
	choices := []session.Option{{Label: "Yes", Input: "1"}, {Label: "No", Input: "3"}}
	if err := h.hs.Ask("Allow Bash: go test ./...?", session.SourceAPI, session.KindPermission, choices); err != nil {
		t.Fatal(err)
	}
	h.back()
	untilListed(t, h.local, "the host's session", func(i session.Info) bool {
		return i.Attention.State == session.AttentionNeedsInput && i.Attention.Message == "Allow Bash: go test ./...?"
	})
	q := h.local.Info().Attention.ID
	untilListed(t, h.hs, "the server's copy", func(i session.Info) bool { return i.Attention.ID == q })
	v := relayControllerSeeing(t, h.base, h.hs.Info().ID, "")
	v.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "a1", Question: q, Input: "1"}))
	deadline := time.After(10 * time.Second)
	for {
		select {
		case b := <-h.proc.Input:
			if string(b) == "1" {
				return
			}
		case <-deadline:
			t.Fatal("the page's answer was never typed")
		}
	}
}

// A report kept for a session that left the server's registry goes with
// it: the host that comes back is not handed it, and a report forwarded
// after is.
func TestAReportKeptForASessionThatLeftIsNotHanded(t *testing.T) {
	h := hostAway(t, nil)
	if err := h.hs.SetAttentionFull(session.AttentionDone, "All tests pass.", session.SourceAPI, session.KindDone, nil, true); err != nil {
		t.Fatal(err)
	}
	h.srv.Registry().Remove(h.hs.Info().ID)
	h.back()
	untilListed(t, h.hs, "the host back", func(i session.Info) bool { return i.Status == session.StatusRunning })
	if err := h.hs.SetAttentionFull(session.AttentionWorking, "", session.SourceAPI, "", nil, true); err != nil {
		t.Fatal(err)
	}
	// The working went behind where the kept done would have: once the
	// host's session shows it, the done would have been applied.
	untilListed(t, h.local, "the later report", func(i session.Info) bool { return i.Attention.State == session.AttentionWorking })
	for _, e := range h.local.Activity() {
		if e.Type == session.ActivityAttention && e.Message == "All tests pass." {
			t.Fatalf("the kept report reached the host: %+v", e)
		}
	}
}

// A kept report is the server's memory alone: a server that restarted
// knows nothing of it, and the host registers afresh there with what its
// session shows. The copy and its host agree; the report is lost, and the
// agent's next report is what tells.
func TestAReportKeptByAServerThatRestartedIsLost(t *testing.T) {
	restarted, ts2 := startServer(t)
	srv, ts := startServer(t)
	a, local, conn, registered, serve, _ := hostedOver(t, ts.URL, nil)
	served := serve(conn)
	d, _ := srv.Registry().Get(registered.SessionID)
	conn.CloseNow()
	<-served
	untilListed(t, d, "the host gone", func(i session.Info) bool { return i.Status == session.StatusHostDisconnected })
	if err := d.(*signal.HostedSession).SetAttentionFull(session.AttentionDone, "All tests pass.", session.SourceAPI, session.KindDone, nil, true); err != nil {
		t.Fatal(err)
	}
	wsURL, err := controlURL(ts2.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.wsURL = wsURL
	a.mu.Unlock()
	if _, _, err := a.dialAndRegister(t.Context()); err == nil {
		t.Fatal("the restarted server resumed a session it never knew")
	}
	conn2, again, err := a.dialAndRegister(t.Context())
	if err != nil || again.Resumed {
		t.Fatalf("registered afresh: %v %+v", err, again)
	}
	t.Cleanup(func() { conn2.CloseNow() })
	serve(conn2)
	d2, ok := restarted.Registry().Get(again.SessionID)
	if !ok {
		t.Fatal("the new copy is not listed")
	}
	untilListed(t, d2, "the new copy", func(i session.Info) bool {
		return i.Status == session.StatusRunning && i.Attention.State == local.Info().Attention.State
	})
	if s := local.Info().Attention.State; s == session.AttentionDone {
		t.Fatalf("the host's session shows %q", s)
	}
}

// A prompt typed into the host's session during a turn makes the done
// after that turn's own the next turn's there: the server forwards both
// dones, its copy keeps the first while it waits for the host's word, and
// then shows the second with the id the host's session gave it.
func TestAPromptQueuedOnTheHostMakesTheNextDoneNew(t *testing.T) {
	srv, ts := startServer(t)
	_, local, conn, registered, serve, proc := hostedOver(t, ts.URL, nil)
	serve(conn)
	d, ok := srv.Registry().Get(registered.SessionID)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	hs := d.(*signal.HostedSession)
	sub, err := local.Attach("", session.RoleControl, "", 80, 24, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Detach(sub) })
	// Two prompts, the second typed while the first's turn runs; each key
	// waited for as the session writes it, past the step that weighs it.
	for _, keys := range []string{"fix the parser", "\r", "and update the docs", "\r"} {
		if err := local.Input(sub, []byte(keys)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-proc.Input:
		case <-time.After(10 * time.Second):
			t.Fatalf("%q was never written", keys)
		}
	}
	report := func(msg string) session.Attention {
		t.Helper()
		if err := hs.SetAttentionFull(session.AttentionDone, msg, session.SourceAPI, session.KindDone, nil, true); err != nil {
			t.Fatal(err)
		}
		untilListed(t, local, "the host's session", func(i session.Info) bool { return i.Attention.Message == msg })
		att := local.Info().Attention
		untilListed(t, hs, "the server's copy", func(i session.Info) bool { return i.Attention.ID == att.ID && i.Attention.Message == msg })
		return att
	}
	first := report("Refactored the parser.")
	if second := report("Docs updated."); second.ID == first.ID || second.Since.Before(*first.Since) {
		t.Fatalf("the next turn's done %+v after %+v", second, first)
	}
}

// sentAttention is an agent whose control connection is a test's: every
// attention message it sends goes to the channel it returns, and each
// waits for the hold its setHold set, if any, to return.
func sentAttention(t *testing.T) (a *agent, proc *sessiontest.FakeProc, sent chan proto.HostAttentionMsg, setHold func(func(proto.HostAttentionMsg))) {
	t.Helper()
	proc = sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	sent = make(chan proto.HostAttentionMsg, 16)
	var hold func(proto.HostAttentionMsg)
	var mu sync.Mutex
	a = &agent{opts: Options{}, peers: map[string]*peer{}, log: discardLog, sessID: "s"}
	a.sendHook = func(v any) {
		if m, ok := v.(proto.HostAttentionMsg); ok {
			sent <- m
			mu.Lock()
			h := hold
			mu.Unlock()
			if h != nil {
				h(m)
			}
		}
	}
	a.local = session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{FileEdit: "off", Log: discardLog, OnChange: a.noteChange, WorkingTitle: workingTitle})
	setHold = func(h func(proto.HostAttentionMsg)) {
		mu.Lock()
		hold = h
		mu.Unlock()
	}
	return a, proc, sent, setHold
}

// nextSent is the next attention message sent within 10 s.
func nextSent(t *testing.T, sent <-chan proto.HostAttentionMsg, what string) proto.HostAttentionMsg {
	t.Helper()
	select {
	case m := <-sent:
		return m
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: nothing sent", what)
		return proto.HostAttentionMsg{}
	}
}

// learnTurnsAlone has the session learn Info.Turns and nothing else: an
// operator's working shows, and the agent's window title says it works,
// which the title's rules keep out (it shows working already).
func learnTurnsAlone(t *testing.T, local *session.Local, proc *sessiontest.FakeProc) {
	t.Helper()
	if err := proc.Print("\x1b]0;working\a"); err != nil {
		t.Fatal(err)
	}
	untilListed(t, local, "turns learned", func(i session.Info) bool { return i.Turns })
	if a := local.Info().Attention; a.Source != session.SourceAdmin {
		t.Fatalf("the title changed the attention: %+v", a)
	}
}

// A change of Info.Turns alone is told the server, as it happens.
func TestTheHostTellsTurnsLearnedAlone(t *testing.T) {
	a, proc, sent, _ := sentAttention(t)
	a.local.SetAttentionFull(session.AttentionWorking, "", session.SourceAdmin, "", nil)
	if m := nextSent(t, sent, "the operator's working"); m.Turns || m.Source != session.SourceAdmin {
		t.Fatalf("told %+v", m)
	}
	learnTurnsAlone(t, a.local, proc)
	if m := nextSent(t, sent, "turns alone"); !m.Turns || m.Source != session.SourceAdmin || m.State != string(session.AttentionWorking) {
		t.Fatalf("told %+v", m)
	}
}

// A change of Info.Turns alone made while a retry is under way is told
// once the retry's send is through: the retry sends again.
func TestARetryTellsTurnsLearnedMeanwhile(t *testing.T) {
	a, proc, sent, setHold := sentAttention(t)
	a.local.SetAttentionFull(session.AttentionWorking, "", session.SourceAdmin, "", nil)
	nextSent(t, sent, "the operator's working")
	release := make(chan struct{})
	held := make(chan struct{})
	var once sync.Once
	setHold(func(proto.HostAttentionMsg) {
		once.Do(func() {
			close(held)
			<-release
		})
	})
	a.retryAttention()
	if m := nextSent(t, sent, "the retry"); m.Turns {
		t.Fatalf("the retry told %+v", m)
	}
	<-held
	learnTurnsAlone(t, a.local, proc)
	// Noted while the retry is under way (onLocalChange), not sent.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		a.mu.Lock()
		noted := a.lastTurns
		a.mu.Unlock()
		if noted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turns was never noted")
		}
	}
	close(release)
	if m := nextSent(t, sent, "the retry again"); !m.Turns || m.Source != session.SourceAdmin {
		t.Fatalf("the retry then told %+v", m)
	}
}

// A host whose session learned Info.Turns alone while it was away tells it
// as its next connection starts: the server's copy learns it, and shows
// what it showed, its stamp too.
func TestAConnectionsStartTellsTurnsLearnedWhileAway(t *testing.T) {
	h := hostAway(t, func(h *awayHost) {
		h.local.SetAttentionFull(session.AttentionWorking, "", session.SourceAdmin, "", nil)
		untilListed(t, h.hs, "the operator's working", func(i session.Info) bool {
			return i.Attention.State == session.AttentionWorking && i.Attention.Source == session.SourceAdmin
		})
		if h.hs.Info().Turns {
			t.Fatal("the copy reports turns already")
		}
	})
	since := *h.hs.Info().Attention.Since
	learnTurnsAlone(t, h.local, h.proc)
	h.back()
	untilListed(t, h.hs, "the copy", func(i session.Info) bool { return i.Status == session.StatusRunning && i.Turns })
	if a := h.hs.Info().Attention; a.Source != session.SourceAdmin || a.State != session.AttentionWorking || !a.Since.Equal(since) {
		t.Fatalf("the copy shows %+v, stamped %v before", a, since)
	}
}
