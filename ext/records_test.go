package ext_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
)

var at = time.Date(2026, 10, 31, 9, 5, 0, 0, time.UTC)

var (
	principal = ext.Principal{Subject: "u-123", Name: "Ada", Grants: []ext.Grant{ext.GrantAdmin, ext.GrantHost}}
	session   = ext.SessionRef{ID: "0123456789abcdef", Name: "builder", AgentID: "claude", Run: "r1", Task: "build", Attempt: 2}
	decision  = ext.Decision{
		ID: "q1", Session: session, Message: "Allow the edit?", Kind: "permission", Source: "question",
		Options: []ext.DecisionOption{{Label: "Yes", Input: "1"}, {Label: "No", Input: "2"}},
		Plan:    "the plan", Since: at, Request: "dr-7",
	}
)

// records is one of every wire record of ext, every field set, so the
// round trip carries each.
var records = []any{
	&principal,
	&session,
	&decision,
	&ext.DecisionOption{Label: "Yes", Input: "1"},
	&ext.Route{To: []ext.Destination{{Kind: "person", Name: "Ada"}}, Rule: "owner of the file", Answer: &ext.Answer{Input: "1", Text: "yes", By: principal}},
	&ext.Destination{Kind: "role", Name: "reviewer"},
	&ext.Answer{Input: "1", Text: "yes", By: principal},
	&ext.Resolution{ID: "q1", Session: session, Ended: true, At: at, Request: "dr-7"},
	&ext.TouchedFile{Session: session, Path: "/src/a.go", Op: "edit", Tool: "Edit", By: "claude", At: at},
	&ext.ContextQuery{
		Run: "r1", Crew: "c1", Task: "build", Attempt: 1, Kind: "work", Goal: "g", Instructions: "i",
		Worktree: "/src/.conductor/worktrees/r1/build.1", Dir: "web", Start: "abc123",
		Inputs: []ext.ContextInput{{Task: "plan", Commit: "def456"}},
	},
	&ext.ContextInput{Task: "plan", Commit: "def456"},
	&ext.ContextBlock{Title: "Callers", Ref: "internal/a.go", Body: "a calls b"},
	&ext.Notification{
		Event: "needs_input", Type: "attention", At: at, Session: session, State: "open", Message: "m",
		URL: "https://example.com/s", To: "ada", Tool: "Edit", Op: "edit", Path: "/src/a.go", By: "v1", ByName: "Ada", Origin: "push",
	},
	&ext.Doc{Store: "data", Path: "crews/c1.json", Epoch: strings.Repeat("ab", 16), Rev: 3, At: at, Size: 10, Offset: 4, Data: []byte("{}")},
	&ext.Entitlement{Allowed: true, Limit: 5, Until: at, Why: "plan"},
	&ext.ICERequest{Purpose: "viewer", Session: "s1", Role: "control", Viewer: "v1"},
	&ext.ICEServer{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "c"},
	&ext.RoomRequest{Kind: "ticket", ID: "CND-1", By: principal},
	&ext.RoomJoin{URL: "https://rooms.example.com/r", Token: "t", ExpiresAt: at},
	&ext.Registration{Kind: ext.KindIdentity, Name: "cloud"},
	&ext.LaunchRequest{Crew: "c1", Label: "nightly", By: principal},
	&ext.RunRef{ID: "r1", Crew: "c1", URL: "https://conductor.example.com/runs/r1"},
	&ext.SubmitRequest{Session: "s1", Question: "q1", Input: "1", Text: "yes", By: principal},
	&ext.SubmitResult{Status: "entered"},
	&ext.TurnLaunch{Agent: "claude", Model: "opus", AllowTools: true, Folder: "/src", Reads: []string{"/home/me"}, Line: "Brakeman request br-1", Label: "Brakeman · briefing br-1", Request: "br-1"},
	&ext.ApprovedLaunch{Ticket: "CND-12", Plan: "0123456789abcdef", ExpectedRev: 3, OpID: "op-1", Label: "nightly", By: principal},
}

