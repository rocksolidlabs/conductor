package hostagent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A report the server forwards marked as the agent's own question (ask,
// #193) is applied as one (session.Local.Ask): the done forwarded after it
// leaves it showing, and the host tells the server its question, with its
// id and the mark, after each. A switchyard is never told the mark.
func TestTheHostAppliesTheAgentsOwnQuestion(t *testing.T) {
	for _, switchyard := range []bool{false, true} {
		proc := sessiontest.NewFakeProc()
		t.Cleanup(func() { proc.End(0) })
		sent := make(chan proto.HostAttentionMsg, 16)
		a := &agent{opts: Options{Switchyard: switchyard}, peers: map[string]*peer{}, log: discardLog}
		a.sendHook = func(v any) {
			if m, ok := v.(proto.HostAttentionMsg); ok {
				sent <- m
			}
		}
		a.local = session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{FileEdit: "off", Log: discardLog, OnChange: a.noteChange})
		next := func(what string) proto.HostAttentionMsg {
			t.Helper()
			select {
			case m := <-sent:
				return m
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: the host told the server nothing", what)
				return proto.HostAttentionMsg{}
			}
		}
		if switchyard {
			// A switchyard forwards no report: the host's own question is
			// told it without words and without the mark.
			a.local.Ask("Which database?", session.SourceAPI, "", nil)
			if m := next("the question"); m.State != "needs_input" || m.Ask || m.Message != "" {
				t.Fatalf("a switchyard was told %+v", m)
			}
			continue
		}
		forward := func(m proto.HostAttentionMsg) {
			t.Helper()
			b, _ := json.Marshal(m)
			if err := a.handleControl(t.Context(), b); err != nil {
				t.Fatal(err)
			}
		}
		forward(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Which database?", Source: session.SourceAPI, Ask: true})
		q := a.local.Info().Attention
		if q.State != session.AttentionNeedsInput || !q.Asked() {
			t.Fatalf("the forwarded question: %+v", q)
		}
		if m := next("the question"); m.ID != q.ID || !m.Ask || m.Message != "Which database?" {
			t.Fatalf("told %+v", m)
		}
		forward(proto.HostAttentionMsg{T: proto.HostAttention, State: "done", Message: "asked", Source: session.SourceAPI})
		if att := a.local.Info().Attention; att.ID != q.ID || att.State != session.AttentionNeedsInput {
			t.Fatalf("the forwarded done replaced the question: %+v", att)
		}
		if m := next("after the done"); m.ID != q.ID || !m.Ask || m.State != "needs_input" {
			t.Fatalf("told %+v after the done", m)
		}
		// A forwarded needs_input without the mark is no question of the
		// agent's: the done replaces it. The question may be told once more
		// before it (a change the session noted before the wait reaches the
		// forwarder after the wait's report marked the attention untold:
		// "at worst the same attention once more", onLocalChange), so the
		// test waits for the wait's own message rather than the next one,
		// and allows that one re-tell, never a second.
		forward(proto.HostAttentionMsg{T: proto.HostAttention, State: "needs_input", Message: "Allow Bash?", Source: session.SourceAPI})
		for retold := false; ; {
			m := next("a wait")
			if m.ID == q.ID && m.Ask && m.State == "needs_input" && m.Message == "Which database?" {
				if retold {
					t.Fatalf("the question was told again a second time before the wait: %+v", m)
				}
				retold = true // the question told once more, as it was
				continue
			}
			if m.Message != "Allow Bash?" || m.Ask {
				t.Fatalf("a wait without the mark told %+v", m)
			}
			break
		}
		forward(proto.HostAttentionMsg{T: proto.HostAttention, State: "done", Source: session.SourceAPI})
		if att := a.local.Info().Attention; att.State != session.AttentionDone {
			t.Fatalf("a done over a wait without the mark: %+v", att)
		}
	}
}
