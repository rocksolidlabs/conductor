package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/hostagent"
	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The host rows of GET /api/hosts (docs/plans/hosts.md, PR-2): hosted
// sessions registered with the hub as a host's registration makes them.

// hostOn registers a hosted session of a host named name whose register
// carries the machine key sent ("" for none), on a connection of its own.
func hostOn(t *testing.T, e *testEnv, name, sent string) (*signal.HostedSession, *signal.HostConn) {
	t.Helper()
	conn := signal.NewHostConn()
	hs, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host:    proto.HostInfo{Name: name, User: "ada", Version: "v0.7.0", Platform: "darwin/arm64", Machine: sent},
		Session: proto.HostSession{AgentID: "sh", Command: []string{"sh"}, Cwd: "/w", Cols: 80, Rows: 24}}, conn)
	if err != nil {
		t.Fatal(err)
	}
	return hs, conn
}

// machineKey is a random key a host sends: 32 lowercase hex digits.
func machineKey(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// hostRowOf is the reply's row of key.
func hostRowOf(r HostsReply, key string) (HostRow, bool) {
	for _, h := range r.Hosts {
		if h.Key == key {
			return h, true
		}
	}
	return HostRow{}, false
}

// Three registrations with one machine key are one row; two machines with
// one host name are two.
func TestHostsGroupsAMachine(t *testing.T) {
	e := newTestEnv(t, nil)
	one, two := machineKey(t), machineKey(t)
	var ids []string
	for range 3 {
		hs, _ := hostOn(t, e, "studio", one)
		ids = append(ids, hs.Info().ID)
	}
	other, _ := hostOn(t, e, "studio", two)
	r := e.hostsOf(t)
	if len(r.Hosts) != 2 {
		t.Fatalf("the rows: %+v", r.Hosts)
	}
	row, ok := hostRowOf(r, machine.Group(one))
	if !ok || len(row.SessionIDs) != 3 || row.KeyFrom != session.HostKeyMachine || row.Name != "studio" || row.User != "ada" ||
		row.Platform != "darwin/arm64" || row.Version != "v0.7.0" || row.Availability != HostConnected || row.Sessions.Running != 3 {
		t.Fatalf("the machine of three: %+v", row)
	}
	for _, id := range ids {
		if !strings.Contains(strings.Join(row.SessionIDs, " "), id) {
			t.Fatalf("%s not on its row: %v", id, row.SessionIDs)
		}
	}
	if row, ok := hostRowOf(r, machine.Group(two)); !ok || len(row.SessionIDs) != 1 || row.SessionIDs[0] != other.Info().ID {
		t.Fatalf("the other machine: %+v", row)
	}
}

// Connected; away, since its last link dropped; Last seen after the hub's
// Expire; gone after 24 hours through the table's clock.
func TestHostsAvailability(t *testing.T) {
	e := newTestEnv(t, nil)
	var mu sync.Mutex
	now := time.Now().UTC()
	e.srv.seen.setNow(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	key := machineKey(t)
	hs, conn := hostOn(t, e, "studio", key)
	row := func() (HostRow, bool) { return hostRowOf(e.hostsOf(t), machine.Group(key)) }
	if r, ok := row(); !ok || r.Availability != HostConnected || !r.Since.IsZero() {
		t.Fatalf("connected: %+v", r)
	}
	hs.HostDisconnected(conn)
	r, ok := row()
	away := hs.Info().HostAwaySince
	if !ok || r.Availability != HostAway || away == nil || !r.Since.Equal(away.UTC()) {
		t.Fatalf("away: %+v (away since %v)", r, away)
	}
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Minute))
	r, ok = row()
	if !ok || r.Availability != HostSeen || !r.Since.Equal(now) || len(r.SessionIDs) != 0 || r.Name != "studio" {
		t.Fatalf("seen: %+v", r)
	}
	mu.Lock()
	now = now.Add(hostsSeenFor)
	mu.Unlock()
	if r, ok := row(); ok {
		t.Fatalf("after 24 hours: %+v", r)
	}
}

