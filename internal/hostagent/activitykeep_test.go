package hostagent

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// recordEntries is the server's record of the hosted session's activity:
// each entry's message, in order.
func (h *reportingHost) recordEntries(t *testing.T) []journal.Entry {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.base+"/api/history/sessions/"+h.hs.Info().ID, nil)
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
	return got.Record.Entries
}

// agentMessages is what of entries the agent reported (its progress
// events, as the tests report them), in order, and the status entries that
// say entries are missing.
func agentMessages(entries []journal.Entry) []string {
	var out []string
	for _, e := range entries {
		if e.Event == session.ActivityProgress || strings.Contains(e.Message, "may be missing") {
			out = append(out, e.Message)
		}
	}
	return out
}

// sendNow hands e to the host's sender as the session hands an entry it
// records now: stamped now, made on the registration the host is on.
func (a *agent) sendNow(e session.ActivityEntry) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	a.sendActivity(e, "", a.madeOn())
}

// untilEntries waits for the record's agent messages to be want.
func (h *reportingHost) untilEntries(t *testing.T, what string, want []string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got := agentMessages(h.recordEntries(t)); slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the record holds %q, want %q", what, agentMessages(h.recordEntries(t)), want)
		}
	}
}

func (h *reportingHost) event(t *testing.T, words string) {
	t.Helper()
	h.send(t, notify.Request{Event: session.ActivityProgress, Message: words})
}

// An agent's events reach the host's socket, are recorded in its session
// and reach the server's record: while the host's connection is up, and,
// made while it is down, as it comes back, in order and each once. The
// server's routes are never asked.
func TestEventsMadeWhileTheHostIsAwayReachTheRecordOnItsReturn(t *testing.T) {
	h := newReportingHostOf(t, codexLike(t), "codex-like", true)
	h.event(t, "up")
	h.untilEntries(t, "the event while up", []string{"up"})
	h.away(t)
	h.event(t, "away 1")
	h.event(t, "away 2")
	for _, e := range h.local.Activity() {
		if e.Message == "away 2" && e.ByName != eventActor {
			t.Fatalf("the host recorded %+v", e)
		}
	}
	if got := agentMessages(h.recordEntries(t)); !slices.Equal(got, []string{"up"}) {
		t.Fatalf("the record changed while the host was away: %q", got)
	}
	h.back()
	h.untilEntries(t, "the events made while away", []string{"up", "away 1", "away 2"})
	h.event(t, "after")
	h.untilEntries(t, "the event after", []string{"up", "away 1", "away 2", "after"})
	if n := h.route.n.Load(); n != 0 {
		t.Fatalf("the server's routes were asked %d times", n)
	}
}

// An entry the host sent as its connection ended, not known taken, is
// sent again as the next connection starts on the resumed session, and the
// server, which took it, keeps it once.
func TestAnEntrySentAgainIsShownOnce(t *testing.T) {
	h := newReportingHostOf(t, codexLike(t), "codex-like", true)
	h.a.actConfirm = func() bool { return false }
	h.event(t, "once")
	h.untilEntries(t, "the event", []string{"once"})
	h.away(t)
	h.back()
	// A later event reaches the record after anything sent before it on
	// the connection: the entry sent again among them.
	h.event(t, "later")
	h.untilEntries(t, "the event sent again, and a later one", []string{"once", "later"})
}

// Past the bound of entries kept the oldest go, and the record says that
// entries may be missing, before the first entry sent after them.
func TestPastTheBoundTheRecordIsToldEntriesAreMissing(t *testing.T) {
	h := newReportingHostOf(t, codexLike(t), "codex-like", true)
	h.a.actBound = 3
	h.away(t)
	for _, w := range []string{"1", "2", "3", "4", "5"} {
		h.event(t, w)
	}
	h.back()
	h.untilEntries(t, "the last three", []string{"2 activity entries of the host's may be missing here: recorded while its connection to this server was down, and not sent", "3", "4", "5"})
}

// sentKept is an agent whose activity is kept for a server that takes its
// numbers, with its sends handed to the test, each taken as written.
func sentKept(t *testing.T) (*agent, chan proto.HostActivityMsg) {
	t.Helper()
	a, _, _, _ := sentAttention(t)
	got := make(chan proto.HostActivityMsg, 64)
	a.sendHook = func(v any) {
		if m, ok := v.(proto.HostActivityMsg); ok {
			got <- m
		}
	}
	a.actConfirm = func() bool { return false }
	a.registeredActivity(true, false)
	return a, got
}

