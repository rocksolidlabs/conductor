package hostagent

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// An entry written on a connection that ends as it goes, before the
// sender marks it sent, is taken as the registration that began meanwhile
// takes the entries sent before it: sent again, late, to a session the
// server resumed; counted left out by one it did not. Never marked sent
// for a connection that did not carry it.
func TestAnEntryWrittenAsTheConnectionEndsIsNotLost(t *testing.T) {
	for _, resumed := range []bool{true, false} {
		a, got := sentKept(t)
		hook := a.sendHook
		once := true
		a.sendHook = func(v any) {
			hook(v)
			if once {
				once = false
				// The connection ends and the next registration runs before
				// the sender marks what it wrote.
				a.registeredActivity(true, resumed)
			}
		}
		a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "edge"})
		first := <-got
		a.actMu.Lock()
		kept := slices.Clone(a.actKept)
		left, epoch := a.actLeftOut, a.actEpoch
		a.actMu.Unlock()
		if resumed {
			// Sent again, late, on the new connection, and held as sent there.
			select {
			case again := <-got:
				if again.Seq != first.Seq || !again.Late || first.Late {
					t.Fatalf("resumed: sent %+v, then %+v", first, again)
				}
			default:
				t.Fatal("resumed: not sent again")
			}
			if len(kept) != 1 || !kept[0].sent || kept[0].on != epoch || !kept[0].late {
				t.Fatalf("resumed: kept %+v", kept)
			}
		} else if len(kept) != 0 || left != 1 || len(got) != 0 {
			t.Fatalf("not resumed: kept %+v, left out %d, sent again %d", kept, left, len(got))
		}
	}
}

// progressEntry is an agent's progress event of words, as the session
// records it.
func progressEntry(words string) session.ActivityEntry {
	return session.ActivityEntry{Type: session.ActivityProgress, Message: words}
}

// nextKept is the next activity message the agent sent.
func nextKept(t *testing.T, got chan proto.HostActivityMsg, what string) proto.HostActivityMsg {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(30 * time.Second):
		t.Fatalf("%s: nothing sent", what)
		return proto.HostActivityMsg{}
	}
}

// untilConfirmed waits for no confirmation to run.
func untilConfirmed(t *testing.T, a *agent) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(time.Millisecond) {
		a.actMu.Lock()
		running := a.actConfirming
		a.actMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the confirmation never ended")
		}
	}
}

// Entries the bound pushes out after their write on the connection the host
// is on are not counted left out as they go, nor what they carried. Once
// the server's taking of them is confirmed on that connection, nothing is
// counted for them; when the connection ends first, the next registration
// counts them, they and what they carried, resumed or not, and the next
// entry sent says so. A confirmation that covers only some of them counts
// for none.
func TestEntriesPushedOutAfterTheirWriteAreCountedOnlyUntilTaken(t *testing.T) {
	// setup leaves the host with entry 4 kept, sent and its confirmation
	// asked; entries 2 (which carried the count of entry 1, gone unsent)
	// and 3 pushed out after their write; the first answer (up to 2) given
	// true.
	setup := func(t *testing.T) (*agent, chan proto.HostActivityMsg, chan bool) {
		a, got := sentKept(t)
		a.actBound = 1
		asked := make(chan struct{}, 8)
		answers := make(chan bool)
		// A confirmation asked after the test's end is answered: not taken.
		t.Cleanup(func() { close(answers) })
		a.actConfirm = func() bool {
			asked <- struct{}{}
			return <-answers
		}
		awaitAsked := func() {
			t.Helper()
			select {
			case <-asked:
			case <-time.After(30 * time.Second):
				t.Fatal("no confirmation was asked")
			}
		}
		// Away: nothing is written; entry 1 goes past the bound, unsent.
		hook := a.sendHook
		a.sendHook = nil
		a.sendNow(progressEntry("x"))
		a.sendNow(progressEntry("y"))
		a.sendHook = hook
		// Back on the resumed session: 2 is sent with the count of 1.
		a.registeredActivity(true, true)
		a.sendKeptActivity()
		if m := nextKept(t, got, "the kept entry"); m.Seq != 2 || m.LeftOut != 1 {
			t.Fatalf("sent %+v, want entry 2 with one left out", m)
		}
		awaitAsked() // its confirmation, up to 2, held
		for i, w := range []string{"b", "c"} {
			a.sendNow(progressEntry(w))
			if m := nextKept(t, got, w); m.Seq != uint64(3+i) || m.LeftOut != 0 {
				t.Fatalf("an entry after one pushed out once written: sent %+v, want entry %d with none left out", m, 3+i)
			}
		}
		// Taken up to 2 only: 3 is not known taken.
		answers <- true
		awaitAsked() // the next, up to 4, held
		return a, got, answers
	}
	t.Run("confirmed", func(t *testing.T) {
		a, got, answers := setup(t)
		answers <- true
		untilConfirmed(t, a)
		a.registeredActivity(true, false)
		a.sendNow(progressEntry("d"))
		if m := nextKept(t, got, "d"); m.Seq != 5 || m.LeftOut != 0 {
			t.Fatalf("after their taking was confirmed: sent %+v, want none left out", m)
		}
	})
	t.Run("not resumed", func(t *testing.T) {
		a, got, answers := setup(t)
		answers <- false
		untilConfirmed(t, a)
		a.registeredActivity(true, false)
		a.sendNow(progressEntry("d"))
		// 2 (and the 1 it carried) and 3 pushed out, and 4 kept: four.
		if m := nextKept(t, got, "d"); m.Seq != 5 || m.LeftOut != 4 {
			t.Fatalf("not resumed: sent %+v, want four left out", m)
		}
	})
	t.Run("resumed", func(t *testing.T) {
		a, got, answers := setup(t)
		answers <- false
		untilConfirmed(t, a)
		a.registeredActivity(true, true)
		a.sendKeptActivity()
		// 4 is sent again, with the count of 2 (and the 1 it carried) and 3.
		if m := nextKept(t, got, "4 again"); m.Seq != 4 || m.LeftOut != 3 || !m.Late {
			t.Fatalf("resumed: sent %+v, want entry 4 again with three left out", m)
		}
	})
}

