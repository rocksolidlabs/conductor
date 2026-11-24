package sessiontest

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session/internal/way"
)

// recorder is the session's side of the driver, recording what it is told.
type recorder struct {
	way.ProtocolEvents
	got []string
}

func (r *recorder) Notice(code, words string) { r.got = append(r.got, code+":"+words) }

// A FakeProtocol hands its events to Read in order, each applied before
// Hand returns; its child's Stop ends Read with io.EOF, after which Hand
// reports the event was not taken; its calls are kept in order.
func TestFakeProtocolHandsEventsInOrder(t *testing.T) {
	f := NewFakeProtocol()
	rec := &recorder{}
	read := make(chan error, 1)
	go func() { read <- f.Read(context.Background(), rec) }()
	if !f.Notice("a", "one") || !f.Notice("b", "two") {
		t.Fatal("an event was not taken")
	}
	if strings.Join(rec.got, " ") != "a:one b:two" {
		t.Fatalf("got %v", rec.got)
	}
	_ = f.CloseInput()
	_ = f.Child().Stop(context.Background(), 0)
	if err := <-read; !errors.Is(err, io.EOF) {
		t.Fatalf("Read = %v", err)
	}
	if f.Notice("c", "three") {
		t.Fatal("an event was taken after the end")
	}
	if got := strings.Join(f.Calls(), " "); got != "close_input stop" {
		t.Fatalf("calls %s", got)
	}
	if f.Child().Exit().Code != -1 {
		t.Fatalf("exit %d", f.Child().Exit().Code)
	}
}

// Fail ends Read with its error; Start hands the drivers in turn, then fails.
func TestFakeProtocolFailAndStart(t *testing.T) {
	f, g := NewFakeProtocol(), NewFakeProtocol()
	boom := errors.New("not the protocol's")
	f.Fail(boom)
	if err := f.Read(context.Background(), &recorder{}); !errors.Is(err, boom) {
		t.Fatalf("Read = %v", err)
	}
	start := f.Start(g)
	for _, want := range []*FakeProtocol{f, g} {
		_, d, err := start(context.Background(), "conv-1")
		if err != nil || d != want || want.StartedOn() != "conv-1" {
			t.Fatalf("start = %v, %v", d, err)
		}
	}
	if _, _, err := start(context.Background(), ""); err == nil {
		t.Fatal("a third start succeeded")
	}
}
