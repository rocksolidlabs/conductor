package hostagent

import (
	"regexp"
	"sync"
	"testing"
	"time"

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
	h := &awayHost{hs: d.(*signal.HostedSession), local: local, proc: proc}
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

// A done the server forwards over a done shown leaves its copy showing
// what the host's session shows. For an agent that reports only its done
// it ends another turn: the host's session shows it as a new done, and
// the copy shows it by the host's id. For an agent whose hooks told a
// turn's start (a working) it is the same turn's: the host's session
// keeps the first, its id, Since and words, and records the second as its
// own entry; the copy keeps the first, by the host's id.
func TestTheCopyShowsTheHostsDoneOverADone(t *testing.T) {
	for _, tc := range []struct {
		name string
		// starts: a hook's working is forwarded before the first done.
		starts bool
	}{
		{"an agent that reports only its done", false},
		{"an agent whose hooks tell its turns' starts", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts := startServer(t)
			_, local, conn, registered, serve, _ := hostedOver(t, ts.URL, nil)
			serve(conn)
			d, ok := srv.Registry().Get(registered.SessionID)
			if !ok {
				t.Fatal("the hosted session is not listed")
			}
			hs := d.(*signal.HostedSession)
			forward := func(state session.AttentionState, msg, kind string) {
				t.Helper()
				if err := hs.SetAttentionFull(state, msg, session.SourceAPI, kind, nil, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.starts {
				forward(session.AttentionWorking, "", "")
				untilListed(t, local, "the host's working", func(i session.Info) bool { return i.Attention.State == session.AttentionWorking })
			}
			const a, b = "Refactored the parser.", "Docs updated."
			forward(session.AttentionDone, a, session.KindDone)
			untilListed(t, local, "the host's first done", func(i session.Info) bool { return i.Attention.Message == a })
			first := local.Info().Attention
			untilListed(t, hs, "the copy's first done", func(i session.Info) bool { return i.Attention.ID == first.ID })
			forward(session.AttentionDone, b, session.KindDone)
			// The host's session has weighed it once its entry is recorded.
			untilLogged(t, local, b)
			want := first
			if !tc.starts {
				untilListed(t, local, "the host's next done", func(i session.Info) bool { return i.Attention.Message == b })
				if want = local.Info().Attention; want.ID == first.ID {
					t.Fatalf("the next turn's done kept the first's id: %+v", want)
				}
			}
			if got := local.Info().Attention; got.ID != want.ID || got.Message != want.Message || !got.Since.Equal(*want.Since) {
				t.Fatalf("the host's session shows %+v, want %+v", got, want)
			}
			untilListed(t, hs, "the copy", func(i session.Info) bool { return i.Attention.ID == want.ID && i.Attention.Message == want.Message })
		})
	}
}

// untilLogged waits up to 10 s for local's activity to hold an attention
// entry with msg.
func untilLogged(t *testing.T, local *session.Local, msg string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		for _, e := range local.Activity() {
			if e.Type == session.ActivityAttention && e.Message == msg {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q was never recorded", msg)
		}
	}
}

// sentAttention is an agent whose control connection is a test's: every
// attention message it sends goes to the channel it returns, and each
// waits for the hold its setHold set, if any, to return. The hold is read
// before the message is handed to the test: a hold set after the test saw
// a message holds only the sends after that one.
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
			mu.Lock()
			h := hold
			mu.Unlock()
			sent <- m
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
