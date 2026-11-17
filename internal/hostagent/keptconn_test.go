package hostagent

import (
	"slices"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// An entry written on a connection that ends as it goes, before the
// sender marks it sent, is taken as the registration that began meanwhile
// takes the entries sent before it: sent again to a session the
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
		a.sendActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: "edge"}, "")
		first := <-got
		a.actMu.Lock()
		kept := slices.Clone(a.actKept)
		left, epoch := a.actLeftOut, a.actEpoch
		a.actMu.Unlock()
		if resumed {
			// Sent again, late, on the new connection, and held as sent there.
			select {
			case again := <-got:
				if again.Seq != first.Seq {
					t.Fatalf("resumed: sent %+v, then %+v", first, again)
				}
			default:
				t.Fatal("resumed: not sent again")
			}
			if len(kept) != 1 || !kept[0].sent || kept[0].on != epoch {
				t.Fatalf("resumed: kept %+v", kept)
			}
		} else if len(kept) != 0 || left != 1 || len(got) != 0 {
			t.Fatalf("not resumed: kept %+v, left out %d, sent again %d", kept, left, len(got))
		}
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
	a.sendActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: "old"}, "")
	<-got
	<-calls
	// A new connection, while the old one's confirmation waits.
	setOut(nil)
	a.registeredActivity(true, true)
	setOut(&controlWriter{})
	a.sendKeptActivity()
	a.sendActivity(session.ActivityEntry{Type: session.ActivityProgress, Message: "new"}, "")
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