func TestRecordsRoundTrip(t *testing.T) {
	for _, rec := range records {
		typ := reflect.TypeOf(rec).Elem()
		t.Run(typ.Name(), func(t *testing.T) {
			v := reflect.ValueOf(rec).Elem()
			for i := range typ.NumField() {
				f := typ.Field(i)
				tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if tag == "" || tag == "-" || tag[0] < 'a' || tag[0] > 'z' || strings.ContainsAny(tag, "_-") {
					t.Errorf("field %s has json name %q, want camelCase", f.Name, tag)
				}
				if v.Field(i).IsZero() {
					t.Errorf("field %s is zero in the sample: the round trip would not carry it", f.Name)
				}
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			back := reflect.New(typ)
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(back.Interface()); err != nil {
				t.Fatalf("decoding %s: %v", data, err)
			}
			if !reflect.DeepEqual(back.Interface(), rec) {
				t.Fatalf("round trip of %s =\n%+v\nwant\n%+v", data, back.Interface(), rec)
			}
		})
	}

	// Times are left out when zero where the record says so, and what may
	// be absent is.
	for _, c := range []struct {
		rec  any
		want string
	}{
		{ext.Entitlement{}, `{"allowed":false}`},
		{ext.RoomJoin{URL: "u"}, `{"url":"u"}`},
		{ext.Route{}, `{}`},
		{ext.Principal{Subject: "s", Name: "n"}, `{"subject":"s","name":"n"}`},
	} {
		data, err := json.Marshal(c.rec)
		if err != nil || string(data) != c.want {
			t.Errorf("json.Marshal(%T) = %s, %v; want %s", c.rec, data, err, c.want)
		}
	}
}

func TestValid(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	by := ext.Principal{Subject: "u-1", Name: "Ada"}
	abs := filepath.Join(os.TempDir(), "brakeman")
	turn := ext.TurnLaunch{Agent: "claude", Model: "claude-opus-5-5", Folder: abs, Line: "Brakeman request br-1 (briefing)", Label: "Brakeman · briefing br-1", Request: "br-1"}
	approved := ext.ApprovedLaunch{Ticket: "CND-12", Plan: "0123456789abcdef", ExpectedRev: 4, OpID: "bk0123456789abcdef0123456789abcd", Label: "nightly", By: by}
	cases := []struct {
		name string
		err  error
		ok   bool
	}{
		// Principal.
		{"principal", principal.Valid(), true},
		{"principal without grants", by.Valid(), true},
		{"principal subject at its bound", ext.Principal{Subject: long(ext.MaxSubject), Name: "n"}.Valid(), true},
		{"principal subject past its bound", ext.Principal{Subject: long(ext.MaxSubject + 1), Name: "n"}.Valid(), false},
		{"principal subject empty", ext.Principal{Name: "n"}.Valid(), false},
		{"principal subject with a space", ext.Principal{Subject: "a b", Name: "n"}.Valid(), false},
		{"principal subject not ASCII", ext.Principal{Subject: "ü", Name: "n"}.Valid(), false},
		{"principal name at its bound in characters", ext.Principal{Subject: "s", Name: strings.Repeat("é", ext.MaxPrincipalName)}.Valid(), true},
		{"principal name past its bound", ext.Principal{Subject: "s", Name: long(ext.MaxPrincipalName + 1)}.Valid(), false},
		{"principal name empty", ext.Principal{Subject: "s"}.Valid(), false},
		{"principal name of two lines", ext.Principal{Subject: "s", Name: "a\nb"}.Valid(), false},
		{"principal unknown grant", ext.Principal{Subject: "s", Name: "n", Grants: []ext.Grant{"reviewer"}}.Valid(), false},
		{"principal grant twice", ext.Principal{Subject: "s", Name: "n", Grants: []ext.Grant{ext.GrantHost, ext.GrantHost}}.Valid(), false},

		// Route, against its decision.
		{"route empty", ext.Route{}.Valid(decision), true},
		{"route to the bound", ext.Route{To: make8("person", "Ada"), Rule: long(ext.MaxRouteRule)}.Valid(decision), true},
		{"route past its destinations", ext.Route{To: append(make8("person", "Ada"), ext.Destination{Kind: "crew", Name: "c"})}.Valid(decision), false},
		{"route unknown destination kind", ext.Route{To: []ext.Destination{{Kind: "team", Name: "a"}}}.Valid(decision), false},
		{"route destination without a name", ext.Route{To: []ext.Destination{{Kind: "role"}}}.Valid(decision), false},
		{"route destination name past its bound", ext.Route{To: []ext.Destination{{Kind: "role", Name: long(ext.MaxDestinationName + 1)}}}.Valid(decision), false},
		{"route rule past its bound", ext.Route{Rule: long(ext.MaxRouteRule + 1)}.Valid(decision), false},
		{"route rule of two lines", ext.Route{Rule: "a\nb"}.Valid(decision), false},
		{"route answer a choice offered", ext.Route{Answer: &ext.Answer{Input: "2", By: by}}.Valid(decision), true},
		{"route answer a choice not offered", ext.Route{Answer: &ext.Answer{Input: "3", By: by}}.Valid(decision), false},
		{"route answer a choice of a question with none", ext.Route{Answer: &ext.Answer{Input: "1", By: by}}.Valid(ext.Decision{ID: "q2"}), false},
		{"route answer a line", ext.Route{Answer: &ext.Answer{Text: "use the second\nand say why", By: by}}.Valid(decision), true},
		{"route answer a line at its bound", ext.Route{Answer: &ext.Answer{Text: long(ext.MaxAnswerText), By: by}}.Valid(decision), true},
		{"route answer a line past its bound", ext.Route{Answer: &ext.Answer{Text: long(ext.MaxAnswerText + 1), By: by}}.Valid(decision), false},
		{"route answer a line with an escape", ext.Route{Answer: &ext.Answer{Text: "y\x1b[A", By: by}}.Valid(decision), false},
		{"route answer both", ext.Route{Answer: &ext.Answer{Input: "1", Text: "yes", By: by}}.Valid(decision), false},
		{"route answer neither", ext.Route{Answer: &ext.Answer{By: by}}.Valid(decision), false},
		{"route answer by no one", ext.Route{Answer: &ext.Answer{Input: "1"}}.Valid(decision), false},

		// ContextBlock.
		{"context block", ext.ContextBlock{Title: "t", Ref: "a.go", Body: "line\n\tindented\r\n"}.Valid(), true},
		{"context block at its bounds", ext.ContextBlock{Title: long(ext.MaxContextTitle), Ref: long(ext.MaxContextRef), Body: long(ext.MaxContextBody)}.Valid(), true},
		{"context block without a title", ext.ContextBlock{Body: "b"}.Valid(), false},
		{"context block title past its bound", ext.ContextBlock{Title: long(ext.MaxContextTitle + 1), Body: "b"}.Valid(), false},
		{"context block title of two lines", ext.ContextBlock{Title: "a\nb", Body: "b"}.Valid(), false},
		{"context block ref past its bound", ext.ContextBlock{Title: "t", Ref: long(ext.MaxContextRef + 1), Body: "b"}.Valid(), false},
		{"context block without a body", ext.ContextBlock{Title: "t"}.Valid(), false},
		{"context block body past its bound", ext.ContextBlock{Title: "t", Body: long(ext.MaxContextBody + 1)}.Valid(), false},
		{"context block body not UTF-8", ext.ContextBlock{Title: "t", Body: "\xff"}.Valid(), false},
		{"context block body with an escape", ext.ContextBlock{Title: "t", Body: "\x1b]0;x\x07"}.Valid(), false},

		// ICEServer.
		{"ice server stun", ext.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}}.Valid(), true},
		{"ice server every scheme", ext.ICEServer{URLs: []string{"stun:a", "stuns:a", "turn:a", "turns:a?transport=tcp"}, Username: "u", Credential: "c"}.Valid(), true},
		{"ice server without urls", ext.ICEServer{}.Valid(), false},
		{"ice server past its urls", ext.ICEServer{URLs: []string{"stun:a", "stun:b", "stun:c", "stun:d", "stun:e"}}.Valid(), false},
		{"ice server url past its bound", ext.ICEServer{URLs: []string{"turn:" + long(ext.MaxICEURL)}}.Valid(), false},
		{"ice server url of another scheme", ext.ICEServer{URLs: []string{"https://a"}}.Valid(), false},
		{"ice server url of a scheme alone", ext.ICEServer{URLs: []string{"turn:"}}.Valid(), false},
		{"ice server url with a space", ext.ICEServer{URLs: []string{"turn:a b"}}.Valid(), false},
		{"ice server username past its bound", ext.ICEServer{URLs: []string{"turn:a"}, Username: long(ext.MaxICESecret + 1), Credential: "c"}.Valid(), false},
		{"ice server credential at its bound", ext.ICEServer{URLs: []string{"turn:a"}, Username: "u", Credential: long(ext.MaxICESecret)}.Valid(), true},
		{"ice server credential past its bound", ext.ICEServer{URLs: []string{"turn:a"}, Username: "u", Credential: long(ext.MaxICESecret + 1)}.Valid(), false},
		// ICEServer, as WebRTC reads it: one server it refuses fails the
		// whole configuration, the configured servers with it.
		{"ice server stun with a port", ext.ICEServer{URLs: []string{"stun:stun.example.com:3478"}}.Valid(), true},
		{"ice server ipv6 host", ext.ICEServer{URLs: []string{"stun:[2001:db8::1]", "stun:[2001:db8::1]:3478"}}.Valid(), true},
		{"ice server turn over udp", ext.ICEServer{URLs: []string{"turn:a:3478?transport=udp"}, Username: "u", Credential: "c"}.Valid(), true},
		{"ice server port not a number", ext.ICEServer{URLs: []string{"stun:example:badport"}}.Valid(), false},
		{"ice server port empty", ext.ICEServer{URLs: []string{"stun:example:"}}.Valid(), false},
		{"ice server port zero", ext.ICEServer{URLs: []string{"stun:example:0"}}.Valid(), false},
		{"ice server port past 65535", ext.ICEServer{URLs: []string{"stun:example:65536"}}.Valid(), false},
		{"ice server port signed", ext.ICEServer{URLs: []string{"stun:example:+3478"}}.Valid(), false},
		{"ice server as a hierarchical url", ext.ICEServer{URLs: []string{"stun://example:3478"}}.Valid(), false},
		{"ice server host with a path", ext.ICEServer{URLs: []string{"stun:example/x"}}.Valid(), false},
		{"ice server host with user info", ext.ICEServer{URLs: []string{"turn:u@example"}, Username: "u", Credential: "c"}.Valid(), false},
		{"ice server ipv6 host without brackets", ext.ICEServer{URLs: []string{"stun:2001:db8::1"}}.Valid(), false},
		{"ice server brackets around a name", ext.ICEServer{URLs: []string{"stun:[example]"}}.Valid(), false},
		{"ice server brackets unclosed", ext.ICEServer{URLs: []string{"stun:[2001:db8::1"}}.Valid(), false},
		{"ice server junk after brackets", ext.ICEServer{URLs: []string{"stun:[2001:db8::1]x"}}.Valid(), false},
		{"ice server stun with a query", ext.ICEServer{URLs: []string{"stun:example?transport=udp"}}.Valid(), false},
		{"ice server turn of another transport", ext.ICEServer{URLs: []string{"turn:a?transport=sctp"}, Username: "u", Credential: "c"}.Valid(), false},
		{"ice server turn of another query", ext.ICEServer{URLs: []string{"turn:a?ttl=5"}, Username: "u", Credential: "c"}.Valid(), false},
		{"ice server scheme in capitals", ext.ICEServer{URLs: []string{"STUN:example"}}.Valid(), false},
		{"ice server turn without credentials", ext.ICEServer{URLs: []string{"turn:a"}}.Valid(), false},
		{"ice server turn without a credential", ext.ICEServer{URLs: []string{"turns:a"}, Username: "u"}.Valid(), false},
		{"ice server turn without a username", ext.ICEServer{URLs: []string{"stun:a", "turn:a"}, Credential: "c"}.Valid(), false},
		{"ice server stun with credentials", ext.ICEServer{URLs: []string{"stun:a"}, Username: "u", Credential: "c"}.Valid(), true},

		// Notification, as a provider's notice.
		{"notice", ext.Notification{Message: "Build finished", URL: "https://example.com/r/1"}.Valid(), true},
		{"notice at its bounds", ext.Notification{Message: long(ext.MaxNoticeMessage), URL: long(ext.MaxNoticeURL)}.Valid(), true},
		{"notice without a message", ext.Notification{URL: "https://example.com"}.Valid(), false},
		{"notice message past its bound", ext.Notification{Message: long(ext.MaxNoticeMessage + 1)}.Valid(), false},
		{"notice message of two lines", ext.Notification{Message: "a\nb"}.Valid(), false},
		{"notice url past its bound", ext.Notification{Message: "m", URL: long(ext.MaxNoticeURL + 1)}.Valid(), false},
		{"notice url of two lines", ext.Notification{Message: "m", URL: "https://a\nb"}.Valid(), false},
		{"notice field past its bound", ext.Notification{Message: "m", Path: long(ext.MaxNoticeMessage + 1)}.Valid(), false},
		{"notice session name of two lines", ext.Notification{Message: "m", Session: ext.SessionRef{Name: "a\nb"}}.Valid(), false},

		// SubmitRequest.
		{"submit a line", ext.SubmitRequest{Session: "s1", Text: "run the tests", By: by}.Valid(), true},
		{"submit a choice", ext.SubmitRequest{Session: "s1", Question: "q1", Input: "1", By: by}.Valid(), true},
		{"submit a line answering", ext.SubmitRequest{Session: "s1", Question: "q1", Text: "yes", By: by}.Valid(), true},
		{"submit without a session", ext.SubmitRequest{Text: "t", By: by}.Valid(), false},
		{"submit session past its bound", ext.SubmitRequest{Session: long(ext.MaxSessionRef + 1), Text: "t", By: by}.Valid(), false},
		{"submit question past its bound", ext.SubmitRequest{Session: "s1", Question: long(ext.MaxQuestionRef + 1), Text: "t", By: by}.Valid(), false},
		{"submit a choice without a question", ext.SubmitRequest{Session: "s1", Input: "1", By: by}.Valid(), false},
		{"submit both", ext.SubmitRequest{Session: "s1", Question: "q1", Input: "1", Text: "t", By: by}.Valid(), false},
		{"submit neither", ext.SubmitRequest{Session: "s1", Question: "q1", By: by}.Valid(), false},
		{"submit input past its bound", ext.SubmitRequest{Session: "s1", Question: "q1", Input: long(ext.MaxAnswerInput + 1), By: by}.Valid(), false},
		{"submit text past its bound", ext.SubmitRequest{Session: "s1", Text: long(ext.MaxAnswerText + 1), By: by}.Valid(), false},
		{"submit text with an escape", ext.SubmitRequest{Session: "s1", Text: "\x03", By: by}.Valid(), false},
		{"submit by no one", ext.SubmitRequest{Session: "s1", Text: "t"}.Valid(), false},

		// LaunchRequest.
		{"launch", ext.LaunchRequest{Crew: "my-crew", Label: "nightly run", By: by}.Valid(), true},
		{"launch without a label", ext.LaunchRequest{Crew: "0crew", By: by}.Valid(), true},
		{"launch crew id at its bound", ext.LaunchRequest{Crew: long(ext.MaxCrewID), By: by}.Valid(), true},
		{"launch crew id past its bound", ext.LaunchRequest{Crew: long(ext.MaxCrewID + 1), By: by}.Valid(), false},
		{"launch without a crew", ext.LaunchRequest{By: by}.Valid(), false},
		{"launch crew id not an id", ext.LaunchRequest{Crew: "My Crew", By: by}.Valid(), false},
		{"launch crew id beginning with a dash", ext.LaunchRequest{Crew: "-c", By: by}.Valid(), false},
		{"launch label at its bound", ext.LaunchRequest{Crew: "c", Label: strings.Repeat("é", ext.MaxRunLabel), By: by}.Valid(), true},
		{"launch label past its bound", ext.LaunchRequest{Crew: "c", Label: long(ext.MaxRunLabel + 1), By: by}.Valid(), false},
		{"launch label with a control character", ext.LaunchRequest{Crew: "c", Label: "a\tb", By: by}.Valid(), false},
		{"launch by no one", ext.LaunchRequest{Crew: "c"}.Valid(), false},

		// TurnLaunch.
		{"turn", turn.Valid(), true},
		{"turn with its default model", with(turn, func(r *ext.TurnLaunch) { r.Model = "" }).Valid(), true},
		{"turn model with a provider's path", with(turn, func(r *ext.TurnLaunch) { r.Model = "openai/gpt-5.4@high:x" }).Valid(), true},
		{"turn model at its bound", with(turn, func(r *ext.TurnLaunch) { r.Model = long(ext.MaxModel) }).Valid(), true},
		{"turn model past its bound", with(turn, func(r *ext.TurnLaunch) { r.Model = long(ext.MaxModel + 1) }).Valid(), false},
		{"turn model beginning with a dash", with(turn, func(r *ext.TurnLaunch) { r.Model = "-x" }).Valid(), false},
		{"turn model with a space", with(turn, func(r *ext.TurnLaunch) { r.Model = "a b" }).Valid(), false},
		{"turn without an agent", with(turn, func(r *ext.TurnLaunch) { r.Agent = "" }).Valid(), false},
		{"turn agent at its bound", with(turn, func(r *ext.TurnLaunch) { r.Agent = long(ext.MaxAgentID) }).Valid(), true},
		{"turn agent past its bound", with(turn, func(r *ext.TurnLaunch) { r.Agent = long(ext.MaxAgentID + 1) }).Valid(), false},
		{"turn agent in capitals", with(turn, func(r *ext.TurnLaunch) { r.Agent = "Claude" }).Valid(), false},
		{"turn folder relative", with(turn, func(r *ext.TurnLaunch) { r.Folder = "src" }).Valid(), false},
		{"turn without a folder", with(turn, func(r *ext.TurnLaunch) { r.Folder = "" }).Valid(), false},
		{"turn folder past its bound", with(turn, func(r *ext.TurnLaunch) { r.Folder = filepath.Join(abs, long(ext.MaxFolder)) }).Valid(), false},
		{"turn reads at their bound", with(turn, func(r *ext.TurnLaunch) { r.Reads = slices.Repeat([]string{abs}, ext.MaxTurnReads) }).Valid(), true},
		{"turn reads past their bound", with(turn, func(r *ext.TurnLaunch) { r.Reads = slices.Repeat([]string{abs}, ext.MaxTurnReads+1) }).Valid(), false},
		{"turn reads a relative folder", with(turn, func(r *ext.TurnLaunch) { r.Reads = []string{abs, "src"} }).Valid(), false},
		{"turn reads an empty folder", with(turn, func(r *ext.TurnLaunch) { r.Reads = []string{""} }).Valid(), false},
		{"turn reads a folder past its bound", with(turn, func(r *ext.TurnLaunch) { r.Reads = []string{filepath.Join(abs, long(ext.MaxFolder))} }).Valid(), false},
		{"turn reads a folder of two lines", with(turn, func(r *ext.TurnLaunch) { r.Reads = []string{abs + "\nx"} }).Valid(), false},
		{"turn without a line", with(turn, func(r *ext.TurnLaunch) { r.Line = "" }).Valid(), false},
		{"turn line at its bound", with(turn, func(r *ext.TurnLaunch) { r.Line = long(ext.MaxTurnLine) }).Valid(), true},
		{"turn line past its bound", with(turn, func(r *ext.TurnLaunch) { r.Line = long(ext.MaxTurnLine + 1) }).Valid(), false},
		{"turn line of two lines", with(turn, func(r *ext.TurnLaunch) { r.Line = "a\nb" }).Valid(), false},
		{"turn without a label", with(turn, func(r *ext.TurnLaunch) { r.Label = "" }).Valid(), false},
		{"turn label past its bound", with(turn, func(r *ext.TurnLaunch) { r.Label = long(ext.MaxRunLabel + 1) }).Valid(), false},
		{"turn request at its bound", with(turn, func(r *ext.TurnLaunch) { r.Request = "br-" + strings.Repeat("9", ext.MaxRequestRef-3) }).Valid(), true},
		{"turn request past its bound", with(turn, func(r *ext.TurnLaunch) { r.Request = "br-" + strings.Repeat("9", ext.MaxRequestRef-2) }).Valid(), false},
		{"turn request with a leading zero", with(turn, func(r *ext.TurnLaunch) { r.Request = "br-01" }).Valid(), false},
		{"turn request of another shape", with(turn, func(r *ext.TurnLaunch) { r.Request = "wb-1" }).Valid(), false},

		// ApprovedLaunch.
		{"approved launch", approved.Valid(), true},
		{"approved launch without a label or a revision", with(approved, func(r *ext.ApprovedLaunch) { r.Label, r.ExpectedRev = "", 0 }).Valid(), true},
		{"approved launch ticket of a long key", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "CND123-" + strings.Repeat("9", 18) }).Valid(), true},
		{"approved launch ticket number past 18 digits", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "CND-" + strings.Repeat("9", 19) }).Valid(), false},
		{"approved launch ticket project past its bound", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "CNDXYZW-1" }).Valid(), false},
		{"approved launch ticket in lower case", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "cnd-1" }).Valid(), false},
		{"approved launch ticket number zero", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "CND-0" }).Valid(), false},
		{"approved launch ticket without a number", with(approved, func(r *ext.ApprovedLaunch) { r.Ticket = "CND" }).Valid(), false},
		{"approved launch plan short", with(approved, func(r *ext.ApprovedLaunch) { r.Plan = "0123" }).Valid(), false},
		{"approved launch plan in capitals", with(approved, func(r *ext.ApprovedLaunch) { r.Plan = "0123456789ABCDEF" }).Valid(), false},
		{"approved launch revision below 0", with(approved, func(r *ext.ApprovedLaunch) { r.ExpectedRev = -1 }).Valid(), false},
		{"approved launch op id at its bound", with(approved, func(r *ext.ApprovedLaunch) { r.OpID = long(ext.MaxOpID) }).Valid(), true},
		{"approved launch op id past its bound", with(approved, func(r *ext.ApprovedLaunch) { r.OpID = long(ext.MaxOpID + 1) }).Valid(), false},
		{"approved launch without an op id", with(approved, func(r *ext.ApprovedLaunch) { r.OpID = "" }).Valid(), false},
		{"approved launch op id with a slash", with(approved, func(r *ext.ApprovedLaunch) { r.OpID = "a/b" }).Valid(), false},
		{"approved launch label past its bound", with(approved, func(r *ext.ApprovedLaunch) { r.Label = long(ext.MaxRunLabel + 1) }).Valid(), false},
		{"approved launch by no one", with(approved, func(r *ext.ApprovedLaunch) { r.By = ext.Principal{} }).Valid(), false},

		// Where.
		{"where native", ext.WhereNative.Valid(), true},
		{"where at its bound", ext.Where(long(ext.MaxWhere)).Valid(), true},
		{"where with a dash and digits", ext.Where("boxcar-2").Valid(), true},
		{"where empty", ext.Where("").Valid(), false},
		{"where past its bound", ext.Where(long(ext.MaxWhere + 1)).Valid(), false},
		{"where beginning with a digit", ext.Where("2vm").Valid(), false},
		{"where beginning with a dash", ext.Where("-vm").Valid(), false},
		{"where in capitals", ext.Where("Native").Valid(), false},
	}
	for _, c := range cases {
		if (c.err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, c.err, c.ok)
		}
		if c.err != nil && !strings.HasPrefix(c.err.Error(), "ext: ") {
			t.Errorf("%s: the refusal %q does not say it is ext's", c.name, c.err)
		}
	}
	if !ext.ValidName("cloud-2") || ext.ValidName("") || ext.ValidName("a.b") {
		t.Error("ValidName disagrees with its rule")
	}
}

