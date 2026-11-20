package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A notification that names its record, and the open decisions' refs
// (docs/plans/phone-shell.md, Part one, OSS-2a).

// sinkFixture is a decisionFixture whose server has the sink push, which
// takes every entry.
func sinkFixture(t *testing.T, mutate func(*config.Config), log *slog.Logger) (*decisionFixture, *recSink) {
	t.Helper()
	sink := &recSink{name: "push"}
	p := ext.Providers{Notifications: []ext.NotificationSink{sink}, All: []ext.Entry{{Kind: ext.KindNotifications, Name: "push", Value: sink}}}
	return newDecisionFixture(t, mutate, log, WithExtensions(local.Fill(p))), sink
}

// decisionNotes is the decisions' notifications of ns.
func decisionNotes(ns []ext.Notification) []ext.Notification {
	return slices.DeleteFunc(slices.Clone(ns), func(n ext.Notification) bool { return n.Event != config.EventDecision })
}

// noteOf waits until sink holds the notification of decision id in state,
// and answers it.
func noteOf(t *testing.T, sink *recSink, id, state string) ext.Notification {
	t.Helper()
	match := func(n ext.Notification) bool {
		return strings.HasSuffix(n.URL, "&decision="+id) && n.State == state
	}
	got := decisionNotes(sink.waitFor(t, id+" "+state, func(ns []ext.Notification) bool { return slices.ContainsFunc(decisionNotes(ns), match) }))
	return got[slices.IndexFunc(got, match)]
}

