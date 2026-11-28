package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// A move noted at a launch (NoteWay, issue 1174): refused where a press of
// the switch is refused, else noted with nothing started, and made by the
// noted move's own path once the first turn has ended.

// launched is a server session in its terminal whose agent has had no turn,
// on the conversation conv-1, as a launch leaves it.
func launched(t *testing.T, info Info) *Local {
	t.Helper()
	p := newFakeProc()
	info.ID, info.Cwd, info.Cols, info.Rows = "sess", realDir(t, t.TempDir()), 80, 24
	if info.Kind == "" {
		info.Kind = KindServer
	}
	info.AgentSession = &AgentSession{ID: "conv-1", Resumable: true}
	s := NewLocal(info, p, Options{ScrollbackBytes: 4096})
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		p.exit()
	})
	return s
}

func TestNoteWayRefusesWhatAMoveRefuses(t *testing.T) {
	ts := &terminals{}
	ts.cleanup(t)
	starts := MoveStarts{Terminal: ts.start, Protocol: func(context.Context, string) (Child, Protocol, error) {
		return nil, nil, errors.New("not here")
	}}
	for _, tc := range []struct {
		name  string
		info  Info
		m     MoveStarts
		words string
	}{
		{"a crew member", Info{Crew: &CrewRef{RunID: "run-1", Member: "writer"}}, starts, CrewStaysWords},
		{"a hosted session", Info{Kind: KindHosted}, starts, HostedStaysWords},
		{"no chat to start", Info{}, MoveStarts{Terminal: ts.start}, CannotMoveWords},
	} {
		s := launched(t, tc.info)
		_, err := s.NoteWay(WayProtocol, tc.m)
		var refusal *MoveRefusal
		if !errors.As(err, &refusal) || refusal.Words != tc.words {
			t.Fatalf("%s: %v, want %q", tc.name, err, tc.words)
		}
		if to, _ := s.Noted(); to != "" {
			t.Fatalf("%s: noted %q", tc.name, to)
		}
	}
	if n := len(ts.started()); n != 0 {
		t.Fatalf("%d terminals started", n)
	}
}

// Noted before the first turn, with its sentence; nothing starts until the
// first turn's done, which makes the move through the move's own path (its
// chat's start, which here fails and goes back to the terminal).
func TestNoteWayIsMadeAtTheFirstTurnsEnd(t *testing.T) {
	ts := &terminals{}
	ts.cleanup(t)
	var chats atomic.Int32
	m := MoveStarts{Terminal: ts.start, Protocol: func(context.Context, string) (Child, Protocol, error) {
		chats.Add(1)
		return nil, nil, errors.New("not here")
	}}
	s := launched(t, Info{})
	st, err := s.NoteWay(WayProtocol, m)
	if err != nil || st.Noted != WayProtocol || st.Why != AfterFirstReplyWords || st.Moving {
		t.Fatalf("the note: %+v %v", st, err)
	}
	s.SetAttention(AttentionWorking, "", SourceAPI)
	if s.Moving() || chats.Load() != 0 {
		t.Fatal("a move began during the first turn")
	}
	if _, why := s.Noted(); why != AfterFirstReplyWords {
		t.Fatalf("during the first turn: %q", why)
	}
	s.SetAttention(AttentionDone, "a turn of its own", SourceAPI)
	settled(t, s)
	if chats.Load() != 1 {
		t.Fatalf("%d chats started at the first turn's end", chats.Load())
	}
	if to, _ := s.Noted(); to != "" || s.Way() != WayTerminal || s.Info().Status.Ended() {
		t.Fatalf("after the move went back: noted %q, way %q, status %q", to, s.Way(), s.Info().Status)
	}
}
