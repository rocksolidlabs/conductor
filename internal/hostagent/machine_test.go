package hostagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// keyServer is a server that answers each upgrade of /ws/host with the
// header given (none when ""), takes the register and answers it, and
// holds the connection until the host closes it.
type keyServer struct {
	url  string
	regs chan proto.Register
}

func newKeyServer(t *testing.T, header string) *keyServer {
	t.Helper()
	s := &keyServer{regs: make(chan proto.Register, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header != "" {
			w.Header().Set(proto.ServerKeyHeader, header)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(proto.MaxHostMessage)
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var reg proto.Register
		if json.Unmarshal(data, &reg) != nil {
			return
		}
		s.regs <- reg
		out, _ := json.Marshal(proto.Registered{T: proto.HostRegistered, SessionID: "s1", Secret: "secret", ShareBaseURL: "http://server.invalid"})
		if c.Write(r.Context(), websocket.MessageText, out) != nil {
			return
		}
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// registerAt dials the server at url with a's options and registers, then
// lets the connection go and forgets the session, so the next registration
// is a fresh one.
func registerAt(t *testing.T, a *agent, url string) {
	t.Helper()
	a.opts.ServerURL = url
	wsURL, err := controlURL(url)
	if err != nil {
		t.Fatal(err)
	}
	a.wsURL = wsURL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := a.dialAndRegister(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.dropConn(conn)
	conn.CloseNow()
	a.mu.Lock()
	a.sessID, a.secret = "", ""
	a.mu.Unlock()
}

// registerAt is the register s took of a's registration.
func (s *keyServer) registerAt(t *testing.T, a *agent) proto.Register {
	t.Helper()
	registerAt(t, a, s.url)
	select {
	case reg := <-s.regs:
		return reg
	default:
		// The server sends registered only after it took the register.
		t.Fatal("the server took no register")
		return proto.Register{}
	}
}

// machineHost is a host whose machine's id is id, logging to log.
func machineHost(t *testing.T, id string, log *slog.Logger) *agent {
	t.Helper()
	a, _, _ := newHostOf(t, session.Options{Log: discardLog}, 0, 0)
	a.opts = Options{Token: hostToken, Name: "machine", Argv: []string{"stub"}, AgentID: "stub", HostName: "h", ReconnectMax: time.Second, Log: log}
	a.log = log
	a.dir = t.TempDir()
	a.instance, a.localID = "instance", "1"
	a.machineID = id
	return a
}

// serverKeyHex is a server's key of the byte b, in hex.
func serverKeyHex(b byte) string {
	return hex.EncodeToString(bytes.Repeat([]byte{b}, machine.ServerKeyLen))
}

// lockedBuffer is a log's output read while the host may still write it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// To a server, the register carries the key of the machine made from the
// key the server's upgrade sent, and the platform; never the id. One
// machine registering with two servers sends two unrelated keys, and the
// host's log holds neither the id nor a key.
func TestRegistrationCarriesMachine(t *testing.T) {
	id, err := machine.ID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out lockedBuffer
	a := machineHost(t, id, slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	one, two := serverKeyHex(1), serverKeyHex(2)
	first := newKeyServer(t, one).registerAt(t, a)
	second := newKeyServer(t, two).registerAt(t, a)
	k1, k2 := machine.Key(id, bytes.Repeat([]byte{1}, machine.ServerKeyLen)), machine.Key(id, bytes.Repeat([]byte{2}, machine.ServerKeyLen))
	if first.Host.Machine != k1 || second.Host.Machine != k2 || k1 == k2 {
		t.Fatalf("sent %q and %q; want %q and %q", first.Host.Machine, second.Host.Machine, k1, k2)
	}
	if first.Host.Platform != machine.Platform() || first.Host.Platform == "" {
		t.Fatalf("platform %q, this machine's %q", first.Host.Platform, machine.Platform())
	}
	for _, reg := range []proto.Register{first, second} {
		if b, _ := json.Marshal(reg); strings.Contains(string(b), id) {
			t.Fatalf("the register holds the machine's id: %s", b)
		}
		if err := reg.Host.CheckMachine(); err != nil {
			t.Fatalf("the host sent what a server refuses: %v", err)
		}
	}
	logged := out.String()
	for _, secret := range []string{id, k1, k2, one, two} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the log holds %q:\n%s", secret, logged)
		}
	}
}

// Through a real server: a host on this server's machine is kept under the
// key the server keys its own row by, the listing carries that key and
// what the host said of itself, and neither the id nor the key the host
// sent; a second start of the server (its key made again) keeps the same
// machine under another key.
func TestHostOnTheServersMachineSharesItsKey(t *testing.T) {
	id, err := machine.ID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listed := func(base string) []session.Info {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/api/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw bytes.Buffer
		raw.ReadFrom(resp.Body)
		if strings.Contains(raw.String(), id) {
			t.Fatalf("the listing holds the machine's id: %s", raw.String())
		}
		var list struct{ Sessions []session.Info }
		if err := json.Unmarshal(raw.Bytes(), &list); err != nil {
			t.Fatalf("the listing: %v: %s", err, raw.String())
		}
		return list.Sessions
	}
	var keys []string
	for range 2 {
		srv, hs := startServer(t)
		srv.SetOwnMachine(id)
		own := srv.OwnHostKey()
		if !proto.ValidHostMachine(own) {
			t.Fatalf("the server's own key %q", own)
		}
		a := machineHost(t, id, discardLog)
		a.opts.ServerURL = hs.URL
		wsURL, _ := controlURL(hs.URL)
		a.wsURL = wsURL
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, _, err := a.dialAndRegister(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		list := listed(hs.URL)
		if len(list) != 1 {
			t.Fatalf("listed %d sessions", len(list))
		}
		got := list[0]
		if got.HostKey != own || got.HostKeyFrom != session.HostKeyMachine || got.HostPlatform != machine.Platform() || got.HostLink != session.HostLinkUp {
			t.Fatalf("the hosted session: key %q from %q platform %q link %q; the server's own %q", got.HostKey, got.HostKeyFrom, got.HostPlatform, got.HostLink, own)
		}
		keys = append(keys, own)
	}
	if keys[0] == keys[1] {
		t.Fatal("two starts of a server keep one machine under one key")
	}
}

// An upgrade with no key, or with one not of 64 lowercase hex digits,
// sends no machine; the rest of the register is as it is.
func TestBadServerKeySendsNoMachine(t *testing.T) {
	id, err := machine.ID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := machineHost(t, id, discardLog)
	good := serverKeyHex(0xab)
	for _, header := range []string{"", good[:len(good)-2], good + "00", strings.ToUpper(good), "zz" + good[2:], good[:63] + " "} {
		reg := newKeyServer(t, header).registerAt(t, a)
		if reg.Host.Machine != "" {
			t.Errorf("the header %q: machine %q sent", header, reg.Host.Machine)
		}
		if reg.Host.Platform == "" || reg.Host.Instance == "" {
			t.Errorf("the header %q: the register lost the rest: %+v", header, reg.Host)
		}
	}
	// The same host, given a good key, sends one: the refusals above were
	// the header's.
	if reg := newKeyServer(t, good).registerAt(t, a); reg.Host.Machine == "" {
		t.Fatal("a good key sent no machine")
	}
}

// A switchyard is sent no machine and no platform, though its upgrade
// carries a key of the header's form and the host has an id: neither a host
// told the server is one (Options.Switchyard) nor a server's publication
// (the Uplink).
func TestNoMachineToASwitchyard(t *testing.T) {
	id, err := machine.ID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	yard := newKeyServer(t, serverKeyHex(4))
	a := machineHost(t, id, discardLog)
	a.opts.Switchyard = true
	if reg := yard.registerAt(t, a); reg.Host.Machine != "" || reg.Host.Platform != "" {
		t.Fatalf("a switchyard was sent %+v", reg.Host)
	}
	if got := machineIDOf(t.TempDir(), true, discardLog); got != "" {
		t.Fatalf("a host of a switchyard read the id %q", got)
	}

	local := catSession(t, "local-m")
	u := &Uplink{ServerURL: yard.url, Token: hostToken, HostName: "office", Log: discardLog}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pub, err := u.Publish(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	select {
	case reg := <-yard.regs:
		if reg.Host.Machine != "" || reg.Host.Platform != "" {
			t.Fatalf("a publication sent the switchyard %+v", reg.Host)
		}
	default:
		t.Fatal("Publish returned before the switchyard took a register")
	}
}

// catSession is a server session running cat, stopped at the test's end.
func catSession(t *testing.T, id string) *session.Local {
	t.Helper()
	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	local := session.NewLocal(session.Info{ID: id, Name: "published", Kind: session.KindServer, AgentID: "cat", Command: []string{"/bin/cat"}, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24, CreatedAt: time.Now().UTC()}, proc, session.Options{Log: discardLog})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = local.Stop(ctx)
	})
	return local
}

// An id that cannot be read sends none: one warning, naming the file and
// not its bytes, and the register to a server whose upgrade carries a key
// has no machine.
func TestUnreadableMachineIDSendsNone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, machine.FileName)
	const near = "5e1d0c9b8a7f6e5d4c3b2a1908f7e6d\n"
	if err := os.WriteFile(p, []byte(near), 0o600); err != nil {
		t.Fatal(err)
	}
	var out lockedBuffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	id := machineIDOf(dir, false, log)
	if id != "" {
		t.Fatalf("read %q", id)
	}
	logged := out.String()
	if strings.Count(logged, "level=WARN") != 1 || !strings.Contains(logged, p) || strings.Contains(logged, strings.TrimSpace(near)) || strings.Contains(logged, "5e1d0c9b") {
		t.Fatalf("the warning:\n%s", logged)
	}
	a := machineHost(t, id, log)
	if reg := newKeyServer(t, serverKeyHex(5)).registerAt(t, a); reg.Host.Machine != "" {
		t.Fatalf("machine %q sent without an id", reg.Host.Machine)
	}
	if strings.Count(out.String(), "level=WARN") != 1 {
		t.Fatalf("warned again:\n%s", out.String())
	}
}

// A publication says its connection is up while it is, false while it is
// down (the link between the two cut), and true again once it has
// registered anew; false once it has ended.
func TestPublishedConnected(t *testing.T) {
	_, hs := startServer(t)
	link := newCutLink(t, strings.TrimPrefix(hs.URL, "http://"))
	local := catSession(t, "local-c")
	u := &Uplink{ServerURL: link.url(), Token: hostToken, HostName: "office", ReconnectMax: 200 * time.Millisecond, Log: discardLog}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pub, err := u.Publish(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pub.Stop)
	if !pub.Connected() {
		t.Fatal("a publication just registered is not connected")
	}
	// Polled for the state the host reaches on its own (its connection's
	// loss, its next registration); the test's own timeout bounds a hang.
	until := func(want bool) {
		t.Helper()
		for pub.Connected() != want {
			time.Sleep(5 * time.Millisecond)
		}
	}
	link.sever()
	until(false)
	link.mend()
	until(true)
	pub.Stop()
	<-pub.Done()
	if pub.Connected() {
		t.Fatal("an ended publication says it is connected")
	}
}
