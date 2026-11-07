package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// A question's plan (#289) rides the attention message and the host's,
// and only when there is one (an older page reads the rest as before); the
// fullest question with a plan at its bound fits a control frame.
func TestAttentionPlan(t *testing.T) {
	plan := "/home/dev/.claude/plans/plan-creating-a-file.md"
	b, err := json.Marshal(Attention{T: CtlAttention, State: "needs_input", Kind: "permission", Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"plan":"` + plan + `"`; !strings.Contains(string(b), want) {
		t.Fatalf("%s", b)
	}
	if b, _ := json.Marshal(Attention{T: CtlAttention, State: "needs_input", Kind: "permission"}); strings.Contains(string(b), "plan") {
		t.Fatalf("no plan: %s", b)
	}
	if b, _ := json.Marshal(HostAttentionMsg{T: HostAttention, SessionID: "s", State: "needs_input", Plan: plan}); !strings.Contains(string(b), `"plan":"`+plan+`"`) {
		t.Fatalf("the host's: %s", b)
	}
	var back HostAttentionMsg
	if err := json.Unmarshal([]byte(`{"t":"host_attention","sessionId":"s","state":"needs_input","plan":"`+plan+`"}`), &back); err != nil || back.Plan != plan {
		t.Fatalf("back: %v %+v", err, back)
	}
	// The fullest question: six choices of the longest labels and inputs,
	// every byte a quote (the longest raw escape), the longest message, an
	// id, the mark, and a plan at MaxPlanPath.
	opts := make([]AttentionOption, 6)
	for i := range opts {
		opts[i] = AttentionOption{Label: strings.Repeat(`"`, 60*4), Input: strings.Repeat(`"`, MaxAnswerInput), Mode: true}
	}
	full := Attention{T: CtlAttention, State: "needs_input", Message: strings.Repeat(`"`, 500), Source: "question", Kind: "permission", Options: opts, ID: strings.Repeat("0", 16), Answered: "terminal", Plan: strings.Repeat(`"`, MaxPlanPath)}
	if n := len(MustControlRaw(full)); n > MaxControl {
		t.Fatalf("the fullest question with a plan is %d bytes, over %d", n, MaxControl)
	}
}

// A question that mirrors a durable decision (docs/plans/decisions.md,
// PR-5) carries its id in `decision`, and only then; the id is "dr-<n>" of
// at most MaxDecisionRef bytes, and the fullest question with a decision at
// its bound still fits a control frame, MaxAttentionMessage's text
// unchanged.
func TestAttentionDecisionBound(t *testing.T) {
	b, err := json.Marshal(Attention{T: CtlAttention, State: "needs_input", Source: "api", Decision: "dr-7"})
	if err != nil || !strings.Contains(string(b), `"decision":"dr-7"`) {
		t.Fatalf("%s %v", b, err)
	}
	if b, _ := json.Marshal(Attention{T: CtlAttention, State: "needs_input", Source: "api"}); strings.Contains(string(b), "decision") {
		t.Fatalf("no decision: %s", b)
	}
	longest := "dr-" + strings.Repeat("9", MaxDecisionRef-3)
	for ref, ok := range map[string]bool{
		"dr-1": true, "dr-7": true, longest: true, "dr-9223372036854775807": false,
		longest + "9": false, "dr-": false, "dr-0": false, "dr-07": false, "dr-1a": false, "DR-1": false, "1": false, "": false, "dr--1": false, "dr-1 ": false,
	} {
		if ValidDecisionRef(ref) != ok {
			t.Errorf("ValidDecisionRef(%q) = %v, want %v", ref, !ok, ok)
		}
	}
	if len(longest) != MaxDecisionRef {
		t.Fatalf("the longest ref is %d bytes", len(longest))
	}
	opts := make([]AttentionOption, 6)
	for i := range opts {
		opts[i] = AttentionOption{Label: strings.Repeat(`"`, 60*4), Input: strings.Repeat(`"`, MaxAnswerInput), Mode: true}
	}
	full := Attention{T: CtlAttention, State: "needs_input", Message: strings.Repeat(`"`, 500), Source: "question", Kind: "permission", Options: opts, ID: strings.Repeat("0", 16), Answered: "terminal", Plan: strings.Repeat(`"`, MaxPlanPath), Decision: longest}
	if n := len(MustControlRaw(full)); n > MaxControl {
		t.Fatalf("the fullest question with a decision is %d bytes, over %d", n, MaxControl)
	}
}
