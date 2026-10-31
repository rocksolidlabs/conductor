package ext_test

import (
	"bytes"
	"encoding/json"
	"reflect"
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
		Plan:    "the plan", Since: at,
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
	&ext.Resolution{ID: "q1", Session: session, Ended: true, At: at},
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

// make8 is MaxDestinations destinations of one kind.
func make8(kind, name string) []ext.Destination {
	out := make([]ext.Destination, ext.MaxDestinations)
	for i := range out {
		out[i] = ext.Destination{Kind: kind, Name: name}
	}
	return out
}
