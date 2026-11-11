package proto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A switchyard that carries the lane says so in registered; an older one
// sends no field, which a publisher reads as false, and the field is left
// out when false, so an older publisher reads the message it always did.
func TestRegisteredSaysDecisions(t *testing.T) {
	b, err := json.Marshal(Registered{T: HostRegistered, SessionID: "s1", Secret: "x", Links: []string{}, Decisions: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"decisions":true`) {
		t.Fatalf("registered with decisions: %s", b)
	}
	b, _ = json.Marshal(Registered{T: HostRegistered, SessionID: "s1", Secret: "x", Links: []string{}})
	if strings.Contains(string(b), "decisions") {
		t.Fatalf("registered without decisions names them: %s", b)
	}
	var older Registered
	if err := json.Unmarshal([]byte(`{"t":"registered","sessionId":"s1","secret":"x","shareBaseUrl":"https://y","resumed":false,"links":[]}`), &older); err != nil || older.Decisions {
		t.Fatalf("an older switchyard's registered: %+v %v", older, err)
	}
	if len(b) > MaxHostMessage {
		t.Fatalf("registered past a host message: %d", len(b))
	}
}

// A link_gone says ended only when the switchyard ended the link of itself;
// the field is left out otherwise, so an older host reads the message it
// always did, and an older switchyard's reads as not ended. At its bound it
// fits a host message.
func TestLinkGoneSaysEnded(t *testing.T) {
	b, _ := json.Marshal(LinkGone{T: HostLinkGone, LinkID: strings.Repeat("l", MaxLinkID), Ended: true})
	if !strings.Contains(string(b), `"ended":true`) || len(b) > MaxHostMessage {
		t.Fatalf("an ended link_gone: %s", b)
	}
	b, _ = json.Marshal(LinkGone{T: HostLinkGone, LinkID: "l1"})
	if strings.Contains(string(b), "ended") {
		t.Fatalf("a link_gone that is not ended says so: %s", b)
	}
	var older LinkGone
	if err := json.Unmarshal([]byte(`{"t":"link_gone","linkId":"l1"}`), &older); err != nil || older.Ended {
		t.Fatalf("an older switchyard's link_gone: %+v %v", older, err)
	}
}

// A desk's first message: the host's instance required (no_instance
// without it), the run within a run group's bounds, its local id the
// run's, a resume within a session's; at its bounds it fits a host
// message.
func TestRegisterDeskValid(t *testing.T) {
	ok := RegisterDesk{T: HostRegisterDesk, Proto: ProtoVersion, Host: HostInfo{Name: "box", Instance: "inst"}, LocalID: DeskLocalID("run-1"), Run: DeskRun{ID: "run-1", Name: "Run one"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a desk: %v", err)
	}
	if DeskLocalID("run-1") != "desk:run-1" {
		t.Fatalf("local id %q", DeskLocalID("run-1"))
	}
	bad := map[string]func(m *RegisterDesk){
		"type":         func(m *RegisterDesk) { m.T = HostRegister },
		"proto":        func(m *RegisterDesk) { m.Proto = ProtoVersion + 1 },
		"no instance":  func(m *RegisterDesk) { m.Host.Instance = "" },
		"instance":     func(m *RegisterDesk) { m.Host.Instance = strings.Repeat("i", MaxHostInstance+1) },
		"host name":    func(m *RegisterDesk) { m.Host.Name = strings.Repeat("h", 121) },
		"user":         func(m *RegisterDesk) { m.Host.User = strings.Repeat("u", MaxHostUser+1) },
		"no run":       func(m *RegisterDesk) { m.Run.ID, m.LocalID = "", DeskLocalID("") },
		"run id":       func(m *RegisterDesk) { m.Run.ID = strings.Repeat("r", MaxRunID+1); m.LocalID = DeskLocalID(m.Run.ID) },
		"no name":      func(m *RegisterDesk) { m.Run.Name = "" },
		"name":         func(m *RegisterDesk) { m.Run.Name = strings.Repeat("n", MaxRunName+1) },
		"local id":     func(m *RegisterDesk) { m.LocalID = "desk:run-2" },
		"session's id": func(m *RegisterDesk) { m.LocalID = "run-1" },
		"resume": func(m *RegisterDesk) {
			m.Resume = &HostResume{SessionID: strings.Repeat("s", MaxDeskID+1), Secret: "x"}
		},
		"no secret": func(m *RegisterDesk) { m.Resume = &HostResume{SessionID: "s"} },
	}
	for name, change := range bad {
		m := ok
		change(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	m := ok
	m.Host.Instance = ""
	if err := m.Validate(); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("no instance: %v", err)
	}
	full := RegisterDesk{T: HostRegisterDesk, Proto: ProtoVersion,
		Host:    HostInfo{Name: strings.Repeat("<", 120), Version: strings.Repeat("<", 64), User: strings.Repeat("<", MaxHostUser), Instance: strings.Repeat("<", MaxHostInstance)},
		LocalID: DeskLocalID(strings.Repeat("<", MaxRunID)), Run: DeskRun{ID: strings.Repeat("<", MaxRunID), Name: strings.Repeat("<", MaxRunName)},
		Resume: &HostResume{SessionID: strings.Repeat("<", MaxDeskID), Secret: strings.Repeat("<", 128)}}
	if err := full.Validate(); err != nil {
		t.Fatalf("at its bounds: %v", err)
	}
	b, _ := json.Marshal(full)
	if len(b) > MaxHostMessage {
		t.Fatalf("register_desk at its bounds is %d bytes", len(b))
	}
	var back RegisterDesk
	if err := json.Unmarshal(b, &back); err != nil || back.Run != full.Run || back.LocalID != full.LocalID {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

// A run group names its desk within a session id's bound, left out with
// none; a group with a desk may have no member yet (the run reached for
// its decisions before any member is), one without needs one.
func TestRunGroupDeskValid(t *testing.T) {
	g := RunGroup{ID: "run-1", Name: "Run", Members: []RunMember{{Name: "a", AgentID: "claude", Status: "pending"}}}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), "desk") {
		t.Fatalf("a group without a desk names one: %s", b)
	}
	g.Desk = strings.Repeat("d", MaxDeskID)
	if err := g.Validate(); err != nil {
		t.Fatalf("a desk at its bound: %v", err)
	}
	b, _ = json.Marshal(g)
	if !strings.Contains(string(b), `"desk":"`+g.Desk+`"`) {
		t.Fatalf("the desk is not sent: %s", b)
	}
	g.Desk += "d"
	if err := g.Validate(); err == nil {
		t.Fatal("a desk past its bound taken")
	}
	empty := RunGroup{ID: "run-1", Name: "Run", Desk: "d1"}
	if err := empty.Validate(); err != nil {
		t.Fatalf("a desk's group with no member: %v", err)
	}
	empty.Desk = ""
	if err := empty.Validate(); err == nil {
		t.Fatal("a group with neither member nor desk taken")
	}
}

// The relay carries the lane's two frames as it carries terminal bytes:
// an envelope with either decodes to the frame whole.
func TestRelayCarriesDecisionFrames(t *testing.T) {
	viewer := strings.Repeat("v", ViewerIDLen)
	for _, typ := range []byte{TypeDecision, TypeDecide} {
		payload := []byte(`{"reqId":"r1"}`)
		env, err := EncodeRelay(viewer, Encode(typ, payload))
		if err != nil {
			t.Fatal(err)
		}
		f, err := Decode(env)
		if err != nil || f.Type != TypeRelay {
			t.Fatalf("0x%02x: the envelope: %v", typ, err)
		}
		id, inner, err := DecodeRelay(f.Payload)
		if err != nil || id != viewer || inner.Type != typ || string(inner.Payload) != string(payload) {
			t.Fatalf("0x%02x: %q %+v %v", typ, id, inner, err)
		}
	}
	// A decision at its bound fits an envelope.
	env, err := EncodeRelay(viewer, Encode(TypeDecision, make([]byte, MaxDecision)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(env); err != nil {
		t.Fatalf("a decision at its bound relayed: %v", err)
	}
}