// An entry whose write is in flight is not pushed out by an entry kept
// meanwhile: the buffer holds one more than the bound until the write
// returns, the next oldest going past that, counted. Once written, the
// entry is judged as any other (sent on the connection the host is on:
// not counted) and the buffer is back at the bound.
func TestAnEntryBeingWrittenIsNotPushedOut(t *testing.T) {
	a, got := sentKept(t)
	a.actBound = 1
	hook := a.sendHook
	reached, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	a.sendHook = func(v any) {
		if m, ok := v.(proto.HostActivityMsg); ok && m.Seq == 1 && held.CompareAndSwap(false, true) {
			close(reached)
			<-release
		}
		hook(v)
	}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		a.sendNow(progressEntry("a"))
	}()
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the write never began")
	}
	state := func() ([]uint64, uint32) {
		a.actMu.Lock()
		defer a.actMu.Unlock()
		var seqs []uint64
		for _, k := range a.actKept {
			seqs = append(seqs, k.m.Seq)
		}
		return seqs, a.actLeftOut
	}
	a.actMu.Lock()
	epoch := a.actEpoch
	a.actMu.Unlock()
	a.keepActivity(hostActivityMsg(a.sessionID(), progressEntry("b")), epoch)
	if seqs, left := state(); !slices.Equal(seqs, []uint64{1, 2}) || left != 0 {
		t.Fatalf("an entry kept during a write: kept %v, left out %d; want 1 and 2, none", seqs, left)
	}
	a.keepActivity(hostActivityMsg(a.sessionID(), progressEntry("c")), epoch)
	if seqs, left := state(); !slices.Equal(seqs, []uint64{1, 3}) || left != 1 {
		t.Fatalf("a second entry kept during a write: kept %v, left out %d; want 1 and 3, one", seqs, left)
	}
	close(release)
	<-sent
	if m := nextKept(t, got, "a"); m.Seq != 1 || m.LeftOut != 0 {
		t.Fatalf("the entry held in its write: sent %+v", m)
	}
	if m := nextKept(t, got, "c"); m.Seq != 3 || m.LeftOut != 1 {
		t.Fatalf("the entry after: sent %+v, want entry 3 with one left out", m)
	}
	if seqs, left := state(); !slices.Equal(seqs, []uint64{3}) || left != 0 {
		t.Fatalf("after the write: kept %v, left out %d; want 3 alone, none", seqs, left)
	}
	// Entry 1, written, went with no count; the connection ending
	// unconfirmed counts it, with 3 and the one 3 carried.
	a.registeredActivity(true, false)
	a.sendNow(progressEntry("d"))
	if m := nextKept(t, got, "d"); m.Seq != 4 || m.LeftOut != 3 {
		t.Fatalf("after the connection ended: sent %+v, want three left out", m)
	}
}

// A confirmation asked on a new connection while the old one's is still
// under way is made after it, on the new connection: the entries sent on
// the new one go.
func TestAConfirmationAskedOnANewConnectionIsMade(t *testing.T) {
	a, got := sentKept(t)
	release := make(chan struct{})
	calls := make(chan struct{}, 8)
	first := true
	a.actConfirm = func() bool {
		calls <- struct{}{}
		if first {
			first = false
			<-release
		}
		return true
	}
	// The writers of the old connection and the new one (the sends go to
	// the test's hook, the confirmations to its seam).
	setOut := func(w *controlWriter) {
		a.mu.Lock()
		a.out = w
		a.mu.Unlock()
	}
	setOut(&controlWriter{})
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "old"})
	<-got
	<-calls
	// A new connection, while the old one's confirmation waits.
	setOut(nil)
	a.registeredActivity(true, true)
	setOut(&controlWriter{})
	a.sendKeptActivity()
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "new"})
	for len(got) > 0 {
		<-got
	}
	close(release)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		a.actMu.Lock()
		n, running := len(a.actKept), a.actConfirming
		a.actMu.Unlock()
		if n == 0 && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d kept after the new connection's confirmation", n)
		}
	}
	if len(calls) == 0 {
		t.Fatal("the new connection's confirmation was never made")
	}
}
