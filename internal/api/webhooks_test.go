package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// logBuffer collects what a server logs; the webhook goroutines write to it
// while a test reads it.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// wait blocks until the log holds s.
func (l *logBuffer) wait(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(l.String(), s) {
		if time.Now().After(deadline) {
			t.Fatalf("the log never said %q:\n%s", s, l.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newWebhookEnv is a test server that delivers to hooks and logs, at debug
// level, to the buffer it returns.
func newWebhookEnv(t *testing.T, hooks ...config.Webhook) (*testEnv, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return newTestEnvLogging(t, func(c *config.Config) { c.Webhooks = hooks }, log), logs
}

// delivery is one request a receiver was sent.
type delivery struct {
	method, path, query string
	header              http.Header
	body                []byte
}

// webhookPayload decodes the body, refusing fields the payload does not have.
func (d delivery) webhookPayload(t *testing.T) (p struct {
	SessionID string `json:"sessionId"`
	Session   struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		AgentID string `json:"agentId"`
	} `json:"session"`
	Entry map[string]any `json:"entry"`
}) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(d.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("payload %s: %v", d.body, err)
	}
	return p
}

// message is the message of the entry a delivery carries.
func (d delivery) message(t *testing.T) string {
	t.Helper()
	m, _ := d.webhookPayload(t).Entry["message"].(string)
	return m
}

// receiver stands in for a webhook's endpoint: it records every request, then
// answers with handle, or 204 without one.
type receiver struct {
	*httptest.Server
	got chan delivery
}

func newReceiver(t *testing.T, handle http.HandlerFunc) *receiver {
	t.Helper()
	r := &receiver{got: make(chan delivery, 1024)}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.got <- delivery{req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), body}
		if handle != nil {
			handle(w, req)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.Close)
	return r
}

// next waits for the next request.
func (r *receiver) next(t *testing.T) delivery {
	t.Helper()
	select {
	case d := <-r.got:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no webhook request arrived")
	}
	return delivery{}
}

// none checks that no request arrives for a while.
func (r *receiver) none(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case d := <-r.got:
		t.Fatalf("an unexpected webhook request: %s %s", d.header.Get("X-Conductor-Event"), d.body)
	case <-time.After(wait):
	}
}

func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// A webhook receives each entry of a type it lists as one signed JSON POST,
// and nothing of the types it does not list: here a progress event and no
// tool_use. The body names the session and carries the entry as the admin
// event stream sends it.
func TestWebhookDeliversSignedEventsOfTheTypesItLists(t *testing.T) {
	rcv := newReceiver(t, nil)
	e, logs := newWebhookEnv(t, config.Webhook{URL: rcv.URL + "/conductor?token=t0k3n", Events: []string{"progress"}, Secret: "s3cret", AllowPrivate: true})
	events := e.sse(t)
	id := e.createSession("cat")
	tok := e.agentToken(id)
	for _, body := range []map[string]any{
		{"type": "tool_use", "tool": "Bash"},
		{"type": "progress", "message": "4/7 handlers"},
	} {
		if resp, out := e.do("POST", "/api/sessions/"+id+"/events", tok, body); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%v: %d %v", body, resp.StatusCode, out)
		}
	}
	streamed := activityPayload(t, e.waitEvent(t, events, isActivity("progress", id)))

	// tool_use was recorded first and the webhook's queue keeps the order:
	// had it been queued, it would have arrived first.
	d := rcv.next(t)
	if d.method != http.MethodPost || d.path != "/conductor" || d.query != "token=t0k3n" {
		t.Fatalf("request %s %s?%s", d.method, d.path, d.query)
	}
	if ct := d.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
	if ev := d.header.Get("X-Conductor-Event"); ev != "progress" {
		t.Fatalf("X-Conductor-Event %q", ev)
	}
	if sig, want := d.header.Get("X-Conductor-Signature"), signature("s3cret", d.body); sig != want {
		t.Fatalf("X-Conductor-Signature %q, want %q", sig, want)
	}
	p := d.webhookPayload(t)
	info, _ := e.srv.registry.Get(id)
	if p.SessionID != id || p.Session.ID != id || p.Session.Name != info.Info().Name || p.Session.AgentID != "cat" {
		t.Fatalf("payload names %+v", p)
	}
	delete(streamed, "sessionId")
	if !reflect.DeepEqual(p.Entry, streamed) || p.Entry["type"] != "progress" || p.Entry["message"] != "4/7 handlers" || p.Entry["byName"] != "agent" {
		t.Fatalf("entry %v, the stream sent %v", p.Entry, streamed)
	}
	rcv.none(t, 300*time.Millisecond)
	if strings.Contains(logs.String(), "s3cret") || strings.Contains(logs.String(), "t0k3n") {
		t.Fatalf("the log holds the secret or the query:\n%s", logs)
	}
}

