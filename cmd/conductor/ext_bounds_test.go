package main

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// TestExtBoundsAreTheServers: ext imports no internal package, so it writes
// the server's own bounds out again; this test, which may import both, holds
// each copy to its source, so a change on one side fails here instead of
// letting a provider's request pass ext's check and fail the server's.
func TestExtBoundsAreTheServers(t *testing.T) {
	for _, c := range []struct {
		name      string
		ext, want int
	}{
		{"MaxQuestionRef = proto.MaxQuestionID", ext.MaxQuestionRef, proto.MaxQuestionID},
		{"MaxAnswerInput = proto.MaxAnswerInput", ext.MaxAnswerInput, proto.MaxAnswerInput},
		{"MaxAnswerText = proto.MaxSubmit", ext.MaxAnswerText, proto.MaxSubmit},
		{"MaxPrincipalName = proto.MaxNameLen", ext.MaxPrincipalName, proto.MaxNameLen},
	} {
		if c.ext != c.want {
			t.Errorf("%s: ext has %d, the server %d", c.name, c.ext, c.want)
		}
	}

	by := ext.Principal{Subject: "u-1", Name: "Ada"}
	// A crew id: ext's LaunchRequest.Valid and crew.ValidID agree at the
	// bound, one past it, and on the shape.
	for _, id := range []string{
		strings.Repeat("a", ext.MaxCrewID), strings.Repeat("a", ext.MaxCrewID+1),
		"a", "9-lives", "-a", "A", "a_b", "",
	} {
		extOK := ext.LaunchRequest{Crew: id, By: by}.Valid() == nil
		if extOK != crew.ValidID(id) {
			t.Errorf("crew id %q (%d bytes): ext takes it %v, crew.ValidID %v", id, len(id), extOK, crew.ValidID(id))
		}
	}
	// A run label: ext's check and crew.ValidateLabel agree at the bound in
	// characters, one past it, and on a control character.
	for _, label := range []string{
		strings.Repeat("é", ext.MaxRunLabel), strings.Repeat("é", ext.MaxRunLabel+1),
		strings.Repeat("a", ext.MaxRunLabel+1), "", "a\tb",
	} {
		extOK := ext.LaunchRequest{Crew: "c", Label: label, By: by}.Valid() == nil
		crewOK := crew.ValidateLabel(label) == nil
		if extOK != crewOK {
			t.Errorf("label of %d bytes: ext takes it %v, crew.ValidateLabel %v", len(label), extOK, crewOK)
		}
	}
	// A question id: ext's SubmitRequest.Valid and proto.ValidQuestionID
	// agree at the bound and one past it.
	for _, q := range []string{strings.Repeat("q", ext.MaxQuestionRef), strings.Repeat("q", ext.MaxQuestionRef+1)} {
		extOK := ext.SubmitRequest{Session: "s1", Question: q, Text: "t", By: by}.Valid() == nil
		if extOK != proto.ValidQuestionID(q) {
			t.Errorf("question id of %d bytes: ext takes it %v, proto.ValidQuestionID %v", len(q), extOK, proto.ValidQuestionID(q))
		}
	}
}

