package signal

import (
	"errors"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

func deskOf(instance, runID string) proto.RegisterDesk {
	return proto.RegisterDesk{T: proto.HostRegisterDesk, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "box", Instance: instance},
		LocalID: proto.DeskLocalID(runID), Run: proto.DeskRun{ID: runID, Name: "Run " + runID}}
}

// A desk is its publisher's: refused without an instance; two publishers
// with one run id get two desks of two owners; one takes its own desk
// back with its secret, or with its instance after this hub forgot the
// connection (a restart), and neither another publisher nor a session
// registration takes it; it is never in the registry, yet holds a place
// among the sessions while it lives, and gives it back as it ends.
func TestADeskIsItsPublishers(t *testing.T) {
	reg := session.NewRegistry(3)
	hub := NewHub(reg, nil)
	if _, _, err := hub.RegisterDesk(deskOf("", "run-1"), NewHostConn()); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("no instance: %v", err)
	}
	connA := NewHostConn()
	a, resumed, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), connA)
	if err != nil || resumed {
		t.Fatalf("a's desk: %v %v", err, resumed)
	}
	b, _, err := hub.RegisterDesk(deskOf("inst-b", "run-1"), NewHostConn())
	if err != nil {
		t.Fatalf("b's desk: %v", err)
	}
	if a.Info().ID == b.Info().ID || a.Owner() == b.Owner() || a.Owner() == "" {
		t.Fatalf("two publishers' desks of one run: %s/%s and %s/%s", a.Info().ID, a.Owner(), b.Info().ID, b.Owner())
	}
	if a.Info().ID != HostedID("inst-a", "desk:run-1") || a.DeskRun() != "run-1" {
		t.Fatalf("a's desk id %s run %s", a.Info().ID, a.DeskRun())
	}
	if got, ok := hub.Desk(a.Info().ID); !ok || got != a {
		t.Fatal("the hub does not find a's desk")
	}
	if _, ok := reg.Get(a.Info().ID); ok || len(reg.List()) != 0 {
		t.Fatal("a desk is listed as a session")
	}
	// Two desks hold two of the three places: one session fits, a second not.
	if _, _, err := registerAs(hub, "inst-c", "s1", NewHostConn()); err != nil {
		t.Fatalf("a session beside two desks: %v", err)
	}
	if _, _, err := registerAs(hub, "inst-c", "s2", NewHostConn()); !errors.Is(err, session.ErrTooManySessions) {
		t.Fatalf("past the limit with two desks: %v", err)
	}
	if _, _, err := hub.RegisterDesk(deskOf("inst-c", "run-9"), NewHostConn()); !errors.Is(err, session.ErrTooManySessions) {
		t.Fatalf("a desk past the limit: %v", err)
	}
	// Held live: another registration of it is refused, by anyone.
	if _, _, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("a live desk registered again: %v", err)
	}
	if _, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: proto.HostInfo{Name: "box", Instance: "inst-a"},
		Session: proto.HostSession{Command: []string{"x"}, LocalID: "desk:run-1"}}, NewHostConn()); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("a session under a desk's id: %v", err)
	}
	// Its connection drops: resumed with its secret alone.
	a.HostDisconnected(connA)
	wrong := deskOf("inst-a", "run-1")
	wrong.Resume = &proto.HostResume{SessionID: a.Info().ID, Secret: "nope"}
	if _, _, err := hub.RegisterDesk(wrong, NewHostConn()); !errors.Is(err, ErrBadResume) {
		t.Fatalf("a resume with the wrong secret: %v", err)
	}
	right := deskOf("inst-a", "run-1")
	right.Resume = &proto.HostResume{SessionID: a.Info().ID, Secret: a.Secret()}
	connA2 := NewHostConn()
	if got, resumed, err := hub.RegisterDesk(right, connA2); err != nil || !resumed || got != a {
		t.Fatalf("a resume: %v %v", err, resumed)
	}
	// Away again, and taken back under its instance (as after a restart,
	// when no secret is known), never by another owner.
	a.HostDisconnected(connA2)
	if _, _, err := hub.RegisterDesk(deskOf("inst-b", "run-1"), NewHostConn()); err == nil {
		// b's own desk is live: its registration is refused too.
		t.Fatal("b registered its live desk again")
	}
	if got, resumed, err := hub.RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn()); err != nil || !resumed || got != a {
		t.Fatalf("taken back under its instance: %v %v", err, resumed)
	}
	// A fresh hub (the switchyard restarted) gives the same id.
	again, _, err := NewHub(session.NewRegistry(3), nil).RegisterDesk(deskOf("inst-a", "run-1"), NewHostConn())
	if err != nil || again.Info().ID != a.Info().ID {
		t.Fatalf("after a restart: %v %s", err, again.Info().ID)
	}
	// Its host ends it: forgotten at once, its place given back.
	b.EndDesk()
	if _, ok := hub.Desk(b.Info().ID); ok {
		t.Fatal("an ended desk is still found")
	}
	if _, _, err := registerAs(hub, "inst-c", "s2", NewHostConn()); err != nil {
		t.Fatalf("the ended desk's place: %v", err)
	}
}

