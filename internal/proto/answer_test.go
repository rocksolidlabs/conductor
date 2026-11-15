package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// An answer on the wire, as a page writes it, and its bounds: a question's
// id of 1 to 32 bytes of printable ASCII, a request id of at most 32,
// exactly one of input (at most 48 bytes, a choice's keys) and text (at
// most 4096, a submit's line).
func TestAnswerShapeAndBounds(t *testing.T) {
	var a Answer
	if err := json.Unmarshal([]byte(`{"t":"answer","reqId":"a1","question":"0123456789abcdef","input":"y"}`), &a); err != nil || !a.Valid() || a.Input != "y" || a.Question != "0123456789abcdef" || a.ReqID != "a1" {
		t.Fatalf("answer %v %+v", err, a)
	}
	b, _ := json.Marshal(Answer{T: CtlAnswer, ReqID: "a2", Question: "q", Text: "Postgres"})
	if string(b) != `{"t":"answer","reqId":"a2","question":"q","text":"Postgres"}` {
		t.Fatalf("wire %s", b)
	}
	ok := Answer{T: CtlAnswer, Question: strings.Repeat("q", MaxQuestionID), ReqID: strings.Repeat("r", MaxAnswerReqID), Input: strings.Repeat("y", MaxAnswerInput)}
	if !ok.Valid() {
		t.Fatalf("at its bounds: %+v", ok)
	}
	line := Answer{T: CtlAnswer, Question: "q", Text: strings.Repeat("x", MaxSubmit)}
	if !line.Valid() {
		t.Fatal("a line at its bound")
	}
	for name, bad := range map[string]Answer{
		"no question":           {Question: "", Input: "y"},
		"a long question":       {Question: strings.Repeat("q", MaxQuestionID+1), Input: "y"},
		"a question with space": {Question: "a b", Input: "y"},
		"a control in question": {Question: "a\x1b", Input: "y"},
		"a long request id":     {Question: "q", ReqID: strings.Repeat("r", MaxAnswerReqID+1), Input: "y"},
		"a request id unicode":  {Question: "q", ReqID: "é", Input: "y"},
		"neither":               {Question: "q"},
		"both":                  {Question: "q", Input: "y", Text: "yes"},
		"a long input":          {Question: "q", Input: strings.Repeat("y", MaxAnswerInput+1)},
		"a long text":           {Question: "q", Text: strings.Repeat("x", MaxSubmit+1)},
	} {
		if bad.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
}

// What the owner says: an attention with its id, the welcome that says it
// takes answers, the answer taken; an attention with none, an older owner's
// welcome, leave the fields out.
func TestAnswerOwnerShapes(t *testing.T) {
	b, _ := json.Marshal(Attention{T: CtlAttention, State: "needs_input", Message: "Allow?", ID: "0123456789abcdef"})
	if string(b) != `{"t":"attention","state":"needs_input","message":"Allow?","id":"0123456789abcdef"}` {
		t.Fatalf("attention %s", b)
	}
	b, _ = json.Marshal(Attention{T: CtlAttention, State: ""})
	if strings.Contains(string(b), `"id"`) || strings.Contains(string(b), `"answered"`) {
		t.Fatalf("a cleared attention %s", b)
	}
	// A question answered by keys in the terminal, shown still: its mark rides along (#258).
	b, _ = json.Marshal(Attention{T: CtlAttention, State: "needs_input", Message: "Allow?", ID: "0123456789abcdef", Answered: "terminal"})
	if !strings.Contains(string(b), `"answered":"terminal"`) {
		t.Fatalf("an answered attention %s", b)
	}
	b, _ = json.Marshal(HostAttentionMsg{T: HostAttention, SessionID: "s", State: "needs_input", ID: "0123456789abcdef", Answered: "terminal"})
	if !strings.Contains(string(b), `"answered":"terminal"`) {
		t.Fatalf("a host's answered attention %s", b)
	}
	b, _ = json.Marshal(Welcome{T: CtlWelcome, Answer: true})
	if !strings.Contains(string(b), `"answer":true`) {
		t.Fatalf("welcome %s", b)
	}
	b, _ = json.Marshal(Welcome{T: CtlWelcome})
	if strings.Contains(string(b), `"answer"`) {
		t.Fatalf("an older owner's welcome %s", b)
	}
	b, _ = json.Marshal(AnswerOK{T: CtlAnswerOK, RequestID: "a1"})
	if string(b) != `{"t":"answer_ok","requestId":"a1"}` {
		t.Fatalf("answer_ok %s", b)
	}
	b, _ = json.Marshal(HostAttentionMsg{T: HostAttention, SessionID: "s", State: "needs_input", ID: "0123456789abcdef"})
	if !strings.Contains(string(b), `"id":"0123456789abcdef"`) {
		t.Fatalf("host attention %s", b)
	}
	// An owner that does not know the message (an older one) reads it as
	// any message it does not know: by its t alone.
	if typ, err := ParseHeader([]byte(`{"t":"answer","question":"q","input":"y"}`)); err != nil || typ != CtlAnswer {
		t.Fatalf("header %q %v", typ, err)
	}
}