// TestExtLaunchBoundsAreTheServers holds the bounds of ext's TurnLaunch
// and ApprovedLaunch to the stores' own: a catalog agent's id, a ticket's
// key, a plan revision's id and an operation's id agree at the bound, one
// past it and on the shape.
func TestExtLaunchBoundsAreTheServers(t *testing.T) {
	turn := func(agent string) bool {
		return ext.TurnLaunch{Agent: agent, Folder: t.TempDir(), Line: "l", Label: "l", Request: "br-1"}.Valid() == nil
	}
	for _, id := range []string{strings.Repeat("a", ext.MaxAgentID), strings.Repeat("a", ext.MaxAgentID+1), "claude-2", "-", "A", "a_b", ""} {
		if turn(id) != catalog.ValidID(id) {
			t.Errorf("agent id %q: ext takes it %v, catalog.ValidID %v", id, turn(id), catalog.ValidID(id))
		}
	}
	by := ext.Principal{Subject: "u-1", Name: "Ada"}
	approved := func(edit func(*ext.ApprovedLaunch)) bool {
		r := ext.ApprovedLaunch{Ticket: "CND-1", Plan: strings.Repeat("a", dispatch.IDLen), OpID: "op", By: by}
		edit(&r)
		return r.Valid() == nil
	}
	if ext.PlanIDLen != dispatch.IDLen {
		t.Errorf("PlanIDLen: ext has %d, Dispatch %d", ext.PlanIDLen, dispatch.IDLen)
	}
	for _, key := range []string{"CND-1", "CNDXYZ-" + strings.Repeat("9", 18), "CND-" + strings.Repeat("9", 19), "CNDXYZW-1", "C-1", "cnd-1", "CND-0", "CND-01", "1ND-1", "CND"} {
		_, _, want := dispatch.ParseKey(key)
		if got := approved(func(r *ext.ApprovedLaunch) { r.Ticket = key }); got != want {
			t.Errorf("ticket key %q: ext takes it %v, dispatch.ParseKey %v", key, got, want)
		}
	}
	for _, id := range []string{strings.Repeat("f", dispatch.IDLen), strings.Repeat("f", dispatch.IDLen+1), strings.Repeat("F", dispatch.IDLen), "0123"} {
		if got, want := approved(func(r *ext.ApprovedLaunch) { r.Plan = id }), dispatch.ValidID(id); got != want {
			t.Errorf("plan id %q: ext takes it %v, dispatch.ValidID %v", id, got, want)
		}
	}
	for _, id := range []string{strings.Repeat("o", ext.MaxOpID), strings.Repeat("o", ext.MaxOpID+1), "a.b_c~d-e", "a/b", "a b", ""} {
		if got, want := approved(func(r *ext.ApprovedLaunch) { r.OpID = id }), dispatch.OpIDPattern.MatchString(id); got != want {
			t.Errorf("op id %q: ext takes it %v, dispatch.OpIDPattern %v", id, got, want)
		}
	}
}

// TestExtICEServerValidAsPion: a server ext.ICEServer.Valid passes is one
// Pion takes, since Pion (as a browser) refuses a whole configuration for one
// server it cannot use, the configured servers with it. Each row is checked
// against ext's answer and, when ext takes it, against NewPeerConnection.
func TestExtICEServerValidAsPion(t *testing.T) {
	for _, c := range []struct {
		name string
		s    ext.ICEServer
		ok   bool
	}{
		{"stun", ext.ICEServer{URLs: []string{"stun:stun.example.com"}}, true},
		{"stun with a port", ext.ICEServer{URLs: []string{"stun:stun.example.com:19302"}}, true},
		{"stuns", ext.ICEServer{URLs: []string{"stuns:stun.example.com:5349"}}, true},
		{"ipv6", ext.ICEServer{URLs: []string{"stun:[2001:db8::1]", "stun:[2001:db8::1]:3478"}}, true},
		{"turn", ext.ICEServer{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "c"}, true},
		{"turn over tcp", ext.ICEServer{URLs: []string{"turn:turn.example.com?transport=tcp"}, Username: "u", Credential: "c"}, true},
		{"turns over udp", ext.ICEServer{URLs: []string{"turns:turn.example.com:5349?transport=udp"}, Username: "u", Credential: "c"}, true},
		{"every scheme", ext.ICEServer{URLs: []string{"stun:a", "stuns:a", "turn:a", "turns:a"}, Username: "u", Credential: "c"}, true},
		{"a port that is no number", ext.ICEServer{URLs: []string{"stun:example:badport"}}, false},
		{"an empty port", ext.ICEServer{URLs: []string{"stun:example:"}}, false},
		{"a hierarchical url", ext.ICEServer{URLs: []string{"stun://example"}}, false},
		{"stun with a query", ext.ICEServer{URLs: []string{"stun:example?transport=udp"}}, false},
		{"turn of another transport", ext.ICEServer{URLs: []string{"turn:a?transport=sctp"}, Username: "u", Credential: "c"}, false},
		{"turn without credentials", ext.ICEServer{URLs: []string{"turn:a"}}, false},
		{"turns without a username", ext.ICEServer{URLs: []string{"turns:a"}, Credential: "c"}, false},
	} {
		err := c.s.Valid()
		if (err == nil) != c.ok {
			t.Errorf("%s: Valid() = %v, want ok %v", c.name, err, c.ok)
		}
		if err != nil {
			continue
		}
		cfg := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: c.s.URLs, Username: c.s.Username, Credential: c.s.Credential}}}
		pc, perr := webrtc.NewPeerConnection(cfg)
		if perr != nil {
			t.Errorf("%s: ext takes %+v and Pion refuses it: %v", c.name, c.s, perr)
			continue
		}
		if err := pc.Close(); err != nil {
			t.Errorf("%s: close: %v", c.name, err)
		}
	}
}