// A machine whose only session exited with its host still connected is
// connected; that host's link dropped, away with its since, the session
// still exited; after Expire, seen.
func TestHostsEndedSessionKeepsItsLink(t *testing.T) {
	e := newTestEnv(t, nil)
	key := machineKey(t)
	hs, conn := hostOn(t, e, "studio", key)
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	before := e.hostsOf(t)
	if r, ok := hostRowOf(before, machine.Group(key)); !ok || r.Availability != HostConnected || r.Sessions.Running != 0 {
		t.Fatalf("exited, host connected: %+v", r)
	}
	hs.HostDisconnected(conn)
	after := e.hostsOf(t)
	r, ok := hostRowOf(after, machine.Group(key))
	if !ok || r.Availability != HostAway || r.Since.IsZero() || hs.Info().Status != session.StatusExited {
		t.Fatalf("exited, host gone: %+v %s", r, hs.Info().Status)
	}
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Minute))
	if r, ok := hostRowOf(e.hostsOf(t), machine.Group(key)); !ok || r.Availability != HostSeen {
		t.Fatalf("after Expire: %+v", r)
	}
}

// I10: Expire records the machine; a machine with a second session left
// records nothing.
func TestHostsSeenAfterExpire(t *testing.T) {
	e := newTestEnv(t, nil)
	key := machineKey(t)
	first, conn := hostOn(t, e, "studio", key)
	second, _ := hostOn(t, e, "studio", key)
	first.HostDisconnected(conn)
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Minute))
	r, ok := hostRowOf(e.hostsOf(t), machine.Group(key))
	if !ok || r.Availability != HostConnected || len(r.SessionIDs) != 1 || r.SessionIDs[0] != second.Info().ID {
		t.Fatalf("a second session left: %+v", r)
	}
	e.srv.seen.mu.Lock()
	_, recorded := e.srv.seen.entries[machine.Group(key)]
	e.srv.seen.mu.Unlock()
	if recorded {
		t.Fatal("recorded while a session of the machine is listed")
	}
}

// I10: a machine's last hosted session ended and deleted, and another's
// swept by the retention, while the hub still keeps each: Last seen at
// once, no row lists the session, and the hub's later Expire changes
// nothing.
func TestHostsSeenAfterDelete(t *testing.T) {
	e := newTestEnv(t, nil)
	key := machineKey(t)
	hs, _ := hostOn(t, e, "studio", key)
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	id := hs.Info().ID
	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode/100 != 2 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, ok := e.srv.hosts.Get(id); !ok {
		t.Fatal("the hub no longer keeps the session: the test needs it kept")
	}
	seen := func(what string) HostRow {
		t.Helper()
		r := e.hostsOf(t)
		row, ok := hostRowOf(r, machine.Group(key))
		if !ok || row.Availability != HostSeen || len(row.SessionIDs) != 0 {
			t.Fatalf("%s: %+v", what, r.Hosts)
		}
		return row
	}
	before := seen("deleted")
	e.srv.hosts.Expire(time.Now())
	if after := seen("after Expire"); !after.Since.Equal(before.Since) {
		t.Fatalf("Expire changed the row: %+v, was %+v", after, before)
	}
}

func TestHostsSeenAfterRetention(t *testing.T) {
	e := newTestEnv(t, nil)
	key := machineKey(t)
	hs, _ := hostOn(t, e, "studio", key)
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	id := hs.Info().ID
	if got := e.srv.registry.GC(0, time.Now().Add(time.Hour)); len(got) != 1 || got[0] != id {
		t.Fatalf("the sweep removed %v", got)
	}
	if _, ok := e.srv.hosts.Get(id); !ok {
		t.Fatal("the hub no longer keeps the session: the test needs it kept")
	}
	r, ok := hostRowOf(e.hostsOf(t), machine.Group(key))
	if !ok || r.Availability != HostSeen {
		t.Fatalf("swept: %+v", r)
	}
	e.srv.hosts.Expire(time.Now())
	if r, ok := hostRowOf(e.hostsOf(t), machine.Group(key)); !ok || r.Availability != HostSeen {
		t.Fatalf("after Expire: %+v", r)
	}
}

// A register and the last removal of one key, in both orders, the removal
// held at a gate in its hook: the second decides.
func TestHostsSeenRegisterAndRemoveRace(t *testing.T) {
	for _, registerFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("register first %v", registerFirst), func(t *testing.T) {
			e := newTestEnv(t, nil)
			key := machineKey(t)
			hs, _ := hostOn(t, e, "studio", key)
			id := hs.Info().ID
			at, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			e.srv.seen.mu.Lock()
			e.srv.seen.removing = func(string) { once.Do(func() { close(at); <-gate }) }
			e.srv.seen.mu.Unlock()
			if registerFirst {
				// The new session registers while the removal waits at its
				// gate, before it reads the registry: the registry lists it.
				done := make(chan struct{})
				go func() { e.srv.registry.Remove(id); close(done) }()
				<-at
				// The register's change waits for the table's lock, which
				// the removal holds: it comes second and decides.
				registered := make(chan struct{})
				go func() { hostOn(t, e, "studio", key); close(registered) }()
				close(gate)
				<-done
				<-registered
			} else {
				done := make(chan struct{})
				go func() { e.srv.registry.Remove(id); close(done) }()
				<-at
				close(gate)
				<-done
				hostOn(t, e, "studio", key)
			}
			r, ok := hostRowOf(e.hostsOf(t), machine.Group(key))
			if !ok || r.Availability != HostConnected || len(r.SessionIDs) != 1 {
				t.Fatalf("the reply: %+v", r)
			}
			e.srv.seen.mu.Lock()
			_, recorded := e.srv.seen.entries[machine.Group(key)]
			e.srv.seen.mu.Unlock()
			if recorded {
				t.Fatal("the machine is kept as seen while its new session is listed")
			}
		})
	}
}