// A desk whose host has been away past the grace is forgotten, its place
// given back, as a session is.
func TestADeskExpiresWithItsHost(t *testing.T) {
	reg := session.NewRegistry(1)
	hub := NewHub(reg, nil)
	conn := NewHostConn()
	d, _, err := hub.RegisterDesk(deskOf("inst", "run-1"), conn)
	if err != nil {
		t.Fatal(err)
	}
	hub.Expire(time.Now())
	if _, ok := hub.Desk(d.Info().ID); !ok {
		t.Fatal("a desk with its host was expired")
	}
	d.HostDisconnected(conn)
	hub.Expire(time.Now().Add(DisconnectGrace))
	if _, ok := hub.Desk(d.Info().ID); ok {
		t.Fatal("a desk away past the grace is still held")
	}
	if _, err := reg.Reserve(); err != nil {
		t.Fatalf("its place: %v", err)
	}
}

// A view-role viewer's lane is dropped at the relay, its reads and its
// decides alike; a control viewer's goes to the host.
func TestRelayDropsTheLaneFromAViewer(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn := register(t, hub)
	view, err := hs.AddViewer("0123456789abcdef", session.RoleView, "l1", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	if err := hs.StartRelay(view); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostRelayStart)
	reads := []any{
		proto.DecisionsList{T: proto.CtlDecisionsList, ReqID: "r1"},
		proto.DecisionRead{T: proto.CtlDecisionRead, ReqID: "r1", Ref: "task:run-1/build"},
		proto.DecisionTrack{T: proto.CtlDecisionTrack, ReqID: "r1", Ref: "task:run-1/build", Receipt: "c1", Attempt: "o1"},
		proto.DecisionRecord{T: proto.CtlDecisionRecord, ReqID: "r1", Ref: "decision:dr-1", Part: proto.RecordHead},
		proto.DecisionEvidence{T: proto.CtlDecisionEvidence, ReqID: "r1", Ref: "task:run-1/build", Evidence: "run:run-1"},
	}
	for _, m := range reads {
		f := proto.MustControl(m)
		if err := hs.RelayToHost(view, proto.Frame{Type: proto.TypeControl, Payload: f[1:]}); !errors.Is(err, session.ErrReadOnly) {
			t.Fatalf("a view viewer's %T: %v", m, err)
		}
	}
	if err := hs.RelayToHost(view, proto.Frame{Type: proto.TypeDecide, Payload: []byte(`{"reqId":"r1"}`)}); !errors.Is(err, session.ErrReadOnly) {
		t.Fatalf("a view viewer's decide: %v", err)
	}
	if o, ok := takeWithin(conn, 50*time.Millisecond); ok {
		t.Fatalf("the host was sent %q", o.Binary)
	}
	ctl, err := hs.AddViewer("fedcba9876543210", session.RoleControl, "l2", "")
	if err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostViewerJoin)
	if err := hs.StartRelay(ctl); err != nil {
		t.Fatal(err)
	}
	drainText(t, conn, proto.HostRelayStart)
	list := proto.MustControl(reads[0])
	for _, f := range []proto.Frame{{Type: proto.TypeControl, Payload: list[1:]}, {Type: proto.TypeDecide, Payload: []byte(`{"reqId":"r2"}`)}} {
		if err := hs.RelayToHost(ctl, f); err != nil {
			t.Fatalf("a control viewer's 0x%02x: %v", f.Type, err)
		}
		o, ok := takeWithin(conn, time.Second)
		if !ok || o.Binary == nil {
			t.Fatalf("a control viewer's 0x%02x did not reach the host", f.Type)
		}
		env, err := proto.Decode(o.Binary)
		if err != nil {
			t.Fatal(err)
		}
		if _, inner, err := proto.DecodeRelay(env.Payload); err != nil || inner.Type != f.Type {
			t.Fatalf("relayed: %+v %v", inner, err)
		}
	}
}
