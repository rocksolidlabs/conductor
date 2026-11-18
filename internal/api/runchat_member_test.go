package api

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/proto"
)

// runChat reads frames until a line of the run's chat of the given kind
// arrives (the session's own chat carries the same question, scope
// "session", which is passed over).
func (w *wsClient) runChat(kind string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for the run's chat %s: %v", kind, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		if m["t"] == proto.CtlChat && m["scope"] == proto.ChatScopeRun && m["kind"] == kind {
			return m
		}
	}
}

// expectControlOf reads frames until a control message of one of the
// given kinds arrives.
func (w *wsClient) expectControlOf(kinds ...string) map[string]any {
	w.t.Helper()
	for {
		f, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for %v: %v", kinds, err)
		}
		if f.Type != proto.TypeControl {
			continue
		}
		var m map[string]any
		json.Unmarshal(f.Payload, &m)
		for _, k := range kinds {
			if m["t"] == k {
				return m
			}
		}
	}
}

// A managed run's attempt is in its run's chat under its member's name, the
// name the run's members go by on its page and its links (#1009): the
// attempt's question reaches the run's chat on that name, so that the
// page finds the member's session to answer it through; a choice answered
// on the attempt's session is said in the run's chat; and a kept message
// sent from the run's chat to that name is typed into the attempt. Joined
// under the attempt's label ("lead · build #1"), the question named no
// member the page knows and a send to the member was refused as unknown.
func TestAManagedAttemptIsInItsRunsChatUnderItsMembersName(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), managedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	sid := a["sessionId"].(string)
	nate := dialViewer(t, e, sid, adminToken)
	nate.send(proto.MustControl(proto.Hello{T: proto.CtlHello, Proto: 1, Client: "test", Name: "Nate"}))
	if w := nate.expectControl(proto.CtlWelcome); w["runChat"] != true || w["answer"] != true {
		t.Fatalf("welcome %v", w)
	}
	nate.expectControl(proto.CtlReady)
	// Live on the run's chat once the roster his attach sends has come, behind the replay.
	nate.expectControl(proto.CtlViewers)

	if resp, out := e.do("POST", "/api/sessions/"+sid+"/attention", adminToken, map[string]any{
		"state": "needs_input", "message": "Which branch?",
		"options": []any{map[string]any{"label": "main", "input": "main\r"}, map[string]any{"label": "release", "input": "release\r"}},
	}); resp.StatusCode != 200 {
		t.Fatalf("ask: %d %v", resp.StatusCode, out)
	}
	q := nate.runChat(proto.ChatKindQuestion)
	if q["on"] != "lead" || q["text"] != "Which branch?" {
		t.Fatalf("the question in the run's chat: %v", q)
	}

	// The line's second choice, its keys as the line gives them, named by the question's id, as the run page sends it on the
	// member's session.
	choice := q["options"].([]any)[1].(map[string]any)
	nate.send(proto.MustControl(proto.Answer{T: proto.CtlAnswer, ReqID: "r1", Question: q["id"].(string), Input: choice["input"].(string)}))
	// The owner's word and the run's chat's answered line, in whichever order they come.
	var ok, done map[string]any
	for ok == nil || done == nil {
		c := nate.expectControlOf(proto.CtlAnswerOK, proto.CtlChat, proto.CtlError)
		switch {
		case c["t"] == proto.CtlError:
			t.Fatalf("the answer was refused: %v", c)
		case c["t"] == proto.CtlAnswerOK:
			ok = c
		case c["scope"] == proto.ChatScopeRun && c["kind"] == proto.ChatKindSystem && c["event"] == "answered":
			done = c
		}
	}
	if ok["requestId"] != "r1" {
		t.Fatalf("answer_ok %v", ok)
	}
	if done["ref"] != q["id"] || done["on"] != "lead" || done["text"] != "release" {
		t.Fatalf("the answered line in the run's chat: %v", done)
	}

	// A kept message sent from the run's chat to the member by its name is typed into the attempt.
	nate.send(proto.MustControl(proto.ChatPost{T: proto.CtlChat, Scope: proto.ChatScopeRun, Text: "echo to-the-lead", Nonce: "n1"}))
	m := nate.runChat(proto.ChatKindMessage)
	nate.send(proto.MustControl(proto.ChatSend{T: proto.CtlChatSend, Scope: proto.ChatScopeRun, Ref: m["id"].(string), To: "lead"}))
	// The marker and cat's echo, in whichever order they come.
	var out []byte
	var marker map[string]any
	for marker == nil || !bytes.Contains(out, []byte("echo to-the-lead")) {
		f, err := nate.read()
		if err != nil {
			t.Fatalf("waiting for the marker and the echo: %v (marker %v, output %q)", err, marker, out)
		}
		switch f.Type {
		case proto.TypeOutput, proto.TypeScrollback:
			out = append(out, f.Payload...)
		case proto.TypeControl:
			var c map[string]any
			json.Unmarshal(f.Payload, &c)
			if c["t"] == proto.CtlChat && c["scope"] == proto.ChatScopeRun && c["kind"] == proto.ChatKindSentToAgent {
				marker = c
			}
			if c["t"] == proto.CtlError {
				t.Fatalf("the send was refused: %v", c)
			}
		}
	}
	if marker["to"] != "lead" || marker["ref"] != m["id"] {
		t.Fatalf("marker %v", marker)
	}
}