// Beside the entry types, a webhook may list what the Events page routes: an
// attention entry reaches one that lists the state it records, and
// the status entry of a process that exited on its own with a non-zero code
// one that lists exit_nonzero, never the status of a process an admin
// stopped. X-Conductor-Event names what the webhook listed. A webhook without
// a secret sends no signature.
func TestWebhookListsAttentionStatesAndExitNonZero(t *testing.T) {
	routed, raw := newReceiver(t, nil), newReceiver(t, nil)
	e, _ := newWebhookEnv(t,
		config.Webhook{URL: routed.URL, Events: []string{"needs_input", "exit_nonzero"}, AllowPrivate: true},
		config.Webhook{URL: raw.URL, Events: []string{"attention", "status"}, AllowPrivate: true},
	)
	id := e.createSession("cat")
	tok := e.agentToken(id)
	for _, body := range []map[string]any{
		{"type": "needs_input", "message": "approve?"},
		{"type": "working"},
	} {
		if resp, out := e.do("POST", "/api/sessions/"+id+"/events", tok, body); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%v: %d %v", body, resp.StatusCode, out)
		}
	}
	check := func(r *receiver, event, message string) {
		t.Helper()
		d := r.next(t)
		if got := d.header.Get("X-Conductor-Event"); got != event || d.message(t) != message {
			t.Fatalf("got %s %q, want %s %q", got, d.message(t), event, message)
		}
		if _, signed := d.header["X-Conductor-Signature"]; signed {
			t.Fatalf("a webhook without a secret signed: %v", d.header)
		}
	}
	check(routed, "needs_input", "approve?")
	check(raw, "attention", "approve?")
	check(raw, "attention", "working")

	exited := e.createSession("exit") // exits 4 on its own
	e.waitEnded(exited)
	check(routed, "exit_nonzero", "exited (exit 4)")
	check(raw, "status", "exited (exit 4)")

	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode >= 300 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	e.waitEnded(id)
	d := raw.next(t)
	if msg := d.message(t); d.header.Get("X-Conductor-Event") != "status" || !strings.HasPrefix(msg, "stopped") {
		t.Fatalf("stop: %s %q", d.header.Get("X-Conductor-Event"), msg)
	}
	routed.none(t, 300*time.Millisecond)
}