func seqsOf(got chan proto.HostActivityMsg) []uint64 {
	var out []uint64
	for len(got) > 0 {
		out = append(out, (<-got).Seq)
	}
	return out
}

// Entries are numbered from 1 in order and kept until taken; a resumed
// session is sent every one again, in order; one not resumed is sent none
// that may have been taken, and they are counted left out, said with the
// next entry sent.
func TestKeptEntriesOnResumeAndOnANewSession(t *testing.T) {
	a, got := sentKept(t)
	for _, w := range []string{"a", "b"} {
		a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: w})
	}
	if s := seqsOf(got); !slices.Equal(s, []uint64{1, 2}) {
		t.Fatalf("sent %v", s)
	}
	a.registeredActivity(true, true)
	a.sendKeptActivity()
	if s := seqsOf(got); !slices.Equal(s, []uint64{1, 2}) {
		t.Fatalf("sent again %v", s)
	}
	a.registeredActivity(true, false)
	a.sendKeptActivity()
	if s := seqsOf(got); len(s) != 0 {
		t.Fatalf("a new session was sent %v", s)
	}
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "c"})
	m := <-got
	if m.Seq != 3 || m.LeftOut != 2 {
		t.Fatalf("sent %+v", m)
	}
	// A server that takes no numbers is sent none, and nothing is kept.
	a.registeredActivity(false, false)
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "d"})
	if m := <-got; m.Seq != 0 || m.LeftOut != 0 {
		t.Fatalf("an older server was sent %+v", m)
	}
	a.actMu.Lock()
	n := len(a.actKept)
	a.actMu.Unlock()
	if n != 0 {
		t.Fatalf("%d kept for an older server", n)
	}
}

// An entry confirmed taken (a ping answered after it on its connection)
// goes, and is not sent again on a resumed session.
func TestAConfirmedEntryIsNotSentAgain(t *testing.T) {
	a, got := sentKept(t)
	confirmed := make(chan struct{}, 4)
	a.actConfirm = func() bool { confirmed <- struct{}{}; return true }
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "a"})
	<-confirmed
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		a.actMu.Lock()
		n, running := len(a.actKept), a.actConfirming
		a.actMu.Unlock()
		if n == 0 && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d kept after the confirmation", n)
		}
	}
	seqsOf(got)
	a.registeredActivity(true, true)
	a.sendKeptActivity()
	if s := seqsOf(got); len(s) != 0 {
		t.Fatalf("sent again %v", s)
	}
}

// A host registered with a server that says nothing of the entries'
// numbers (one of an earlier release) sends its entries as before: no
// number, nothing kept.
func TestAnOlderServerIsSentNoNumbers(t *testing.T) {
	srv := newStubServer(t)
	a := &agent{opts: Options{ServerURL: srv.url, Token: hostToken, Name: "older", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", Log: discardLog}, peers: map[string]*peer{}, log: discardLog}
	a.dir = t.TempDir()
	var err error
	if a.wsURL, err = controlURL(srv.url); err != nil {
		t.Fatal(err)
	}
	conn, _, err := a.dialAndRegister(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	<-srv.conns
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "plain"})
	for deadline := time.After(10 * time.Second); ; {
		select {
		case data := <-srv.got:
			if !strings.Contains(string(data), `"plain"`) {
				continue
			}
			if strings.Contains(string(data), `"seq"`) || strings.Contains(string(data), `"leftOut"`) {
				t.Fatalf("an older server was sent %s", data)
			}
			a.actMu.Lock()
			n := len(a.actKept)
			a.actMu.Unlock()
			if n != 0 {
				t.Fatalf("%d kept for an older server", n)
			}
			return
		case <-deadline:
			t.Fatal("the entry never reached the server")
		}
	}
}

// An entry recorded while there is no connection was never written: it is
// sent to the next connection's server even when that server did not
// resume the session, and nothing is counted left out.
func TestAnEntryNeverWrittenIsSentToANewSession(t *testing.T) {
	a, _, _, _ := sentAttention(t)
	a.sendHook = nil
	a.registeredActivity(true, false)
	a.sendNow(session.ActivityEntry{Type: session.ActivityProgress, Message: "away"})
	a.registeredActivity(true, false)
	a.actMu.Lock()
	defer a.actMu.Unlock()
	if len(a.actKept) != 1 || a.actKept[0].sent || a.actLeftOut != 0 {
		t.Fatalf("kept %+v, left out %d", a.actKept, a.actLeftOut)
	}
}