// 65 machines seen: the oldest dropped.
func TestHostsSeenBounded(t *testing.T) {
	h := newHostsSeen()
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	h.setNow(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	keys := make([]string, hostsSeenMax+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("%032x", i+1)
		mu.Lock()
		now = now.Add(time.Second)
		mu.Unlock()
		h.removed(session.Info{ID: fmt.Sprint(i), Kind: session.KindHosted, HostName: "m", HostKey: keys[i]}, "", func() []session.Info { return nil })
	}
	rows := h.list(nil)
	if len(rows) != hostsSeenMax || rows[len(rows)-1].Key != keys[1] || rows[0].Key != keys[hostsSeenMax] {
		t.Fatalf("%d rows, newest %s, oldest %s", len(rows), rows[0].Key, rows[len(rows)-1].Key)
	}
}

// A hosted session's question stays a question: counted on its machine's
// row with its link, and a session hosted from this machine's on this
// server's hosted sessions.
func TestHostsHostedQuestionStaysAQuestion(t *testing.T) {
	e := newTestEnv(t, nil)
	key := machineKey(t)
	hs, _ := hostOn(t, e, "studio", key)
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, dbChoices, "q-1", true, "", "")
	r, ok := hostRowOf(e.hostsOf(t), machine.Group(key))
	if !ok || r.Sessions.Waiting != 1 || r.Sessions.WaitingRef != questionRef(hs.Info().ID, "q-1") {
		t.Fatalf("a host's question: %+v", r)
	}
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, dbChoices, "q-1", true, session.AnsweredTerminal, "")
	if r, _ := hostRowOf(e.hostsOf(t), machine.Group(key)); r.Sessions.Waiting != 0 || r.Sessions.WaitingRef != "" {
		t.Fatalf("answered in its terminal: %+v", r)
	}
}