// eventTypeOf types an entry as the Events page does (eventTypeOf in
// web/app/utils/events.ts): an attention entry is the state it records, a
// status entry is exit_nonzero for a process that exited on its own with a
// non-zero code, the six event types are themselves. The state an attention
// entry records comes with it (set with the entry's stamp on a server
// session, sent by the host on a hosted one); the entry names it itself when
// the report had no message, and nothing else types it.
func TestWebhookEventTypeOfFollowsTheEventsPage(t *testing.T) {
	entry := func(typ, message string) session.ActivityEntry {
		return session.ActivityEntry{Type: typ, Message: message}
	}
	for _, tc := range []struct {
		e     session.ActivityEntry
		state session.AttentionState
		want  string
	}{
		{entry("attention", "Claude needs your permission to use Bash"), "needs_input", "needs_input"},
		{entry("attention", "All tests pass"), "done", "done"},
		{entry("attention", "compiling"), "working", "working"},
		{entry("attention", "working"), "", "working"},
		{entry("attention", "done"), "", "done"},
		{entry("attention", "needs_input"), "", "needs_input"},
		{entry("attention", "done"), "needs_input", "needs_input"},
		{entry("attention", "Waiting"), "", ""},
		{entry("attention", "Waiting"), "clear", ""},
		{entry("status", "exited (exit 1)"), "", "exit_nonzero"},
		{entry("status", "exited (exit -1)"), "", "exit_nonzero"},
		{entry("status", "exited (exit 0)"), "", ""},
		{entry("status", "exited"), "", ""},
		{entry("status", "stopped (exit 143)"), "", ""},
		{entry("status", "running"), "", ""},
		{entry("progress", ""), "", "progress"},
		{entry("artifact", ""), "", "artifact"},
		{entry("handoff", ""), "", "handoff"},
		{entry("tool_use", ""), "", "tool_use"},
		{entry("tool_denied", ""), "", "tool_denied"},
		{entry("error", ""), "", "error"},
		{entry("progress", ""), "needs_input", "progress"},
		{entry("join", "x"), "", ""},
		{entry("leave", "x"), "", ""},
		{entry("input", "x"), "", ""},
		{entry("link", "x"), "", ""},
	} {
		if got := eventTypeOf(tc.e, tc.state); got != tc.want {
			t.Errorf("%s %q with state %q: %q, want %q", tc.e.Type, tc.e.Message, tc.state, got, tc.want)
		}
	}
	// A webhook that lists both an entry's own type and the one it is typed
	// as hears of it once, by the more specific name.
	lists := func(events ...string) *webhook {
		w := &webhook{events: map[string]bool{}}
		for _, ev := range events {
			w.events[ev] = true
		}
		return w
	}
	for _, tc := range []struct {
		events   []string
		typ, own string
		want     string
	}{
		{[]string{"attention", "needs_input"}, "needs_input", "attention", "needs_input"},
		{[]string{"attention", "done"}, "needs_input", "attention", "attention"},
		{[]string{"done"}, "needs_input", "attention", ""},
		{[]string{"status", "exit_nonzero"}, "exit_nonzero", "status", "exit_nonzero"},
		{[]string{"status", "exit_nonzero"}, "", "status", "status"},
		{[]string{"exit_nonzero"}, "", "status", ""},
		{[]string{"progress"}, "progress", "progress", "progress"},
		{[]string{"progress"}, "tool_use", "tool_use", ""},
		{[]string{"join"}, "", "join", "join"},
	} {
		if got := lists(tc.events...).eventFor(tc.typ, tc.own); got != tc.want {
			t.Errorf("%v gets %s typed %q: %q, want %q", tc.events, tc.own, tc.typ, got, tc.want)
		}
	}
}

// A host that passes the address rule when the config is checked and resolves
// to a loopback address when the server dials it (DNS rebinding) is refused
// at the dial, and nothing reaches the loopback address. A webhook to the
// same URL that allows private addresses gets through: the refusal is the
// rule's.
func TestWebhookRefusesAHostThatRebindsToAPrivateAddress(t *testing.T) {
	rcv := newReceiver(t, nil)
	_, port, err := net.SplitHostPort(rcv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// At validation the name is public; when the server dials, it is not.
	old := config.LookupWebhookHost
	config.LookupWebhookHost = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "rebind.test" {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	}
	t.Cleanup(func() { config.LookupWebhookHost = old })
	dns := newFakeDNS(t)
	dns.set("rebind.test", "127.0.0.1")
	useResolver(t, dns.resolver())
	url := "http://rebind.test:" + port + "/hook"
	e, logs := newWebhookEnv(t,
		config.Webhook{URL: url, Events: []string{"progress"}},
		config.Webhook{URL: url, Events: []string{"progress"}, AllowPrivate: true},
	)
	id := e.createSession("cat")
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", e.agentToken(id), map[string]any{"type": "progress", "message": "1/2"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	logs.wait(t, "127.0.0.1 is a loopback address")
	if lines := strings.Count(logs.String(), `msg="webhook delivery failed" webhook=0 `); lines != 1 || strings.Contains(logs.String(), `msg="webhook delivery failed" webhook=1 `) {
		t.Fatalf("the refusal is not the first webhook's, once:\n%s", logs)
	}
	if d := rcv.next(t); d.message(t) != "1/2" {
		t.Fatalf("delivered %s", d.body)
	}
	rcv.none(t, 300*time.Millisecond)
}

// When the first address of a host does not answer, the next one gets its
// turn within the 5 seconds a delivery has: the server dials as the standard
// dialer does, which gives each address a share of the time, and does not
// spend it all on the first.
func TestWebhookDialsTheNextAddressWhenOneHangs(t *testing.T) {
	rcv := newReceiver(t, nil)
	_, portText, err := net.SplitHostPort(rcv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	blackhole(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.2"), uint16(port)))
	dns := newFakeDNS(t)
	dns.set("eyeballs.test", "127.0.0.2", "127.0.0.1")
	useResolver(t, dns.resolver())
	e, logs := newWebhookEnv(t, config.Webhook{URL: "http://eyeballs.test:" + portText + "/hook", Events: []string{"artifact"}, AllowPrivate: true})
	start := time.Now()
	e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityArtifact, URL: "https://x/1"}, "", "")
	d := rcv.next(t)
	took := time.Since(start)
	if d.header.Get("X-Conductor-Event") != "artifact" {
		t.Fatalf("delivered %s", d.body)
	}
	// The first address was tried, and given up in time for the second.
	if took < time.Second || took >= webhookTimeout {
		t.Fatalf("delivered after %v", took)
	}
	if strings.Contains(logs.String(), "webhook delivery failed") {
		t.Fatalf("log:\n%s", logs)
	}
}

// blackhole makes connections to addr hang: a socket listens there with no
// room in its accept queue, and Linux drops the handshake of every connection
// past the one that fills it.
func blackhole(t *testing.T, addr netip.AddrPort) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("a full accept queue drops connections only on Linux")
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: int(addr.Port()), Addr: addr.Addr().As4()}); err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	if filler, err := net.DialTimeout("tcp", addr.String(), time.Second); err == nil {
		t.Cleanup(func() { filler.Close() })
	}
	if probe, err := net.DialTimeout("tcp", addr.String(), 300*time.Millisecond); err == nil {
		probe.Close()
		t.Skip("this system answers connections past a full accept queue")
	}
}

