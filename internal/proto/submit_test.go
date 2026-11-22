package proto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A submit on the wire (#322): the line, and when the client wants a word
// on it, a request id (at most 32 bytes of printable ASCII, as an answer's)
// and the unless-waiting guard; an older client's submit, with neither,
// reads the same as before. Its bounds: the text at most MaxSubmit bytes.
func TestSubmitShapeAndBounds(t *testing.T) {
	var m Submit
	if err := json.Unmarshal([]byte(`{"t":"submit","text":"looks good","reqId":"s1","unlessWaiting":true}`), &m); err != nil || !m.Valid() || m.Text != "looks good" || m.ReqID != "s1" || !m.UnlessWaiting {
		t.Fatalf("submit %v %+v", err, m)
	}
	var old Submit
	if err := json.Unmarshal([]byte(`{"t":"submit","text":"plain"}`), &old); err != nil || !old.Valid() || old.ReqID != "" || old.UnlessWaiting {
		t.Fatalf("an older client's submit %v %+v", err, old)
	}
	b, _ := json.Marshal(Submit{T: CtlSubmit, Text: "plain"})
	if string(b) != `{"t":"submit","text":"plain"}` {
		t.Fatalf("wire without a request: %s", b)
	}
	b, _ = json.Marshal(Submit{T: CtlSubmit, Text: "x", ReqID: "s2", UnlessWaiting: true})
	if string(b) != `{"t":"submit","text":"x","reqId":"s2","unlessWaiting":true}` {
		t.Fatalf("wire with a request: %s", b)
	}
	if !(Submit{Text: strings.Repeat("x", MaxSubmit), ReqID: strings.Repeat("r", MaxAnswerReqID)}).Valid() {
		t.Fatal("at its bounds")
	}
	for name, bad := range map[string]Submit{
		"a long text":          {Text: strings.Repeat("x", MaxSubmit+1)},
		"a long request id":    {Text: "x", ReqID: strings.Repeat("r", MaxAnswerReqID+1)},
		"a request id unicode": {Text: "x", ReqID: "é"},
		"a request id space":   {Text: "x", ReqID: "a b"},
	} {
		if bad.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
	// The owner's word, and the welcome that says it gives one.
	ok, _ := json.Marshal(SubmitOK{T: CtlSubmitOK, RequestID: "s1"})
	if string(ok) != `{"t":"submit_ok","requestId":"s1"}` {
		t.Fatalf("submit_ok %s", ok)
	}
	w, _ := json.Marshal(Welcome{T: CtlWelcome, SubmitAck: true})
	if !strings.Contains(string(w), `"submitAck":true`) {
		t.Fatalf("welcome %s", w)
	}
	w, _ = json.Marshal(Welcome{T: CtlWelcome})
	if strings.Contains(string(w), "submitAck") {
		t.Fatalf("an older owner's welcome: %s", w)
	}
}

// A submit or an answer whose line is within MaxSubmit but passes
// MaxControl once encoded (each byte a character JSON writes as six, or
// as two for MaxSubmit of them and the envelope) is refused as a control
// past its bound, ErrControlTooLarge, which an owner answers with
// ControlTooLargeWords; a line that fits decodes as before.
func TestALineThatEncodesPastMaxControlIsRefused(t *testing.T) {
	for _, c := range []string{`"`, "\x01", "<", `\`} {
		text := strings.Repeat(c, MaxSubmit)
		for name, m := range map[string]any{
			"submit": Submit{T: CtlSubmit, Text: text, ReqID: "s1"},
			"answer": Answer{T: CtlAnswer, ReqID: "a1", Question: "q", Text: text},
		} {
			_, err := Decode(MustControl(m))
			if !errors.Is(err, ErrControlTooLarge) || !errors.Is(err, ErrTooLarge) {
				t.Errorf("%s of %d %q: %v", name, MaxSubmit, c, err)
			}
		}
	}
	for name, m := range map[string]any{
		"submit": Submit{T: CtlSubmit, Text: strings.Repeat("x", MaxSubmit), ReqID: strings.Repeat("r", MaxAnswerReqID), UnlessWaiting: true},
		"answer": Answer{T: CtlAnswer, ReqID: strings.Repeat("r", MaxAnswerReqID), Question: strings.Repeat("q", MaxQuestionID), Text: strings.Repeat("x", MaxSubmit)},
	} {
		f, err := Decode(MustControl(m))
		if err != nil || f.Type != TypeControl {
			t.Errorf("%s that fits: %v", name, err)
		}
	}
	// Any other frame past its bound is not taken for a control's.
	if _, err := Decode(Encode(TypeInput, make([]byte, MaxInput+1))); errors.Is(err, ErrControlTooLarge) || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an input past its bound: %v", err)
	}
	// The words name the bound as it is.
	if MaxControl != 8<<10 || !strings.Contains(ControlTooLargeWords, "8 KiB") {
		t.Fatalf("the words %q for a bound of %d bytes", ControlTooLargeWords, MaxControl)
	}
}
