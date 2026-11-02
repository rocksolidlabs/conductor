package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// answer_as (the decision seam, C5) and its bounds: a question's id of 1 to
// 32 bytes of printable ASCII, exactly one of input (at most 48 bytes) and
// text (at most 4096), a name of 1 to 40 characters on one line, a subject
// of at most 48 bytes of printable ASCII; the largest message far under
// MaxHostMessage.
func TestHostAnswerAsBounds(t *testing.T) {
	ok := HostAnswerAsMsg{T: HostAnswerAs, Question: "0123456789abcdef", Input: "y", ByName: "Ada", BySubject: "u-1"}
	if !ok.Valid() {
		t.Fatalf("a choice: %+v", ok)
	}
	b, _ := json.Marshal(ok)
	if string(b) != `{"t":"answer_as","question":"0123456789abcdef","input":"y","byName":"Ada","bySubject":"u-1"}` {
		t.Fatalf("wire %s", b)
	}
	var back HostAnswerAsMsg
	if err := json.Unmarshal([]byte(`{"t":"answer_as","question":"q","text":"Postgres","byName":"Ada"}`), &back); err != nil || !back.Valid() || back.Text != "Postgres" || back.BySubject != "" {
		t.Fatalf("a line from the wire %v %+v", err, back)
	}
	name40 := strings.Repeat("é", MaxNameLen)
	at := map[string]HostAnswerAsMsg{
		"question at its bound": {Question: strings.Repeat("q", MaxQuestionID), Input: "y", ByName: "Ada"},
		"input at its bound":    {Question: "q", Input: strings.Repeat("y", MaxAnswerInput), ByName: "Ada"},
		"text at its bound":     {Question: "q", Text: strings.Repeat("x", MaxSubmit), ByName: "Ada"},
		"name at its bound":     {Question: "q", Input: "y", ByName: name40},
		"subject at its bound":  {Question: "q", Input: "y", ByName: "Ada", BySubject: strings.Repeat("s", MaxAnswerAsSubject)},
		"no subject":            {Question: "q", Input: "y", ByName: "Ada"},
	}
	for name, m := range at {
		if !m.Valid() {
			t.Errorf("%s: refused", name)
		}
	}
	past := map[string]HostAnswerAsMsg{
		"no question":           {Input: "y", ByName: "Ada"},
		"a long question":       {Question: strings.Repeat("q", MaxQuestionID+1), Input: "y", ByName: "Ada"},
		"a question with space": {Question: "a b", Input: "y", ByName: "Ada"},
		"neither":               {Question: "q", ByName: "Ada"},
		"both":                  {Question: "q", Input: "y", Text: "yes", ByName: "Ada"},
		"a long input":          {Question: "q", Input: strings.Repeat("y", MaxAnswerInput+1), ByName: "Ada"},
		"a long text":           {Question: "q", Text: strings.Repeat("x", MaxSubmit+1), ByName: "Ada"},
		"no name":               {Question: "q", Input: "y"},
		"a long name":           {Question: "q", Input: "y", ByName: name40 + "é"},
		"a name of two lines":   {Question: "q", Input: "y", ByName: "Ada\nLovelace"},
		"a name with an escape": {Question: "q", Input: "y", ByName: "Ada\x1b[2J"},
		"a name not UTF-8":      {Question: "q", Input: "y", ByName: "Ada\xff"},
		"a long subject":        {Question: "q", Input: "y", ByName: "Ada", BySubject: strings.Repeat("s", MaxAnswerAsSubject+1)},
		"a subject with space":  {Question: "q", Input: "y", ByName: "Ada", BySubject: "u 1"},
		"a subject not ASCII":   {Question: "q", Input: "y", ByName: "Ada", BySubject: "é"},
	}
	for name, m := range past {
		if m.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
	// The largest: every field at its bound, each byte of the text one JSON
	// escapes at its longest (\u0001 is six bytes), the name of four-byte
	// characters.
	big := HostAnswerAsMsg{T: HostAnswerAs, Question: strings.Repeat("q", MaxQuestionID), Text: strings.Repeat("\x01", MaxSubmit), ByName: strings.Repeat("😀", MaxNameLen), BySubject: strings.Repeat("s", MaxAnswerAsSubject)}
	if !big.Valid() {
		t.Fatal("the largest refused")
	}
	b, _ = json.Marshal(big)
	if len(b) > MaxHostMessage {
		t.Fatalf("the largest answer_as is %d bytes, past %d", len(b), MaxHostMessage)
	}
	plain := big
	plain.Text = strings.Repeat("x", MaxSubmit)
	if b, _ := json.Marshal(plain); len(b) > 4500 {
		t.Fatalf("a plain line's answer_as is %d bytes", len(b))
	}
}

// The register says answerAs when the host takes answer_as, and leaves the
// field out when it does not (a switchyard's uplink, an older host).
func TestRegisterAnswerAs(t *testing.T) {
	b, _ := json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", AnswerAs: true}})
	if !strings.Contains(string(b), `"answerAs":true`) {
		t.Fatalf("register %s", b)
	}
	b, _ = json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop"}})
	if strings.Contains(string(b), "answerAs") {
		t.Fatalf("a register that takes none %s", b)
	}
	var reg Register
	if err := json.Unmarshal([]byte(`{"t":"register","proto":1,"host":{"name":"laptop","answerAs":true},"session":{}}`), &reg); err != nil || !reg.Host.AnswerAs {
		t.Fatalf("from the wire %v %+v", err, reg)
	}
}

// kept on a host's attention: two ids taken, three or an invalid one
// refused, an older host's message without the field unchanged.
func TestHostAttentionKeptBounds(t *testing.T) {
	m := HostAttentionMsg{T: HostAttention, State: "working", Kept: []string{"0123456789abcdef", strings.Repeat("k", MaxQuestionID)}}
	if !m.KeptValid() {
		t.Fatal("two ids refused")
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"kept":["0123456789abcdef","`) {
		t.Fatalf("wire %s", b)
	}
	for name, kept := range map[string][]string{
		"three":         {"a", "b", "c"},
		"an empty id":   {""},
		"a long id":     {strings.Repeat("k", MaxQuestionID+1)},
		"an id spaced":  {"a b"},
		"an id escaped": {"a\x1b"},
	} {
		if (HostAttentionMsg{T: HostAttention, State: "working", Kept: kept}).KeptValid() {
			t.Errorf("%s: valid", name)
		}
	}
	older := HostAttentionMsg{T: HostAttention, SessionID: "s", State: "needs_input", Message: "Allow?", ID: "0123456789abcdef"}
	b, _ = json.Marshal(older)
	if string(b) != `{"t":"attention","sessionId":"s","state":"needs_input","message":"Allow?","id":"0123456789abcdef"}` || !older.KeptValid() {
		t.Fatalf("an attention without kept %s", b)
	}
	var back HostAttentionMsg
	if err := json.Unmarshal([]byte(`{"t":"attention","state":"working","kept":["a","b"]}`), &back); err != nil || len(back.Kept) != 2 || !back.KeptValid() {
		t.Fatalf("from the wire %v %+v", err, back)
	}
}