// One slow endpoint holds up neither the session nor the other webhooks: the
// session only queues, and a full queue drops its oldest entry. What the
// endpoint gets once it answers again is the newest 256, in order.
func TestWebhookQueueDropsTheOldestAndNeverDelaysTheSession(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	stalled := make(chan struct{})
	var stallOnce sync.Once
	slow := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		stallOnce.Do(func() {
			close(stalled)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	fast := newReceiver(t, nil)
	e, logs := newWebhookEnv(t,
		config.Webhook{URL: slow.URL, Events: []string{"progress"}, AllowPrivate: true},
		config.Webhook{URL: fast.URL, Events: []string{"error"}, AllowPrivate: true},
	)
	id := e.createSession("cat")
	progress := func(message string) session.ActivityEntry {
		return session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityProgress, Message: message}
	}

	e.srv.events.activity(id, progress("0"), "", "")
	select {
	case <-stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow endpoint was never called")
	}
	// The delivery of 0 hangs. 456 more entries arrive, far faster than the
	// endpoint answers: 256 wait, the 200 oldest are dropped.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 456; i++ {
			e.srv.events.activity(id, progress(strconv.Itoa(i)), "", "")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recording waited for the webhook")
	}
	hook := e.srv.webhooks.hooks[0]
	if queued, dropped := hook.stats(); queued != webhookQueue || dropped != 200 {
		t.Fatalf("queued %d, dropped %d", queued, dropped)
	}
	// The first drop and every 100th are logged.
	var counts []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "dropped the oldest") {
			counts = append(counts, line[strings.LastIndex(line, "dropped=")+len("dropped="):])
		}
	}
	if !slices.Equal(counts, []string{"1", "100", "200"}) {
		t.Fatalf("logged the drops %q, want the 1st, 100th and 200th:\n%s", counts, logs)
	}

	// Another webhook delivers meanwhile.
	e.srv.events.activity(id, session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityError, Message: "boom"}, "", "")
	if d := fast.next(t); d.message(t) != "boom" {
		t.Fatalf("the other webhook got %s", d.body)
	}
	// And the session's own route answers at once.
	tok := e.agentToken(id)
	start := time.Now()
	for i := 1; i <= 3; i++ {
		if resp, out := e.do("POST", "/api/sessions/"+id+"/events", tok, map[string]any{"type": "progress", "message": fmt.Sprintf("api-%d", i)}); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%d %v", resp.StatusCode, out)
		}
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("three events took %v while the endpoint hung", took)
	}
	if _, dropped := hook.stats(); dropped != 203 {
		t.Fatalf("dropped %d, want 203", dropped)
	}

	releaseOnce.Do(func() { close(release) })
	var got []string
	for len(got) < 1+253+3 {
		got = append(got, slow.next(t).message(t))
	}
	want := []string{"0"}
	for i := 204; i <= 456; i++ {
		want = append(want, strconv.Itoa(i))
	}
	want = append(want, "api-1", "api-2", "api-3")
	if !slices.Equal(got, want) {
		t.Fatalf("delivered %v\nwant %v", got, want)
	}
	slow.none(t, 200*time.Millisecond)
}

