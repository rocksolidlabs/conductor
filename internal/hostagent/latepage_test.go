package hostagent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// hookCall is one webhook delivery: the entry's type, its message.
type hookCall struct{ typ, message string }

// pagedHost is a reporting host whose server delivers every entry of every
// session to a webhook, whose deliveries the test reads in order.
func pagedHost(t *testing.T) (*reportingHost, chan hookCall) {
	t.Helper()
	calls := make(chan hookCall, 512)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Entry session.ActivityEntry `json:"entry"`
			State string                `json:"state"`
		}
		_ = json.Unmarshal(b, &p)
		calls <- hookCall{typ: p.Entry.Type, message: p.Entry.Message}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rcv.Close)
	h := newReportingHostWith(t, codexLike(t), "codex-like", true, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", AllowPrivate: true,
			Events: []string{"needs_input", "done", "working", "tool_denied", "progress", "artifact", "handoff", "error", "tool_use", "file"}}}
	})
	return h, calls
}

// pagedBefore reports the webhook's deliveries before the marker, a live
// event the test reports last: a webhook delivers in order, so what was
// offered it before the marker has come by then.
func (h *reportingHost) pagedBefore(t *testing.T, calls chan hookCall) []hookCall {
	t.Helper()
	h.event(t, "marker")
	var out []hookCall
	for deadline := time.After(30 * time.Second); ; {
		select {
		case c := <-calls:
			if c.typ == session.ActivityProgress && c.message == "marker" {
				return out
			}
			out = append(out, c)
		case <-deadline:
			t.Fatalf("the marker never came; before it %+v", out)
		}
	}
}

// lateAttention reads the server's event stream feed for the session's
// attention entries until the late ones' messages are want, in order: what
// a person's feed shows of them. It counts the live ones meanwhile.
func lateAttention(t *testing.T, feed <-chan string, sessionID string, want []string) (live int) {
	t.Helper()
	var got []string
	for deadline := time.After(15 * time.Second); !slices.Equal(got, want); {
		select {
		case ev := <-feed:
			data, ok := strings.CutPrefix(ev, "activity ")
			if !ok {
				continue
			}
			var e struct {
				SessionID string `json:"sessionId"`
				Type      string `json:"type"`
				Message   string `json:"message"`
				Late      bool   `json:"late"`
			}
			if json.Unmarshal([]byte(data), &e) != nil || e.SessionID != sessionID || e.Type != session.ActivityAttention {
				continue
			}
			if !e.Late {
				live++
				continue
			}
			got = append(got, e.Message)
		case <-deadline:
			t.Fatalf("the stream showed %q late, want %q", got, want)
		}
	}
	return live
}

// A question asked and answered while the host is away pages nobody when
// the host comes back: its entries are shown in order, marked late, and no
// webhook is offered them.
func TestAQuestionAnsweredWhileAwayPagesNobody(t *testing.T) {
	h, calls := pagedHost(t)
	feed := adminFeed(t, h.base)
	h.away(t)
	h.send(t, notify.Request{State: "needs_input", Message: "Deploy now?", Ask: true})
	h.send(t, notify.Request{State: "working", Message: "deploying"})
	h.back()
	if live := lateAttention(t, feed, h.hs.Info().ID, []string{"Deploy now?", "deploying"}); live != 0 {
		t.Fatalf("%d of them shown as live events", live)
	}
	if got := h.pagedBefore(t, calls); len(got) != 0 {
		t.Fatalf("paged for what the session moved past: %+v", got)
	}
}

// A question asked while the host is away and still open as it comes back
// pages once: from the session's state, not from its entries.
func TestAQuestionStillOpenAtTheReturnPagesOnce(t *testing.T) {
	h, calls := pagedHost(t)
	feed := adminFeed(t, h.base)
	h.away(t)
	h.send(t, notify.Request{State: "needs_input", Message: "Ship it?", Ask: true})
	h.back()
	lateAttention(t, feed, h.hs.Info().ID, []string{"Ship it?"})
	untilListed(t, h.hs, "the copy's question", func(i session.Info) bool { return i.Attention.Message == "Ship it?" })
	got := h.pagedBefore(t, calls)
	if len(got) != 1 || got[0].typ != session.ActivityAttention || got[0].message != "Ship it?" {
		t.Fatalf("paged %+v, want the open question once", got)
	}
	// Told again (a connection's start once more), it pages no more.
	h.away(t)
	h.back()
	if got := h.pagedBefore(t, calls); len(got) != 0 {
		t.Fatalf("paged again: %+v", got)
	}
}

// A burst of entries kept while away is recorded and offered to no
// webhook.
func TestABurstOfKeptEntriesPagesNobody(t *testing.T) {
	h, calls := pagedHost(t)
	h.away(t)
	var want []string
	for i := range 20 {
		w := string(rune('a' + i))
		h.event(t, w)
		want = append(want, w)
	}
	h.send(t, notify.Request{Event: session.ActivityToolDenied, Tool: "Bash"})
	h.back()
	h.untilEntries(t, "the burst", want)
	if got := h.pagedBefore(t, calls); len(got) != 0 {
		t.Fatalf("a burst of kept entries reached the webhook: %d, first %+v", len(got), got[0])
	}
}

// The host marks its retell Retold only for a question its session was
// asked before the connection began, only on the retell, and only to a
// server that takes activity's numbers.
func TestTheRetellOfAQuestionAskedBeforeTheConnectionIsMarked(t *testing.T) {
	a, _, sent, _ := sentAttention(t)
	a.activitySeq.Store(true)
	a.local.SetAttentionFull(session.AttentionNeedsInput, "Asked before?", session.SourceAPI, "", nil)
	if m := nextSent(t, sent, "the question"); m.Retold {
		t.Fatalf("a live question marked retold: %+v", m)
	}
	a.retellAttention()
	if m := nextSent(t, sent, "the retell"); !m.Retold || m.Message != "Asked before?" {
		t.Fatalf("the retell %+v", m)
	}
	a.local.SetAttentionFull(session.AttentionNeedsInput, "Asked after", session.SourceAPI, "", nil)
	if m := nextSent(t, sent, "a later question"); m.Retold {
		t.Fatalf("a later question marked retold: %+v", m)
	}
	// A question asked after the connection began, told as its retell:
	// the connection's start set before the question is asked.
	a.mu.Lock()
	a.retellPending, a.connStart, a.attentionUntold = true, time.Now(), true
	a.mu.Unlock()
	a.local.SetAttentionFull(session.AttentionNeedsInput, "Asked as it began", session.SourceAPI, "", nil)
	if m := nextSent(t, sent, "the retell of a newer question"); m.Retold || m.Message != "Asked as it began" {
		t.Fatalf("a question asked after the connection began marked retold: %+v", m)
	}
	// An older server is never sent the mark.
	a.activitySeq.Store(false)
	a.retellAttention()
	if m := nextSent(t, sent, "the retell to an older server"); m.Retold {
		t.Fatalf("an older server was sent %+v", m)
	}
}
