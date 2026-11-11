package proto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The host and the server are built apart and may differ by a release, so the
// field names of the host `activity` message are part of the contract.
func TestHostActivityMsgWireShape(t *testing.T) {
	msg := HostActivityMsg{T: HostActivity, SessionID: "0123456789abcdef", Entry: Activity{
		T: CtlActivity, At: "2026-09-29T12:00:00Z", Type: "artifact", By: "b1", ByName: "agent",
		Message: "PR opened", URL: "https://github.com/x/y/pull/1", To: "review", Tool: "gh",
	}}
	got, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":"activity","sessionId":"0123456789abcdef","entry":{"t":"activity","at":"2026-09-29T12:00:00Z","type":"artifact","by":"b1","byName":"agent","message":"PR opened","url":"https://github.com/x/y/pull/1","to":"review","tool":"gh"}}`
	if string(got) != want {
		t.Fatalf("host activity message\n got %s\nwant %s", got, want)
	}
	var back HostActivityMsg
	if err := json.Unmarshal(got, &back); err != nil || back != msg {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

// The server never sends a session id: the host has one session, and the
// connection says which.
func TestHostActivityMsgOmitsAnEmptySessionAndEmptyFields(t *testing.T) {
	got, err := json.Marshal(HostActivityMsg{T: HostActivity, Entry: Activity{T: CtlActivity, At: "2026-09-29T12:00:00Z", Type: "progress"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":"activity","entry":{"t":"activity","at":"2026-09-29T12:00:00Z","type":"progress"}}`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// A viewer_leave carries its reason when the viewer's link went, none
// otherwise, so that an older host reads the same message it always did;
// a host acts on the two reasons it knows, and on nothing else: an unknown
// reason, or one past MaxLeaveReason, is none.
func TestViewerLeaveWireShapeAndReasons(t *testing.T) {
	b, _ := json.Marshal(ViewerLeave{T: HostViewerLeave, ViewerID: "0123456789abcdef", Reason: LeaveExpired})
	if want := `{"t":"viewer_leave","viewerId":"0123456789abcdef","reason":"expired"}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	b, _ = json.Marshal(ViewerLeave{T: HostViewerLeave, ViewerID: "0123456789abcdef"})
	if want := `{"t":"viewer_leave","viewerId":"0123456789abcdef"}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var old ViewerRef
	if err := json.Unmarshal([]byte(`{"t":"viewer_leave","viewerId":"0123456789abcdef","reason":"revoked"}`), &old); err != nil || old.ViewerID != "0123456789abcdef" {
		t.Fatalf("an older host's read: %+v %v", old, err)
	}
	for in, want := range map[string]string{
		"": "", LeaveRevoked: LeaveRevoked, LeaveExpired: LeaveExpired,
		"left": "", "Revoked": "", strings.Repeat("r", MaxLeaveReason+1): "", "revoked\x00": "",
	} {
		if got := LeaveReason(in); got != want {
			t.Errorf("LeaveReason(%q) = %q, want %q", in, got, want)
		}
	}
	if len(LeaveRevoked) > MaxLeaveReason || len(LeaveExpired) > MaxLeaveReason {
		t.Fatal("a reason is over its bound")
	}
}

// link_gone names a link alone.
func TestLinkGoneWireShape(t *testing.T) {
	b, _ := json.Marshal(LinkGone{T: HostLinkGone, LinkID: "l1"})
	if want := `{"t":"link_gone","linkId":"l1"}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var m LinkGone
	if err := json.Unmarshal(b, &m); err != nil || m != (LinkGone{T: HostLinkGone, LinkID: "l1"}) {
		t.Fatalf("round trip: %+v %v", m, err)
	}
}

// viewer_behind names a viewer and says behind, false as well as true.
func TestViewerBehindWireShape(t *testing.T) {
	for behind, want := range map[bool]string{
		true:  `{"t":"viewer_behind","viewerId":"0123456789abcdef","behind":true}`,
		false: `{"t":"viewer_behind","viewerId":"0123456789abcdef","behind":false}`,
	} {
		in := ViewerBehind{T: HostViewerBehind, ViewerID: "0123456789abcdef", Behind: behind}
		b, _ := json.Marshal(in)
		if string(b) != want {
			t.Fatalf("got %s want %s", b, want)
		}
		var m ViewerBehind
		if err := json.Unmarshal(b, &m); err != nil || m != in {
			t.Fatalf("round trip: %+v %v", m, err)
		}
	}
}

// The register message carries the host's instance and the session's local
// id; registered always carries its links, [] when none, and a reply
// without the field (an older server) reads as nil.
func TestRegisterWireShapeCarriesInstanceAndLocalID(t *testing.T) {
	b, _ := json.Marshal(Register{T: HostRegister, Proto: ProtoVersion, Host: HostInfo{Name: "laptop", Instance: "secret"}, Session: HostSession{Command: []string{"bash"}, LocalID: "s1"}})
	if !strings.Contains(string(b), `"instance":"secret"`) || !strings.Contains(string(b), `"localId":"s1"`) {
		t.Fatalf("register %s", b)
	}
	b, _ = json.Marshal(Registered{T: HostRegistered, Links: []string{}})
	if !strings.Contains(string(b), `"links":[]`) {
		t.Fatalf("registered %s", b)
	}
	var old Registered
	if err := json.Unmarshal([]byte(`{"t":"registered","sessionId":"x"}`), &old); err != nil || old.Links != nil {
		t.Fatalf("an older server's registered: %+v %v", old, err)
	}
}

// A run group is checked against its bounds, and a full one fits a host message.
func TestRunGroupValidateBounds(t *testing.T) {
	member := func(i int) RunMember {
		return RunMember{Name: fmt.Sprintf("m%02d", i), AgentID: "claude", Status: "running", SessionID: strings.Repeat("a", 16)}
	}
	g := RunGroup{ID: strings.Repeat("r", MaxRunID), Name: strings.Repeat("n", MaxRunName)}
	for i := range MaxRunLinkMembers {
		g.Members = append(g.Members, member(i))
	}
	g.Revision = MaxRunRevision
	if err := g.Validate(); err != nil {
		t.Fatalf("a full group: %v", err)
	}
	b, _ := json.Marshal(HostRunLinkMsg{T: HostRunLink, RequestID: "r", Role: "view", Label: strings.Repeat("l", MaxLinkLabel), Run: g})
	if len(b) > MaxHostMessage/4 {
		t.Fatalf("a full link_run is %d bytes", len(b))
	}
	bad := []func(*RunGroup){
		func(g *RunGroup) { g.Members = append(g.Members, member(99)) },
		func(g *RunGroup) { g.Members[0].Status = "dancing" },
		func(g *RunGroup) { g.Members[0].Name = strings.Repeat("x", MaxRunMemberName+1) },
		func(g *RunGroup) { g.Members[1].Name = g.Members[0].Name },
		func(g *RunGroup) { g.ID = strings.Repeat("r", MaxRunID+1) },
		func(g *RunGroup) { g.Members = nil },
		func(g *RunGroup) { g.Revision = -1 },
		func(g *RunGroup) { g.Revision = MaxRunRevision + 1 },
	}
	for i, mutate := range bad {
		c := g
		c.Members = append([]RunMember(nil), g.Members...)
		mutate(&c)
		if c.Validate() == nil {
			t.Fatalf("bad group %d passed", i)
		}
	}
}

// A group's revision goes on the wire only when set; a host that sends
// none (an older one) reads as 0, and a reader that knows no revision (an
// older switchyard, which decodes host messages without refusing unknown
// fields) reads the rest of a group that has one.
func TestRunGroupRevisionOnTheWire(t *testing.T) {
	g := RunGroup{ID: "api-1", Name: "api", Members: []RunMember{{Name: "lead", AgentID: "claude", Status: "running"}}}
	b, _ := json.Marshal(HostRunLinkUpdateMsg{T: HostRunLinkUpdate, RequestID: "u", Run: g})
	if strings.Contains(string(b), "revision") {
		t.Fatalf("a group without a revision: %s", b)
	}
	g.Revision = 1760000000123
	b, _ = json.Marshal(HostRunLinkUpdateMsg{T: HostRunLinkUpdate, RequestID: "u", Run: g})
	if !strings.Contains(string(b), `"revision":1760000000123`) {
		t.Fatalf("a group with a revision: %s", b)
	}
	var older struct {
		T         string `json:"t"`
		RequestID string `json:"requestId"`
		Run       struct {
			ID      string      `json:"id"`
			Name    string      `json:"name"`
			Members []RunMember `json:"members"`
		} `json:"run"`
	}
	if err := json.Unmarshal(b, &older); err != nil || older.Run.ID != "api-1" || len(older.Run.Members) != 1 {
		t.Fatalf("an older reader: %+v %v", older, err)
	}
	var m HostRunLinkUpdateMsg
	if err := json.Unmarshal([]byte(`{"t":"link_run_update","requestId":"u","run":{"id":"api-1","name":"api","members":[]}}`), &m); err != nil || m.Run.Revision != 0 {
		t.Fatalf("an older host's update: %+v %v", m, err)
	}
}

func TestHostRunLinkMsgWireShape(t *testing.T) {
	b, _ := json.Marshal(HostRunLinkMsg{T: HostRunLink, RequestID: "r", Role: "view", Run: RunGroup{ID: "api-1", Name: "api", Members: []RunMember{{Name: "lead", AgentID: "claude", Status: "pending"}}}})
	if !strings.Contains(string(b), `"t":"link_run"`) || !strings.Contains(string(b), `"members":[{"name":"lead","agentId":"claude","status":"pending"}]`) {
		t.Fatalf("link_run %s", b)
	}
	b, _ = json.Marshal(LinkCreated{T: HostLinkCreated, RunID: "api-1"})
	if !strings.Contains(string(b), `"runId":"api-1"`) {
		t.Fatalf("link_created %s", b)
	}
}

// The host attention message carries the mark of the agent's own question
// (#193) as ask, both ways, and leaves it out when unset, so that an older
// host or server reads the message it always did. The largest message
// (500 bytes of words, six choices at their bounds, an id, the mark) is far
// within MaxHostMessage.
func TestHostAttentionMsgCarriesTheMark(t *testing.T) {
	b, err := json.Marshal(HostAttentionMsg{T: HostAttention, State: "needs_input", Message: "Which database?", Source: "api", Ask: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"t":"attention","state":"needs_input","message":"Which database?","source":"api","ask":true}`; string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
	var back HostAttentionMsg
	if err := json.Unmarshal(b, &back); err != nil || !back.Ask {
		t.Fatalf("round trip %+v %v", back, err)
	}
	if b, _ := json.Marshal(HostAttentionMsg{T: HostAttention, State: "done"}); strings.Contains(string(b), "ask") {
		t.Fatalf("an unmarked change %s", b)
	}
	big := HostAttentionMsg{T: HostAttention, SessionID: strings.Repeat("s", 64), State: "needs_input", Message: strings.Repeat("é", 250), Source: "question", Kind: "permission", ID: strings.Repeat("0", 32), Ask: true}
	for range 6 {
		big.Options = append(big.Options, AttentionOption{Label: strings.Repeat("é", 60), Input: strings.Repeat("\x01", MaxAnswerInput), Mode: true})
	}
	if b, _ := json.Marshal(big); len(b) > MaxHostMessage/8 {
		t.Fatalf("the largest attention message is %d bytes", len(b))
	}
}

// registered's recipe (#293): its wire names, absent when there is none
// (an older host and an older server read each other as before), and its
// bounds: Check refuses what is past the catalog's own. That registered
// stays within a host message with one is the server's to keep
// (registeredMessage in internal/api).
func TestRegisteredRecipeWireShapeAndBounds(t *testing.T) {
	b, _ := json.Marshal(Registered{T: HostRegistered, Links: []string{}})
	if strings.Contains(string(b), "recipe") {
		t.Fatalf("a registered without a recipe names one: %s", b)
	}
	r := HostRecipe{TrustPrompt: `Trust\s*this\s*folder\?`, TrustAnswers: []RecipeAnswer{{Label: "Yes", Input: "\r"}}, WorkingTitle: "Working",
		ConfirmSubmit: true, Questions: []RecipeQuestion{{Prompt: "proceed", Words: "w", Anytime: true, Answers: []RecipeAnswer{{Label: "Yes, proceed", Input: "y"}}, AnswersFromScreen: true, ModeAnswers: "m", TypedAnswers: "t"}}}
	b, _ = json.Marshal(Registered{T: HostRegistered, Links: []string{}, Recipe: &r})
	for _, want := range []string{`"recipe":{`, `"trustPrompt":`, `"trustAnswers":[{"label":"Yes","input":"\r"}]`, `"workingTitle":"Working"`, `"confirmSubmit":true`,
		`"questions":[{"prompt":"proceed","words":"w","anytime":true,"answers":[{"label":"Yes, proceed","input":"y"}],"answersFromScreen":true,"modeAnswers":"m","typedAnswers":"t"}]`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("registered %s lacks %s", b, want)
		}
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	var old Registered
	if err := json.Unmarshal([]byte(`{"t":"registered","sessionId":"x","links":[]}`), &old); err != nil || old.Recipe != nil {
		t.Fatalf("an older server's registered: %+v %v", old, err)
	}

	long := strings.Repeat("x", MaxRecipePattern+1)
	answers := func(n int) []RecipeAnswer {
		out := make([]RecipeAnswer, n)
		for i := range out {
			out[i] = RecipeAnswer{Label: "a", Input: "k"}
		}
		return out
	}
	for name, bad := range map[string]HostRecipe{
		"a long trust prompt":    {TrustPrompt: long},
		"a long working title":   {WorkingTitle: long},
		"too many trust answers": {TrustAnswers: answers(MaxRecipeAnswers + 1)},
		"an answer with no keys": {TrustAnswers: []RecipeAnswer{{Label: "a"}}},
		"a blank label":          {TrustAnswers: []RecipeAnswer{{Label: " ", Input: "k"}}},
		"a long label":           {TrustAnswers: []RecipeAnswer{{Label: strings.Repeat("é", MaxRecipeAnswerLabel+1), Input: "k"}}},
		"long keys":              {TrustAnswers: []RecipeAnswer{{Label: "a", Input: strings.Repeat("k", MaxRecipeAnswerInput+1)}}},
		"too many questions":     {Questions: make([]RecipeQuestion, MaxRecipeQuestions+1)},
		"a question no prompt":   {Questions: []RecipeQuestion{{}}},
		"a long question word":   {Questions: []RecipeQuestion{{Prompt: "p", Words: long}}},
		"a long mode pattern":    {Questions: []RecipeQuestion{{Prompt: "p", ModeAnswers: long}}},
		"a long typed pattern":   {Questions: []RecipeQuestion{{Prompt: "p", TypedAnswers: long}}},
		"too many answers":       {Questions: []RecipeQuestion{{Prompt: "p", Answers: answers(MaxRecipeAnswers + 1)}}},
	} {
		if bad.Check() == nil {
			t.Errorf("%s: checked", name)
		}
	}
	// A label is counted trimmed, as the catalog counts it: one at the
	// bound with spaces around it is the catalog's and passes; one past
	// the bound once trimmed does not.
	atBound := " " + strings.Repeat("é", MaxRecipeAnswerLabel) + " "
	padded := HostRecipe{TrustAnswers: []RecipeAnswer{{Label: atBound, Input: "k"}}, Questions: []RecipeQuestion{{Prompt: "p", Answers: []RecipeAnswer{{Label: atBound, Input: "k"}}}}}
	if err := padded.Check(); err != nil {
		t.Errorf("a padded label at the bound: %v", err)
	}
	past := HostRecipe{TrustAnswers: []RecipeAnswer{{Label: " " + strings.Repeat("é", MaxRecipeAnswerLabel+1) + " ", Input: "k"}}}
	if past.Check() == nil {
		t.Error("a padded label past the bound: checked")
	}
}

// TestHostTokenMessage holds host_token to its bounds and its strict
// decode, and registered's renews to being left out when false.
func TestHostTokenMessage(t *testing.T) {
	// Every token68 character at once, and a token at its bound.
	for _, tok := range []string{"Az09-._~+/=", strings.Repeat("A", MaxHostToken)} {
		b, _ := json.Marshal(HostTokenMsg{T: HostTokenType, Token: tok})
		if len(b) > MaxHostTokenMessage {
			t.Fatalf("a token of %d bytes is a message of %d, past %d", len(tok), len(b), MaxHostTokenMessage)
		}
		if got, err := ParseHostToken(b); err != nil || got != tok {
			t.Errorf("ParseHostToken(%d bytes) = %d bytes, %v", len(tok), len(got), err)
		}
	}
	for name, msg := range map[string]string{
		"past the token's bound": `{"t":"host_token","token":"` + strings.Repeat("A", MaxHostToken+1) + `"}`,
		"past the message's":     `{"t":"host_token","token":"a"}` + strings.Repeat(" ", MaxHostTokenMessage),
		"empty":                  `{"t":"host_token","token":""}`,
		"not token68":            `{"t":"host_token","token":"a b"}`,
		"escaped":                `{"t":"host_token","token":"a\u0000"}`,
		"an unknown field":       `{"t":"host_token","token":"a","more":1}`,
		"another type":           `{"t":"status","token":"a"}`,
		"two values":             `{"t":"host_token","token":"a"}{}`,
	} {
		if _, err := ParseHostToken([]byte(msg)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	b, _ := json.Marshal(Registered{T: HostRegistered})
	if strings.Contains(string(b), "renews") {
		t.Errorf("registered without renews says it: %s", b)
	}
	b, _ = json.Marshal(Registered{T: HostRegistered, Renews: true})
	if !strings.Contains(string(b), `"renews":true`) {
		t.Errorf("registered with renews: %s", b)
	}
}