// A webhook's answer is taken as it is: a redirect is not followed, and an
// answer other than 2xx is logged at warn level, without its body or the
// webhook's query string.
func TestWebhookDoesNotFollowRedirects(t *testing.T) {
	target := newReceiver(t, nil)
	redirect := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	})
	e, logs := newWebhookEnv(t, config.Webhook{URL: redirect.URL + "/hook?token=t0k3n", Events: []string{"handoff"}, AllowPrivate: true})
	e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityHandoff, To: "tests"}, "", "")
	if d := redirect.next(t); d.path != "/hook" {
		t.Fatalf("request %s", d.path)
	}
	logs.wait(t, "status=307")
	target.none(t, 300*time.Millisecond)
	if l := logs.String(); !strings.Contains(l, "level=WARN") || strings.Contains(l, "elsewhere") || strings.Contains(l, "t0k3n") {
		t.Fatalf("log:\n%s", l)
	}
}

// Each delivery has 5 seconds, redirects are not followed, and no proxy is
// ever used: the address check must see where the request goes.
func TestWebhookClientBounds(t *testing.T) {
	c := webhookClient(false)
	if c.Timeout != 5*time.Second {
		t.Fatalf("timeout %v", c.Timeout)
	}
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect: %v", err)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil || tr.DialContext == nil {
		t.Fatalf("transport %#v", c.Transport)
	}
}

// Shutdown stops the deliveries: the one in flight is abandoned, the
// goroutines end, and entries recorded afterwards are not sent.
func TestWebhookDeliveriesStopWithTheServer(t *testing.T) {
	abandoned := make(chan struct{})
	rcv := newReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(abandoned)
	})
	e, _ := newWebhookEnv(t, config.Webhook{URL: rcv.URL, Events: []string{"artifact"}, AllowPrivate: true})
	e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityArtifact, URL: "https://x/1"}, "", "")
	rcv.next(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	e.srv.Shutdown(ctx)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("shutdown took %v", took)
	}
	select {
	case <-abandoned:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery in flight was not abandoned")
	}
	stopped := make(chan struct{})
	go func() {
		e.srv.webhooks.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook goroutines did not end")
	}
	e.srv.events.activity("s1", session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityArtifact, URL: "https://x/2"}, "", "")
	rcv.none(t, 300*time.Millisecond)
	if queued, _ := e.srv.webhooks.hooks[0].stats(); queued != 0 {
		t.Fatalf("queued %d after shutdown", queued)
	}
}