// Every decision's notification a sink takes names its record, beside its
// state, as it opens, is answered and is closed; nothing else of it
// changes.
func TestDecisionNotificationNamesItsRecord(t *testing.T) {
	f, sink := sinkFixture(t, nil, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	f.ask(t, approvalBody("r2"), http.StatusCreated, "")
	f.respond(t, adminToken, "dr-1", "a1", 0, 1, http.StatusCreated, "")
	f.expect(t, http.MethodPost, "/api/decisions/dr-2/cancel", map[string]any{"commandId": "c-1", "why": "Not needed.", "release": true, "expectedRev": 1}, http.StatusOK, "")
	for _, c := range []struct{ id, request, state, by string }{
		{"dr-1", "r1", ledger.DecisionOpen, ""},
		{"dr-1", "r1", ledger.DecisionAnswered, "admin"},
		{"dr-2", "r2", ledger.DecisionOpen, ""},
		{"dr-2", "r2", ledger.DecisionCancelled, ""},
	} {
		n := noteOf(t, sink, c.id, c.state)
		if n.Ref != "decision:"+c.id {
			t.Errorf("%s %s: ref %q", c.id, c.state, n.Ref)
		}
		want := ext.Notification{Event: "decision", Type: "decision", At: n.At, State: c.state, Ref: "decision:" + c.id,
			Message: "Ship build " + c.request + "?", URL: "/runs/" + f.run + "?view=tasks&task=build&decision=" + c.id, By: n.By,
			Session: ext.SessionRef{ID: f.sid, Run: f.run, Task: "build", Attempt: n.Session.Attempt}}
		if n != want || n.Session.Attempt != 1 || (c.by != "" && n.By != c.by) {
			t.Errorf("%s %s: %+v\nwant %+v", c.id, c.state, n, want)
		}
	}
}

// A session's question, an attention entry and no durable decision,
// reaches a sink with no ref.
func TestAttentionEntryHasNoRef(t *testing.T) {
	sink := &recSink{name: "push"}
	e := newNotifyEnv(t, nil, nil, sink)
	id := e.createSession("cat")
	d, _ := e.srv.registry.Get(id)
	d.(*session.Local).SetAttention(session.AttentionNeedsInput, "Allow the edit?", session.SourceAPI)
	got := sink.waitFor(t, "the question", func(ns []ext.Notification) bool {
		return slices.ContainsFunc(ns, func(n ext.Notification) bool { return n.Session.ID == id && n.Message == "Allow the edit?" })
	})
	for _, n := range got {
		if n.Ref != "" {
			t.Fatalf("an entry of the session reached the sink with a ref: %+v", n)
		}
	}
}

// nextHook is the next request rcv was sent, waited for as long as the
// test's own bound on a step the server takes at once.
func nextHook(t *testing.T, rcv *receiver) delivery {
	t.Helper()
	select {
	case d := <-rcv.got:
		return d
	case <-time.After(testWait):
		t.Fatal("no webhook request arrived")
	}
	return delivery{}
}

// mainDecisionBody is the body main sent a webhook for n, built from
// main's payload types, not from the code under test.
func mainDecisionBody(t *testing.T, n ext.Notification) []byte {
	t.Helper()
	b, err := json.Marshal(mainPayload{SessionID: n.Session.ID, Session: mainSession{ID: n.Session.ID, Name: n.Session.Name, AgentID: n.Session.AgentID},
		Entry: session.ActivityEntry{At: n.At, Type: n.Type, By: n.By, ByName: n.ByName, Message: n.Message, URL: n.URL, To: n.To, Tool: n.Tool, Op: n.Op, Path: n.Path}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A webhook that lists decision is sent, for a decision's notification
// that names its record, the bytes and the event a notification without
// the ref gives: the ref reaches no webhook.
func TestWebhookBodyUnchangedByRef(t *testing.T) {
	rcv := newReceiver(t, nil)
	f, sink := sinkFixture(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: []string{config.EventDecision}, AllowPrivate: true}}
	}, nil)
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	n := noteOf(t, sink, "dr-1", ledger.DecisionOpen)
	if n.Ref != "decision:dr-1" {
		t.Fatalf("the sink's notification has ref %q", n.Ref)
	}
	d := nextHook(t, rcv)
	if ev := d.header.Get("X-Conductor-Event"); ev != config.EventDecision {
		t.Fatalf("X-Conductor-Event %q", ev)
	}
	if want := mainDecisionBody(t, n); !bytes.Equal(d.body, want) {
		t.Fatalf("the webhook's body\n%s\nwant\n%s", d.body, want)
	}
	if bytes.Contains(d.body, []byte("decision:dr-1")) || bytes.Contains(d.body, []byte(`"ref"`)) {
		t.Fatalf("the ref reached the webhook: %s", d.body)
	}
}

// A ref the server built in a form ParseDecisionRef does not take is its
// own bug: it reaches no sink and no webhook, the notification goes with
// no ref, and one error line says its length, never its text.
func TestServerNotificationRefChecked(t *testing.T) {
	const bad = "decision:dr-1 and a secret line"
	was := decisionRefOf
	t.Cleanup(func() { decisionRefOf = was })
	decisionRefOf = func(id string) string { return bad }
	logs := &logBuffer{}
	rcv := newReceiver(t, nil)
	f, sink := sinkFixture(t, func(c *config.Config) {
		c.Webhooks = []config.Webhook{{URL: rcv.URL + "/hook", Events: []string{config.EventDecision}, AllowPrivate: true}}
	}, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	f.ask(t, approvalBody("r1"), http.StatusCreated, "")
	n := noteOf(t, sink, "dr-1", ledger.DecisionOpen)
	if n.Ref != "" {
		t.Fatalf("the sink was handed the ref %q", n.Ref)
	}
	for _, other := range sink.all() {
		if other.Ref != "" {
			t.Fatalf("the sink was handed the ref %q", other.Ref)
		}
	}
	d := nextHook(t, rcv)
	if want := mainDecisionBody(t, n); !bytes.Equal(d.body, want) || bytes.Contains(d.body, []byte("secret")) {
		t.Fatalf("the webhook's body\n%s\nwant\n%s", d.body, want)
	}
	// The line is written before the notification is queued.
	var lines []string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "ref of another form") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "level=ERROR") || !strings.Contains(lines[0], fmt.Sprintf("bytes=%d", len(bad))) {
		t.Fatalf("the error lines %q", lines)
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatalf("the log holds the ref's text:\n%s", logs.String())
	}
}

