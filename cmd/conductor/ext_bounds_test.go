package main

import (
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
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