// GET /api/integrations lists the webhooks for the read-only column of the
// routing matrix: each URL without its user info, query or fragment, and its
// events; never a secret. Without webhooks the list is empty, not null.
func TestIntegrationsListTheWebhooks(t *testing.T) {
	e, _ := newWebhookEnv(t,
		config.Webhook{URL: "https://us3r:pa55@127.0.0.1:1/hook?token=t0k3n#fr4g", Events: []string{"needs_input", "exit_nonzero"}, Secret: "s3cret", AllowPrivate: true},
		config.Webhook{URL: "http://127.0.0.1:2", Events: []string{"progress"}, AllowPrivate: true},
	)
	req, _ := http.NewRequest("GET", e.http.URL+"/api/integrations", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, secret := range []string{"s3cret", "t0k3n", "us3r", "pa55", "fr4g"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("the response holds %q:\n%s", secret, body)
		}
	}
	var out struct {
		Webhooks []map[string]any `json:"webhooks"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"url": "https://127.0.0.1:1/hook", "events": []any{"needs_input", "exit_nonzero"}},
		{"url": "http://127.0.0.1:2", "events": []any{"progress"}},
	}
	if !reflect.DeepEqual(out.Webhooks, want) {
		t.Fatalf("webhooks %v, want %v", out.Webhooks, want)
	}

	plain := newTestEnv(t, nil)
	_, list := plain.do("GET", "/api/integrations", adminToken, nil)
	if hooks, ok := list["webhooks"].([]any); !ok || len(hooks) != 0 {
		t.Fatalf("without webhooks: %v", list["webhooks"])
	}
}

// The webhooks share one sink: an entry is typed, its session looked up and
// its body built once, and every webhook that wants it gets the same body.
func TestWebhooksShareOneSinkAndOneBody(t *testing.T) {
	a, b, c := newReceiver(t, nil), newReceiver(t, nil), newReceiver(t, nil)
	e, _ := newWebhookEnv(t,
		config.Webhook{URL: a.URL, Events: []string{"needs_input"}, AllowPrivate: true},
		config.Webhook{URL: b.URL, Events: []string{"attention"}, Secret: "s3cret", AllowPrivate: true},
		config.Webhook{URL: c.URL, Events: []string{"done"}, AllowPrivate: true},
	)
	// One sink for the three webhooks, beside the sinks a server without
	// webhooks has.
	if n := len(e.srv.events.sinks) - len(newTestEnv(t, nil).srv.events.sinks); n != 1 {
		t.Fatalf("%d sinks for three webhooks", n)
	}
	id := e.createSession("cat")
	if resp, out := e.do("POST", "/api/sessions/"+id+"/events", e.agentToken(id), map[string]any{"type": "needs_input", "message": "approve?"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	da, db := a.next(t), b.next(t)
	if da.header.Get("X-Conductor-Event") != "needs_input" || db.header.Get("X-Conductor-Event") != "attention" || !bytes.Equal(da.body, db.body) {
		t.Fatalf("a got %s %s\nb got %s %s", da.header.Get("X-Conductor-Event"), da.body, db.header.Get("X-Conductor-Event"), db.body)
	}
	if db.header.Get("X-Conductor-Signature") != signature("s3cret", db.body) {
		t.Fatal("the shared body is not what b's signature signs")
	}
	c.none(t, 300*time.Millisecond)
}

// A hosted session's host sends the attention state an attention entry
// records with the entry, and the entry may arrive before the attention
// message that changes the session: the webhooks type it by the state it
// carries, not by the session's state before. A state that is not one of the
// three, or that comes with another type of entry, is ignored, and the
// session's state is never used instead: an entry without one is typed only
// by the state it names.
func TestWebhookTypesAHostedAttentionEntryByTheStateItCarries(t *testing.T) {
	rcv := newReceiver(t, nil)
	e, _ := newWebhookEnv(t, config.Webhook{URL: rcv.URL, Events: []string{"needs_input", "working", "attention", "progress"}, AllowPrivate: true})
	host := dialFakeHost(t, e, "hosted-agent-token")
	attention := func(state, message string) {
		host.send(proto.HostAttentionMsg{T: proto.HostAttention, SessionID: host.sessionID, State: state, Message: message, Source: session.SourceAPI})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if d, ok := e.srv.registry.Get(host.sessionID); ok && d.Info().Attention.State == session.AttentionState(state) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the hosted session never showed %s", state)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	entry := func(typ, message, state string) {
		m := hostActivity(typ, func(a *proto.Activity) { a.Message = message })
		m.State = state
		host.send(m)
	}
	check := func(event, message string) {
		t.Helper()
		d := rcv.next(t)
		if got := d.header.Get("X-Conductor-Event"); got != event || d.message(t) != message {
			t.Fatalf("got %s %q, want %s %q", got, d.message(t), event, message)
		}
	}
	attention("working", "compiling")
	// The report that it needs input: its entry first, the change after.
	entry(session.ActivityAttention, "approve?", "needs_input")
	check("needs_input", "approve?")
	attention("needs_input", "approve?")
	// The session needs input; neither entry is typed by that.
	entry(session.ActivityAttention, "Waiting", "clear")
	check("attention", "Waiting")
	entry(session.ActivityAttention, "Waiting", "")
	check("attention", "Waiting")
	entry(session.ActivityProgress, "1/2", "working")
	check("progress", "1/2")
	entry(session.ActivityAttention, "working", "")
	check("working", "working")
	rcv.none(t, 300*time.Millisecond)
}