// seedOpen opens n approval gates on runs of their own the engine does not
// hold, at most 60 a run, oldest first, and answers their numbers.
func seedOpen(t *testing.T, e *testEnv, n int) []int64 {
	t.Helper()
	var out []int64
	for r := 0; len(out) < n; r++ {
		run := fmt.Sprintf("seed%04d-0000aaaa", r)
		if _, err := e.srv.ledger.Transition(t.Context(), run, func(tx *ledger.Tx) error {
			if err := tx.PutRun(ledger.RunRow{ID: run, CrewID: "seed", Crew: "{}", State: ledger.RunStopped, CreatedAt: tx.Now()}); err != nil {
				return err
			}
			for i := 0; i < 60 && len(out) < n; i++ {
				row, _, err := tx.OpenDecision(ledger.DecisionRow{RunID: run, Origin: ledger.OriginPlan, Type: ledger.DecisionApproval, Gate: fmt.Sprintf("g%d", i), Plan: "pl-1", PlanDigest: "d1",
					Fingerprint: fmt.Sprintf("fp-%s-%d", run, i), Request: seedRequest(""), Policy: `{"revision":"local/1","approver":"operator","quorum":1,"authority":[{"kind":"operator"}]}`})
				if err != nil {
					return err
				}
				out = append(out, row.N)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// rawLedgerWrite runs stmt on e's ledger file through a connection of its
// own.
func rawLedgerWrite(t *testing.T, e *testEnv, stmt string) {
	t.Helper()
	(&decideFixture{decisionFixture: &decisionFixture{testEnv: e}}).rawWrite(t, stmt)
}

// A sink walks every open decision's ref in pages of 256, in id order,
// next the last ref of a page that more follow: 300 open among 320, 20
// closed between them, in two pages of 256 and 44; every open one once,
// no closed one, and nothing but refs.
func TestOpenDecisionRefsPagesEveryOpenRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	ns := seedOpen(t, e, 320)
	var closed []string
	for i := 7; i < len(ns) && len(closed) < 20; i += 16 {
		closed = append(closed, fmt.Sprint(ns[i]))
	}
	rawLedgerWrite(t, e, "UPDATE decisions SET state = 'cancelled' WHERE n IN ("+strings.Join(closed, ",")+")")
	var want []string
	for _, n := range ns {
		if !slices.Contains(closed, fmt.Sprint(n)) {
			want = append(want, "decision:"+ledger.DecisionID(n))
		}
	}
	if len(want) != 300 || len(closed) != 20 {
		t.Fatalf("seeded %d open, %d closed", len(want), len(closed))
	}
	h := testHost(e.srv, t.Context(), "push")
	first, next, err := h.OpenDecisionRefs(t.Context(), "")
	if err != nil || len(first) != ext.MaxOpenDecisionRefs || next != first[len(first)-1] {
		t.Fatalf("the first page: %d refs, next %q, %v", len(first), next, err)
	}
	second, last, err := h.OpenDecisionRefs(t.Context(), next)
	if err != nil || len(second) != 44 || last != "" {
		t.Fatalf("the second page: %d refs, next %q, %v", len(second), last, err)
	}
	got := append(first, second...)
	if !slices.Equal(got, want) {
		t.Fatalf("the walk\n%q\nwant\n%q", got, want)
	}
	for _, ref := range got {
		if _, ok := ext.ParseDecisionRef(ref); !ok {
			t.Fatalf("%q is not a decision's ref", ref)
		}
	}
	// A cursor past the last open decision: no ref, no next.
	if refs, next, err := h.OpenDecisionRefs(t.Context(), want[len(want)-1]); err != nil || len(refs) != 0 || next != "" {
		t.Fatalf("after the last: %q %q %v", refs, next, err)
	}
	// A cursor that is a closed decision's ref walks on from it.
	from := "decision:dr-" + closed[0]
	refs, _, err := h.OpenDecisionRefs(t.Context(), from)
	if err != nil || len(refs) == 0 || refs[0] != "decision:"+ledger.DecisionID(ns[8]) {
		t.Fatalf("after the closed %s: %q %v", from, refs[:min(len(refs), 2)], err)
	}
}

// The refusals: an after that is no decision's ref is refused, a
// switchyard keeps no ledger, the Host of a try given up refuses as its
// other calls do, and a ledger without its index of the open decisions
// answers its ErrUnavailable.
func TestOpenDecisionRefsRefusals(t *testing.T) {
	e := newTestEnv(t, nil)
	seedOpen(t, e, 3)
	h := testHost(e.srv, t.Context(), "push")
	for _, after := range []string{"dr-1", "decision:7", "decision:dr-01", "decision:dr-0", "decisions:dr-1", "decision:dr-1 ", "question:x/y",
		"decision:dr-1234567890123456789", strings.Repeat("x", 5000)} {
		refs, next, err := h.OpenDecisionRefs(t.Context(), after)
		if !errors.Is(err, ext.ErrRefused) || refs != nil || next != "" {
			t.Errorf("%.40q: %q %q %v", after, refs, next, err)
		}
		if err != nil && len(err.Error()) > 200 {
			t.Errorf("%.40q: the refusal is %d bytes", after, len(err.Error()))
		}
	}

	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if _, _, err := testHost(yard.srv, t.Context(), "push").OpenDecisionRefs(t.Context(), ""); !errors.Is(err, ext.ErrUnsupported) {
		t.Fatalf("a switchyard: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	given := testHost(e.srv, ctx, "push")
	cancel()
	_, _, err := given.OpenDecisionRefs(t.Context(), "")
	if _, derr := given.Decision(t.Context(), "dr-1"); !errors.Is(err, context.Canceled) || !errors.Is(derr, context.Canceled) {
		t.Fatalf("a try given up: %v, its Decision %v", err, derr)
	}

	rawLedgerWrite(t, e, "DROP INDEX decisions_open")
	refs, next, err := h.OpenDecisionRefs(t.Context(), "")
	if !errors.Is(err, ledger.ErrUnavailable) || refs != nil || next != "" {
		t.Fatalf("with no index: %q %q %v", refs, next, err)
	}
}
