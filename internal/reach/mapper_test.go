package reach

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v4"
)

// silentUDP is an address nothing answers at: SSDP finds no gateway there.
func silentUDP(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn.LocalAddr().String()
}

func stunAnswering(t *testing.T, ip string) string {
	t.Helper()
	return newFakeSTUN(t, func(_ int, req *stun.Message) *stun.Message { return success(req, ip, 40000) }).url()
}

// run starts the mapper and returns a stop that cancels Run and closes the
// mappings.
func run(t *testing.T, m *Mapper) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	stop = func() {
		cancel()
		<-done
		cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer ccancel()
		m.Close(cctx)
	}
	t.Cleanup(stop)
	return stop
}

func await(t *testing.T, m *Mapper, within time.Duration, cond func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		st := m.Status()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never satisfied: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMapperMapsThroughUPnPAndUnmapsOnClose(t *testing.T) {
	igd := newFakeIGD(t, svcWANIP2)
	ssdp := newFakeSSDP(t, igdDevice2, igd.location())
	gw := newFakeGateway(t, true)
	verify := make(chan struct{})
	m := New(Options{Mode: ModeAuto, Ports: []PortMap{{External: 443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.9"),
		SSDPAddr: ssdp.addr(), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port(),
		Verify: func(ctx context.Context, _ string) string {
			select {
			case <-verify:
				return "ok"
			case <-ctx.Done():
				return ""
			}
		}})
	stop := run(t, m)
	// The mapper shows the mapping before it verifies it: a status with
	// Mapped and no Verified yet is one it passes through. The verify is
	// held until that status is seen, then the wait is for the verified
	// one the assertions read, never for Mapped alone.
	if st := await(t, m, 5*time.Second, func(s Status) bool { return s.Mapped }); st.Verified != "" {
		t.Fatalf("verified before the verify ran: %+v", st)
	}
	close(verify)
	st := await(t, m, 5*time.Second, func(s Status) bool { return s.Verified != "" })
	if !st.Mapped || st.Method != MethodUPnP || st.PublicURL != "https://203.0.113.9" || st.ExternalPort != 443 || st.ListenPort != 8443 || st.Verified != "ok" || st.Err != "" {
		t.Fatalf("status %+v", st)
	}
	if st.ExpiresAt.Before(time.Now().Add(50*time.Minute)) || st.RenewedAt.IsZero() {
		t.Fatalf("lease times %+v", st)
	}
	if n := len(gw.seen()); n != 0 {
		t.Fatalf("PCP/NAT-PMP were asked (%d requests) although UPnP mapped", n)
	}
	stop()
	igd.mu.Lock()
	_, still := igd.mappings[443]
	igd.mu.Unlock()
	if still {
		t.Fatal("the mapping outlived Close")
	}
	if st := m.Status(); st.Mapped || st.PublicURL != "" {
		t.Fatalf("status after close %+v", st)
	}
}

func TestMapperFallsBackToPCPThenNATPMP(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		pcp          bool
	}{{"pcp", MethodPCP, true}, {"nat-pmp", MethodNATPMP, false}} {
		t.Run(tc.name, func(t *testing.T) {
			gw := newFakeGateway(t, tc.pcp)
			m := New(Options{Mode: ModeAuto, Ports: []PortMap{{External: 443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.7"),
				SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port()})
			run(t, m)
			st := await(t, m, 5*time.Second, func(s Status) bool { return s.Mapped })
			if st.Method != tc.method || st.PublicURL != "https://203.0.113.7" || st.ExternalPort != 443 {
				t.Fatalf("status %+v", st)
			}
			if _, ok := gw.mapping(8443); !ok {
				t.Fatal("the gateway holds no mapping")
			}
		})
	}
}

func TestMapperRenewsBeforeExpiry(t *testing.T) {
	gw := newFakeGateway(t, false)
	m := New(Options{Mode: ModeAuto, Ports: []PortMap{{External: 443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.7"),
		SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port(), Lease: 2 * time.Second})
	run(t, m)
	first := await(t, m, 5*time.Second, func(s Status) bool { return s.Mapped })
	await(t, m, 4*time.Second, func(s Status) bool { return s.Mapped && s.RenewedAt.After(first.RenewedAt) })
	maps := 0
	for _, r := range gw.seen() {
		if strings.HasPrefix(r, "pmp map 8443<-443 2") {
			maps++
		}
	}
	if maps < 2 {
		t.Fatalf("the gateway saw %d map requests, want a renewal: %v", maps, gw.seen())
	}
}

func TestMapperManualModeOnlyAsksSTUN(t *testing.T) {
	gw := newFakeGateway(t, true)
	m := New(Options{Mode: ModeManual, Ports: []PortMap{{External: 8443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.7"),
		SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port()})
	// The address shows before the manual URL does (setAddress, its
	// OnAddress callbacks, then setManual): a callback held here holds
	// the mapper in that gap, and the wait is for the URL itself.
	held := make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	m.OnAddress(func(netip.Addr) { <-held })
	run(t, m)
	t.Cleanup(release) // before run's stop, which waits for Run
	if st := await(t, m, 5*time.Second, func(s Status) bool { return s.ExternalIP.IsValid() }); st.Method != "" || st.PublicURL != "" {
		t.Fatalf("the manual URL before its address's callbacks ran: %+v", st)
	}
	release()
	st := await(t, m, 5*time.Second, func(s Status) bool { return s.Method != "" })
	if st.Mapped || st.Method != MethodManual || st.PublicURL != "https://203.0.113.7:8443" || st.ExternalPort != 8443 {
		t.Fatalf("status %+v", st)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(gw.seen()); n != 0 {
		t.Fatalf("the gateway was asked %d times in manual mode", n)
	}
}

func TestMapperAutoWithoutPortsOnlyDiscovers(t *testing.T) {
	gw := newFakeGateway(t, true)
	m := New(Options{Mode: ModeAuto, STUNServer: stunAnswering(t, "203.0.113.7"), SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port()})
	// The address shows in the status before its OnAddress callbacks run:
	// the wait is for the callback, not the status.
	seen := make(chan netip.Addr, 8)
	m.OnAddress(func(a netip.Addr) { seen <- a })
	run(t, m)
	select {
	case a := <-seen:
		if a != netip.MustParseAddr("203.0.113.7") {
			t.Fatalf("OnAddress saw %v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnAddress was never called")
	}
	st := await(t, m, 5*time.Second, func(s Status) bool { return s.ExternalIP.IsValid() })
	if st.Mapped || st.PublicURL != "" || st.Method != "" || st.Err != "" {
		t.Fatalf("status %+v", st)
	}
	if n := len(seen); n != 0 {
		t.Fatalf("OnAddress was called %d more times for one address", n)
	}
}

func TestMapperReportsFailureAndRetries(t *testing.T) {
	gw := newFakeGateway(t, false)
	gw.set(func() { gw.pmpResult = 2 })
	m := New(Options{Mode: ModeAuto, Ports: []PortMap{{External: 443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.7"),
		SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port(), Retry: 100 * time.Millisecond, RetryMax: 200 * time.Millisecond})
	run(t, m)
	st := await(t, m, 5*time.Second, func(s Status) bool { return s.Err != "" })
	if st.Mapped || !strings.Contains(st.Err, "not authorized") || !strings.Contains(st.Err, "UPnP, PCP and NAT-PMP") {
		t.Fatalf("status %+v", st)
	}
	gw.set(func() { gw.pmpResult = 0 })
	if st := await(t, m, 5*time.Second, func(s Status) bool { return s.Mapped }); st.Err != "" || st.Method != MethodNATPMP {
		t.Fatalf("status %+v", st)
	}
}

func TestMapperFlagsADoubleNAT(t *testing.T) {
	gw := newFakeGateway(t, false)
	gw.set(func() { gw.external = netip.MustParseAddr("10.0.0.5") })
	m := New(Options{Mode: ModeAuto, Ports: []PortMap{{External: 443, Internal: 8443}}, STUNServer: stunAnswering(t, "203.0.113.7"),
		SSDPAddr: silentUDP(t), Gateway: gw.addr.Addr(), GatewayPort: gw.addr.Port(), Retry: time.Minute})
	run(t, m)
	st := await(t, m, 5*time.Second, func(s Status) bool { return s.Err != "" })
	if st.Mapped || !strings.Contains(st.Err, "another NAT") {
		t.Fatalf("status %+v", st)
	}
	if _, ok := gw.mapping(8443); ok {
		t.Fatal("a mapping was left on a gateway behind another NAT")
	}
}

func TestMapperReportsTheSTUNFailure(t *testing.T) {
	m := New(Options{Mode: ModeAuto, STUNServer: "stun:" + silentUDP(t), Retry: time.Minute})
	run(t, m)
	st := await(t, m, 6*time.Second, func(s Status) bool { return s.Err != "" })
	if !strings.Contains(st.Err, "public address is unknown") {
		t.Fatalf("status %+v", st)
	}
}

func TestMapperOffDoesNothing(t *testing.T) {
	m := New(Options{Mode: ModeOff, STUNServer: "stun:" + silentUDP(t)})
	done := make(chan struct{})
	go func() { m.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return in off mode")
	}
	if st := m.Status(); st.Mode != ModeOff || st.ExternalIP.IsValid() {
		t.Fatalf("status %+v", st)
	}
}

func TestPublicURLOmitsTheDefaultPort(t *testing.T) {
	m := New(Options{Scheme: "https"})
	if u := m.publicURL(netip.MustParseAddr("203.0.113.7"), 443); u != "https://203.0.113.7" {
		t.Fatal(u)
	}
	if u := m.publicURL(netip.MustParseAddr("203.0.113.7"), 8443); u != "https://203.0.113.7:8443" {
		t.Fatal(u)
	}
	if u := m.publicURL(netip.MustParseAddr("2001:db8::1"), 443); u != "https://[2001:db8::1]" {
		t.Fatal(u)
	}
}

func TestVerifyMatchesTheInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"instance":"abc"}`))
	}))
	defer srv.Close()
	if v := (Verifier{Instance: "abc", Client: srv.Client()}).Verify(context.Background(), srv.URL); v != "ok" {
		t.Fatalf("got %q", v)
	}
	if v := (Verifier{Instance: "other", Client: srv.Client()}).Verify(context.Background(), srv.URL); v != "unverified" {
		t.Fatalf("got %q", v)
	}
	if v := (Verifier{Instance: "abc"}).Verify(context.Background(), "http://127.0.0.1:1"); v != "unverified" {
		t.Fatalf("got %q", v)
	}
}