// I6: a host registering with a key: the bytes of GET /api/hosts, of GET
// /api/sessions, of the session event and the server's log hold neither
// the machine's id, the key the host sent, nor the server's key.
func TestHostsReplyHoldsNoMachineID(t *testing.T) {
	var logs bytes.Buffer
	var logMu sync.Mutex
	e := newTestEnvLogging(t, nil, slog.New(slog.NewTextHandler(lockedWriter{b: &logs, mu: &logMu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	id := machineKey(t)
	var serverKey [machine.ServerKeyLen]byte
	if _, err := rand.Read(serverKey[:]); err != nil {
		t.Fatal(err)
	}
	sent := machine.Key(id, serverKey[:])
	events := e.sse(t)
	hs, _ := hostOn(t, e, "studio", sent)
	hs.HostAttention(session.AttentionNeedsInput, "Allow?", session.SourceAPI, session.KindPermission, dbChoices, "q-1", true, "", "")
	var event string
	waitUntil(t, "the session's event", func() bool {
		select {
		case ev := <-events:
			if strings.HasPrefix(ev, "session ") && strings.Contains(ev, hs.Info().ID) {
				event = ev
				return true
			}
		default:
		}
		return false
	})
	_, hosts := e.get(t, "/api/hosts")
	_, sessions := e.get(t, "/api/sessions")
	if !strings.Contains(string(hosts), machine.Group(sent)) {
		t.Fatalf("the reply does not group the machine: %s", hosts)
	}
	logMu.Lock()
	logged := logs.String()
	logMu.Unlock()
	own := e.srv.serverKeyHeader()
	if own == "" {
		t.Fatal("the server has no key of this start")
	}
	for what, b := range map[string]string{"GET /api/hosts": string(hosts), "GET /api/sessions": string(sessions), "the session event": event, "the log": logged} {
		if strings.Contains(b, id) || strings.Contains(b, sent) || strings.Contains(b, own) {
			t.Fatalf("%s holds the machine's id, the key the host sent or the server's key", what)
		}
	}
}

// Every list past its bound and every field at its longest and full of
// characters JSON escapes: the reply within 256 KiB, the first half within
// its budget, more counting exactly what was left out.
func TestHostsReplyBounded(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.MaxSessions = 400 })
	const machines = 300
	long := strings.Repeat("<", hostsNameMax)
	for i := range machines {
		hostOn(t, e, long[:len(long)-4]+fmt.Sprintf("%04d", i), machineKey(t))
	}
	busy := machineKey(t)
	for range hostsMaxSessionIDs + 6 {
		hostOn(t, e, "busy", busy)
	}
	status, body := e.get(t, "/api/hosts")
	if status != http.StatusOK || len(body) > hostsReplyMax {
		t.Fatalf("%d, %d bytes", status, len(body))
	}
	r := decodeHosts(t, body)
	first := hostsEncodedLen(r.This) + hostsEncodedLen(r.Switchyard) + hostsEncodedLen(r.Ledger) + hostsEncodedLen(r.Hosts) + hostsEncodedLen(r.Runs)
	if first > hostsFirstBudget+16 {
		t.Fatalf("the first half is %d bytes", first)
	}
	if len(r.Hosts)+r.More.Hosts != machines+1 || r.More.Hosts == 0 || len(r.Hosts) > hostsMaxHostRows {
		t.Fatalf("%d rows and %d more", len(r.Hosts), r.More.Hosts)
	}
	for _, h := range r.Hosts {
		if len(h.Name) > hostsNameMax {
			t.Fatalf("a name of %d bytes", len(h.Name))
		}
		if h.Name == "busy" && (len(h.SessionIDs) != hostsMaxSessionIDs || h.MoreIDs != 6) {
			t.Fatalf("the busy machine: %d ids, %d more", len(h.SessionIDs), h.MoreIDs)
		}
	}
}

// runMachineHost runs `conductor host` with the machine directory dir
// against e until the test ends, and answers its session's id.
func runMachineHost(t *testing.T, e *testEnv, dir string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	registered := make(chan string, 1)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = hostagent.Run(ctx, hostagent.Options{
			ServerURL: e.http.URL, Token: "test-host-token", Name: "on this machine", HostName: "this-machine", RelayOnly: true,
			Argv: []string{"/bin/cat"}, Dir: e.root, MachineDir: dir, Log: slog.New(slog.DiscardHandler),
			Registered: func(id, _ string) { registered <- id },
		})
	}()
	t.Cleanup(func() { cancel(); <-ran })
	select {
	case id := <-registered:
		return id
	case <-time.After(testWait):
		t.Fatal("the host never registered")
	}
	return ""
}

// I7, through a real registration: a host given this server's machine
// directory reads the upgrade's key and registers with the key it makes;
// its session is this server's hosted one, this.key is its hostKey, and it
// has no row of its own. At a second server, another key: there it is a
// row of its own.
func TestHostsHostOnThisMachine(t *testing.T) {
	dir := t.TempDir()
	id, err := machine.ID(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, nil)
	e.srv.SetOwnMachine(id)
	sid := runMachineHost(t, e, dir)
	d, ok := e.srv.registry.Get(sid)
	if !ok {
		t.Fatal("the hosted session is not listed")
	}
	key := d.Info().HostKey
	r := e.hostsOf(t)
	if key == "" || r.This.Key != key || r.This.Hosted == nil || len(r.This.Hosted.SessionIDs) != 1 || r.This.Hosted.SessionIDs[0] != sid || len(r.Hosts) != 0 {
		t.Fatalf("hosted from this machine: key %q, this %+v, rows %+v", key, r.This, r.Hosts)
	}
	other := newTestEnv(t, nil)
	osid := runMachineHost(t, other, dir)
	ro := other.hostsOf(t)
	if row, ok := hostRowOf(ro, other.mustInfo(t, osid).HostKey); !ok || ro.This.Hosted != nil || row.KeyFrom != session.HostKeyMachine || row.Key == key {
		t.Fatalf("at a second server: this %+v, rows %+v", ro.This, ro.Hosts)
	}
}

// mustInfo is the listed session id's Info.
func (e *testEnv) mustInfo(t *testing.T, id string) session.Info {
	t.Helper()
	d, ok := e.srv.registry.Get(id)
	if !ok {
		t.Fatalf("session %s is not listed", id)
	}
	return d.Info()
}
