package signal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// registerHost registers a session whose host says h of itself, with the
// local id that goes with an instance.
func registerHost(hub *Hub, h proto.HostInfo) (*HostedSession, *HostConn, error) {
	conn := NewHostConn()
	s := proto.HostSession{Command: []string{"bash"}, Cols: 80, Rows: 24}
	if h.Instance != "" {
		s.LocalID = "1"
	}
	if h.Name == "" {
		h.Name = "laptop"
	}
	hs, _, err := hub.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion, Host: h, Session: s}, conn)
	return hs, conn, err
}

// isKey says k is 32 lowercase hex digits.
func isKey(k string) bool { return proto.ValidHostMachine(k) }

// The host's version is kept as it registered, in its form; one out of its
// form is not kept, and the register still stands (an older host's version
// may be of any form).
func TestRegisterKeepsHostVersion(t *testing.T) {
	hub := NewHub(session.NewRegistry(8), nil)
	hs, _, err := registerHost(hub, proto.HostInfo{Version: "v1.2.3-rc.4-12-gabcdef0-dirty", Platform: "darwin/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if info := hs.Info(); info.HostVersion != "v1.2.3-rc.4-12-gabcdef0-dirty" || info.HostPlatform != "darwin/arm64" {
		t.Fatalf("kept version %q platform %q", info.HostVersion, info.HostPlatform)
	}
	for _, v := range []string{strings.Repeat("v", proto.MaxHostVersion+1), "v1 <b>", "-1", ""} {
		hs, _, err := registerHost(hub, proto.HostInfo{Version: v})
		if err != nil {
			t.Fatalf("version %q refused the register: %v", v, err)
		}
		if got := hs.Info().HostVersion; got != "" {
			t.Errorf("version %q kept as %q", v, got)
		}
	}
	// A host back on its session says its version again.
	inst := "inst-v"
	first, conn, err := registerHost(hub, proto.HostInfo{Instance: inst, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	first.HostDisconnected(conn)
	again, _, err := registerHost(hub, proto.HostInfo{Instance: inst, Version: "v2"})
	if err != nil || again != first || again.Info().HostVersion != "v2" {
		t.Fatalf("back with v2: %v, same %v, %q", err, again == first, again.Info().HostVersion)
	}
}

// A register whose machine or platform is out of its form is refused
// whole, nothing registered; one that says neither registers as before.
func TestRegisterRefusesMachineOutOfForm(t *testing.T) {
	reg := session.NewRegistry(8)
	hub := NewHub(reg, nil)
	key := strings.Repeat("ab", 16)
	for name, h := range map[string]proto.HostInfo{
		"a machine one short":       {Machine: key[:31]},
		"a machine one past":        {Machine: key + "a"},
		"a machine in upper case":   {Machine: strings.ToUpper(key)},
		"a platform past its bound": {Platform: strings.Repeat("a", 13) + "/" + strings.Repeat("b", 15) + "+wsl"},
		"a platform out of form":    {Platform: "Linux/amd64"},
	} {
		if hs, _, err := registerHost(hub, h); !errors.Is(err, ErrBadRegister) || hs != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(reg.List()); n != 0 {
		t.Fatalf("%d sessions registered", n)
	}
	if _, _, err := registerHost(hub, proto.HostInfo{Version: "v0.9"}); err != nil {
		t.Fatalf("an older host: %v", err)
	}
	// A host back on its session with a machine out of its form is refused
	// too, and the session keeps what it had.
	first, conn, err := registerHost(hub, proto.HostInfo{Instance: "inst-r", Machine: key})
	if err != nil {
		t.Fatal(err)
	}
	first.HostDisconnected(conn)
	if _, _, err := registerHost(hub, proto.HostInfo{Instance: "inst-r", Machine: key + "a"}); !errors.Is(err, ErrBadRegister) {
		t.Fatalf("back with a bad machine: %v", err)
	}
	if info := first.Info(); info.HostKey != machine.Group(key) || info.HostLink != session.HostLinkDown {
		t.Fatalf("after a refused return: %+v", info)
	}
}

// What the host sent is never in the session's Info, on the wire or
// anywhere: only a hash of it, 32 lowercase hex digits.
func TestMachineKeyHashesTheKey(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	sent := machine.Key(strings.Repeat("cd", 16), bytes.Repeat([]byte{7}, machine.ServerKeyLen))
	hs, _, err := registerHost(hub, proto.HostInfo{Machine: sent, Instance: "inst-m"})
	if err != nil {
		t.Fatal(err)
	}
	info := hs.Info()
	if !isKey(info.HostKey) || info.HostKey == sent || info.HostKeyFrom != session.HostKeyMachine {
		t.Fatalf("key %q from %q (sent %q)", info.HostKey, info.HostKeyFrom, sent)
	}
	b, _ := json.Marshal(info)
	if strings.Contains(string(b), sent) {
		t.Fatalf("the key the host sent is on the wire: %s", b)
	}
}

// A register's hostKey is machine.Group of the key it sent, byte for byte,
// the function the server's own row is keyed by; a process's key and a
// session's never equal the machine.Group of any key in the test.
func TestGroupIsTheHubsKey(t *testing.T) {
	hub := NewHub(session.NewRegistry(8), nil)
	serverKey := bytes.Repeat([]byte{9}, machine.ServerKeyLen)
	id := strings.Repeat("ef", 16)
	sent := machine.Key(id, serverKey)
	a, _, err := registerHost(hub, proto.HostInfo{Machine: sent, Instance: "inst-a"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := registerHost(hub, proto.HostInfo{Machine: sent})
	if err != nil {
		t.Fatal(err)
	}
	own := machine.Group(machine.Key(id, serverKey))
	if a.Info().HostKey != own || b.Info().HostKey != own {
		t.Fatalf("two hosts of one machine: %q and %q, the server's own row %q", a.Info().HostKey, b.Info().HostKey, own)
	}
	proc, _, err := registerHost(hub, proto.HostInfo{Instance: "inst-p"})
	if err != nil {
		t.Fatal(err)
	}
	lone, _, err := registerHost(hub, proto.HostInfo{})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{own: true}
	for _, k := range []string{sent, ownerOf("inst-p"), proc.Info().ID, lone.Info().ID, proc.Owner()} {
		groups[machine.Group(k)] = true
	}
	for _, hs := range []*HostedSession{proc, lone} {
		if k := hs.Info().HostKey; !isKey(k) || groups[k] {
			t.Errorf("%s's key %q is a machine's", hs.Info().HostKeyFrom, k)
		}
	}
}

// A host that sends no machine but an instance is one process: its
// sessions share a key made from its owner, under a prefix of its own,
// never the owner itself.
func TestOlderHostKeyedByProcess(t *testing.T) {
	hub := NewHub(session.NewRegistry(8), nil)
	conn := NewHostConn()
	one, _, err := registerAs(hub, "inst-o", "1", conn)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := registerAs(hub, "inst-o", "2", NewHostConn())
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := registerAs(hub, "inst-x", "1", NewHostConn())
	if err != nil {
		t.Fatal(err)
	}
	i1, i2 := one.Info(), two.Info()
	if i1.HostKeyFrom != session.HostKeyProcess || !isKey(i1.HostKey) || i1.HostKey != i2.HostKey {
		t.Fatalf("one process's sessions: %q %q / %q %q", i1.HostKey, i1.HostKeyFrom, i2.HostKey, i2.HostKeyFrom)
	}
	if i1.HostKey == one.Owner() || i1.HostKey == other.Info().HostKey {
		t.Fatalf("the process key %q is its owner or another process's", i1.HostKey)
	}
}

// The oldest hosts send neither machine nor instance: each session its own
// key, made from its id.
func TestOldestHostsKeyedBySession(t *testing.T) {
	hub := NewHub(session.NewRegistry(8), nil)
	seen := map[string]bool{}
	for range 4 {
		hs, _ := register(t, hub)
		info := hs.Info()
		if info.HostKeyFrom != session.HostKeySession || !isKey(info.HostKey) || seen[info.HostKey] {
			t.Fatalf("an oldest host's session: %q from %q (seen %v)", info.HostKey, info.HostKeyFrom, seen)
		}
		seen[info.HostKey] = true
	}
}

// hostLink is the host's connection as it is, apart from the status: a
// running session's up; its link dropped, down with hostAwaySince and the
// status host_disconnected; reattached, up and no hostAwaySince, though the
// time it left is still kept; an exited session whose host is connected,
// up and exited; that host gone, down with hostAwaySince, still exited.
func TestHostLinkApartFromStatus(t *testing.T) {
	hub := NewHub(session.NewRegistry(4), nil)
	hs, conn, err := registerHost(hub, proto.HostInfo{Instance: "inst-l"})
	if err != nil {
		t.Fatal(err)
	}
	look := func(when string, link string, away bool, status session.Status) {
		t.Helper()
		info := hs.Info()
		if info.HostLink != link || (info.HostAwaySince != nil) != away || info.Status != status {
			t.Fatalf("%s: link %q away %v status %q", when, info.HostLink, info.HostAwaySince, info.Status)
		}
	}
	look("running", session.HostLinkUp, false, session.StatusRunning)
	hs.HostDisconnected(conn)
	look("dropped", session.HostLinkDown, true, session.StatusHostDisconnected)
	if at := hs.Info().HostAwaySince; at.Location() != time.UTC {
		t.Fatalf("hostAwaySince in %v", at.Location())
	}
	again, conn2, err := registerHost(hub, proto.HostInfo{Instance: "inst-l"})
	if err != nil || again != hs {
		t.Fatalf("reattach: %v", err)
	}
	look("reattached", session.HostLinkUp, false, session.StatusRunning)
	hs.mu.Lock()
	kept := !hs.disconnectedAt.IsZero()
	hs.mu.Unlock()
	if !kept {
		t.Fatal("the time the host left was not kept: the test proves nothing")
	}
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	look("exited, host connected", session.HostLinkUp, false, session.StatusExited)
	hs.HostDisconnected(conn2)
	look("exited, host gone", session.HostLinkDown, true, session.StatusExited)
}