// TestValidHostToken holds the host token to 1 to MaxHostToken bytes of
// token68's characters.
func TestValidHostToken(t *testing.T) {
	for _, tok := range []string{"a", "Ab-0._~+/=", strings.Repeat("x", ext.MaxHostToken)} {
		if !ext.ValidHostToken(tok) {
			t.Errorf("ValidHostToken refused %q", tok)
		}
	}
	for _, tok := range []string{"", strings.Repeat("x", ext.MaxHostToken+1), "a b", `a"b`, "a\\b", "é", "a\nb", "a%20"} {
		if ext.ValidHostToken(tok) {
			t.Errorf("ValidHostToken took %q", tok)
		}
	}
}

// TestGrantDecideValid holds the decide grant to the grants a principal may
// hold: alone and beside admin and host, never twice; an unknown grant's
// words name the three.
func TestGrantDecideValid(t *testing.T) {
	if !ext.GrantDecide.Valid() || ext.GrantDecide != "decide" {
		t.Fatalf("the decide grant %q is not valid", ext.GrantDecide)
	}
	for _, gs := range [][]ext.Grant{{ext.GrantDecide}, {ext.GrantAdmin, ext.GrantDecide}, {ext.GrantHost, ext.GrantDecide}, {ext.GrantAdmin, ext.GrantHost, ext.GrantDecide}} {
		if err := (ext.Principal{Subject: "s", Name: "n", Grants: gs}).Valid(); err != nil {
			t.Errorf("%v: %v", gs, err)
		}
	}
	if err := (ext.Principal{Subject: "s", Name: "n", Grants: []ext.Grant{ext.GrantDecide, ext.GrantDecide}}).Valid(); err == nil {
		t.Error("the decide grant given twice was taken")
	}
	err := ext.Principal{Subject: "s", Name: "n", Grants: []ext.Grant{"reviewer"}}.Valid()
	if err == nil || !strings.Contains(err.Error(), "is not admin, host or decide") {
		t.Errorf("an unknown grant's words: %v", err)
	}
}

// with is a copy of r changed by edit.
func with[T any](r T, edit func(*T)) T {
	edit(&r)
	return r
}

// make8 is MaxDestinations destinations of one kind.
func make8(kind, name string) []ext.Destination {
	out := make([]ext.Destination, ext.MaxDestinations)
	for i := range out {
		out[i] = ext.Destination{Kind: kind, Name: name}
	}
	return out
}
