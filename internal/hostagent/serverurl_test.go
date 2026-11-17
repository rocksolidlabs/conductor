package hostagent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/notify"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A server is named by its address alone: Run and Uplink.Publish refuse a
// server URL with user information (an @ alone included), a query or a
// fragment (an empty one included) before anything is written or dialed,
// in words that repeat nothing of it.
func TestRunRefusesAServerURLThatCarriesMore(t *testing.T) {
	var dialed atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dialed.Add(1) }))
	t.Cleanup(hs.Close)
	host := strings.TrimPrefix(hs.URL, "http://")
	servers := []string{"http://me:SECRET@" + host, "http://SECRET@" + host, "http://:SECRET@" + host, "http://@" + host, hs.URL + "/?token=SECRET", hs.URL + "?", hs.URL + "/#SECRET", hs.URL + "#"}
	const want = "host: the server URL must carry no user name, password, query or fragment: the host token is a setting of its own"
	for _, server := range servers {
		hooks := t.TempDir()
		_, err := Run(t.Context(), Options{ServerURL: server, Token: hostToken, Argv: []string{"/bin/cat"}, Adapter: "claude", HooksDir: hooks, Log: slog.New(slog.DiscardHandler)})
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v", server, err)
		}
		if entries, _ := os.ReadDir(hooks); len(entries) != 0 {
			t.Errorf("%s: wrote hook assets before refusing: %v", server, entries)
		}
	}

	proc, err := pty.Start(pty.Spec{Argv: []string{"/bin/cat"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	local := session.NewLocal(session.Info{ID: "local-extra", Name: "extra", Kind: session.KindServer, Command: []string{"/bin/cat"}, Cwd: t.TempDir(), Status: session.StatusRunning, Cols: 80, Rows: 24}, proc, session.Options{Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = local.Stop(context.Background()) })
	for _, server := range servers {
		u := &Uplink{ServerURL: server, Token: hostToken, Log: slog.New(slog.DiscardHandler)}
		if _, err := u.Publish(t.Context(), local); err == nil || err.Error() != want {
			t.Errorf("publish to %s: %v", server, err)
		}
	}
	if n := dialed.Load(); n != 0 {
		t.Fatalf("the server was asked %d times", n)
	}
}

// controlURL refuses in words of its own, naming nothing of the URL; a path
// the server serves under is kept.
func TestControlURL(t *testing.T) {
	for server, want := range map[string]string{
		"http://[::1%SECRET":             "host: the server URL cannot be read, or names no host",
		"SECRET.example":                 "host: the server URL cannot be read, or names no host",
		"ftp://SECRET.example":           "host: the server URL must be http, https, ws or wss",
		"https://me:SECRET@yard.example": "host: the server URL must carry no user name, password, query or fragment: the host token is a setting of its own",
	} {
		if _, err := controlURL(server); err == nil || err.Error() != want {
			t.Errorf("%s: %v", server, err)
		}
	}
	for server, want := range map[string]string{
		"http://localhost:8080":         "ws://localhost:8080/ws/host",
		"https://yard.example/prefix/":  "wss://yard.example/prefix/ws/host",
		"wss://yard.example":            "wss://yard.example/ws/host",
		"https://yard.example/team@two": "wss://yard.example/team@two/ws/host",
	} {
		if got, err := controlURL(server); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", server, got, err, want)
		}
	}
}

// The URL an agent reports its attention to is the server's host and path,
// then the session's route, whatever else the server URL held: over http
// for an http or ws server, over https for an https or wss one.
func TestAttentionURL(t *testing.T) {
	for server, want := range map[string]string{
		"https://yard.example":                              "https://yard.example/api/sessions/s1/attention",
		"https://yard.example/prefix/":                      "https://yard.example/prefix/api/sessions/s1/attention",
		"http://127.0.0.1:8080":                             "http://127.0.0.1:8080/api/sessions/s1/attention",
		"https://me:SECRET@yard.example/p?t=SECRET#SECRET":  "https://yard.example/p/api/sessions/s1/attention",
		"https://yard.example/a%2Fb/?token=SECRET#fragment": "https://yard.example/a%2Fb/api/sessions/s1/attention",
		"ws://127.0.0.1:8080":                               "http://127.0.0.1:8080/api/sessions/s1/attention",
		"wss://yard.example/prefix/":                        "https://yard.example/prefix/api/sessions/s1/attention",
		"WSS://yard.example":                                "https://yard.example/api/sessions/s1/attention",
		"ws://me:SECRET@yard.example/p?t=SECRET#SECRET":     "http://yard.example/p/api/sessions/s1/attention",
	} {
		if got := attentionURL(server, "s1"); got != want {
			t.Errorf("%s: %q, want %q", server, got, want)
		}
	}
}

// The process the host starts finds the attention URL in its environment
// (CONDUCTOR_NOTIFY_URL): the server's address, its path kept, and the
// session's route, over http for a server named http:// or ws:// alike,
// so that a hook's report with it and the session's token reaches the
// server.
func TestTheProcessIsToldTheServersAddressToReportTo(t *testing.T) {
	srv, _ := startServer(t)
	hs := httptest.NewServer(http.StripPrefix("/yard", srv.Handler()))
	t.Cleanup(hs.Close)
	for _, server := range []string{hs.URL + "/yard/", "ws://" + strings.TrimPrefix(hs.URL, "http://") + "/yard/"} {
		scheme, _, _ := strings.Cut(server, ":")
		t.Run(scheme, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "notify")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			registered := make(chan string, 1)
			done := make(chan error, 1)
			go func() {
				_, err := Run(ctx, Options{
					ServerURL:  server,
					Token:      hostToken,
					Argv:       []string{"/bin/sh", "-c", `printf '%s\n%s\n' "$CONDUCTOR_NOTIFY_URL" "$CONDUCTOR_NOTIFY_TOKEN" > "$0"; exec /bin/cat`, out},
					RelayOnly:  true,
					Log:        slog.New(slog.DiscardHandler),
					Registered: func(id, _ string) { registered <- id },
				})
				done <- err
			}()
			var id string
			select {
			case id = <-registered:
			case err := <-done:
				t.Fatalf("run returned before registering: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("the host did not register")
			}
			want := hs.URL + "/yard/api/sessions/" + id + "/attention"
			var got []byte
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if got, _ = os.ReadFile(out); strings.Count(string(got), "\n") == 2 {
					break
				}
			}
			notifyURL, token, _ := strings.Cut(strings.TrimSuffix(string(got), "\n"), "\n")
			if notifyURL != want {
				t.Fatalf("CONDUCTOR_NOTIFY_URL %q, want %q", notifyURL, want)
			}
			if err := notify.Send(ctx, notifyURL, token, notify.Request{State: "needs_input", Message: "from the hook"}); err != nil {
				t.Fatalf("the report: %v", err)
			}
			d, ok := srv.Registry().Get(id)
			if !ok {
				t.Fatal("the hosted session is not listed")
			}
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if att := d.Info().Attention; att.State == session.AttentionNeedsInput && att.Message == "from the hook" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the server's attention %+v, want the report", d.Info().Attention)
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("run did not end")
			}
		})
	}
}

// A dial that fails is said in this package's words: the host it was
// dialing and, as far as the error's kind tells, why; never the error's own
// text, which can quote a redirect's URL, a Location the client could not
// read, or what the server sent (a handshake's headers, a certificate's
// names).
func TestAFailedDialNamesItsHostAlone(t *testing.T) {
	// A port nobody answers on. The listener keeps it for the test's length
	// and accepts nothing, so that the kernel hands it to no other process,
	// and the transport's dial below answers for it as the kernel answers a
	// port with no listener. (A listener closed so that the port is refused
	// gives the port back: now and then it was another test's server by the
	// time of the dial, which answered it.)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	closed := ln.Addr().String()
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/nowhere"):
			http.Redirect(w, r, "http://nowhere.invalid:1/P-SECRET/ws/host?token=Q-SECRET", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/closed"):
			http.Redirect(w, r, "http://"+closed+"/P-SECRET/ws/host?token=Q-SECRET", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/malformed"):
			w.Header().Set("Location", "http://[SECRET/ws/host")
			w.WriteHeader(http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/ok"):
			w.Header().Set("X-Said", "SECRET")
			fmt.Fprint(w, "SECRET")
		case strings.HasPrefix(r.URL.Path, "/upgrade"):
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: SECRET\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: SECRET\r\n\r\n")
			buf.Flush()
		}
	}))
	t.Cleanup(away.Close)
	awayHost := strings.TrimPrefix(away.URL, "http://")
	// A name that does not resolve fails as the resolver fails it, without a
	// lookup leaving the machine.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); host == "nowhere.invalid" {
			return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host SECRET", Name: host, IsNotFound: true}}
		}
		if addr == closed {
			return nil, &net.OpError{Op: "dial", Net: network, Addr: ln.Addr(), Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}

	// TLS: a server whose certificate this machine does not trust, one whose
	// certificate names another host (SECRET.example), one whose certificate
	// has expired, and a plain server dialed as TLS.
	untrusted := httptest.NewUnstartedServer(http.NotFoundHandler())
	untrusted.Config.ErrorLog = log.New(io.Discard, "", 0)
	untrusted.StartTLS()
	t.Cleanup(untrusted.Close)
	named, namedRoots := tlsServer(t, time.Now().Add(time.Hour), "SECRET.example")
	expired, expiredRoots := tlsServer(t, time.Now().Add(-time.Minute), "127.0.0.1")
	withRoots := func(roots *x509.CertPool) *http.Transport {
		c := tr.Clone()
		c.TLSClientConfig = &tls.Config{RootCAs: roots}
		return c
	}
	// A proxy, with a password in its URL, that answers the CONNECT with a
	// status and a reason phrase of its own.
	viaProxy := func(status string) *http.Transport {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					if _, err := http.ReadRequest(bufio.NewReader(c)); err == nil {
						io.WriteString(c, "HTTP/1.1 "+status+"\r\nContent-Length: 0\r\n\r\n")
					}
				}()
			}
		}()
		c := tr.Clone()
		c.Proxy = http.ProxyURL(&url.URL{Scheme: "http", User: url.UserPassword("me", "PROXY-SECRET"), Host: ln.Addr().String()})
		return c
	}

	for _, tc := range []struct {
		name      string
		server    string
		transport *http.Transport
		want      string
	}{
		{"a name that does not resolve", away.URL + "/nowhere", tr, "host: the control connection to nowhere.invalid:1 failed: its name does not resolve"},
		{"a refused connection", away.URL + "/closed", tr, "host: the control connection to " + closed + " failed: the connection was refused"},
		{"a Location that cannot be read", away.URL + "/malformed", tr, "host: the control connection to " + awayHost + " failed"},
		{"an answer that is not an upgrade", away.URL + "/ok", tr, "host: the control connection to " + awayHost + " failed: it answered HTTP 200, not a WebSocket upgrade"},
		{"an upgrade it cannot use", away.URL + "/upgrade", tr, "host: the control connection to " + awayHost + " failed: its WebSocket handshake is not one this host accepts"},
		{"an authority not trusted", untrusted.URL, withRoots(x509.NewCertPool()), "host: the control connection to " + strings.TrimPrefix(untrusted.URL, "https://") + " failed: its certificate is signed by an authority this machine does not trust"},
		{"a certificate for another name", named.URL, withRoots(namedRoots), "host: the control connection to " + strings.TrimPrefix(named.URL, "https://") + " failed: its certificate is for another name"},
		{"a certificate expired", expired.URL, withRoots(expiredRoots), "host: the control connection to " + strings.TrimPrefix(expired.URL, "https://") + " failed: its certificate has expired, or is not valid yet"},
		{"no TLS", strings.Replace(away.URL, "http://", "https://", 1), tr, "host: the control connection to " + awayHost + " failed: it did not answer in TLS"},
		{"a proxy that asks for authentication", "https://yard.example", viaProxy("407 SECRET reason"), "host: the control connection to yard.example failed: the proxy the environment names (HTTPS_PROXY) asks for authentication"},
		{"a proxy that refuses", "https://yard.example", viaProxy("403 SECRET reason"), "host: the control connection to yard.example failed: the proxy the environment names (HTTPS_PROXY) refused it with HTTP 403"},
	} {
		wsURL, err := controlURL(tc.server)
		if err != nil {
			t.Fatal(err)
		}
		a := &agent{opts: Options{Insecure: true, transport: tc.transport}, wsURL: wsURL, peers: map[string]*peer{}, log: slog.New(slog.DiscardHandler)}
		_, _, err = a.dialAndRegister(context.Background())
		if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: %v\nwant %s", tc.name, err, tc.want)
		}
	}
}

// tlsServer serves TLS with a certificate of its own for names, valid until
// notAfter, and gives the pool that trusts it.
func tlsServer(t *testing.T, notAfter time.Time, names ...string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             notAfter.Add(-2 * time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}}
	// The handshakes these tests fail on purpose are not the server's news.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return srv, roots
}

// failureCause words each kind of failure its own way, and one it does not
// know not at all: never with the error's text.
func TestFailureCause(t *testing.T) {
	secret := errors.New("SECRET")
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.Canceled, "stopped"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), "timed out"},
		{&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, "timed out"},
		{&offMachine{"192.0.2.1:9"}, "its address 192.0.2.1:9 is not this machine"},
		{&net.OpError{Op: "dial", Err: &net.DNSError{Err: "SECRET", Name: "SECRET.example", IsNotFound: true}}, "its name does not resolve"},
		{&net.OpError{Op: "dial", Err: &net.DNSError{Err: "SECRET", Name: "SECRET.example", IsTimeout: true}}, "looking up its name timed out"},
		{&net.OpError{Op: "proxyconnect", Err: secret}, "the proxy the environment names (HTTPS_PROXY, HTTP_PROXY) did not connect it"},
		{&proxyRefused{http.StatusProxyAuthRequired}, "the proxy the environment names (HTTPS_PROXY) asks for authentication"},
		{&proxyRefused{http.StatusBadGateway}, "the proxy the environment names (HTTPS_PROXY) refused it with HTTP 502"},
		{&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "its certificate is signed by an authority this machine does not trust"},
		{&tls.CertificateVerificationError{Err: x509.HostnameError{Host: "SECRET"}}, "its certificate is for another name"},
		{&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired, Detail: "SECRET"}}, "its certificate has expired, or is not valid yet"},
		{&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.CANotAuthorizedForThisName, Detail: "https://SECRET"}}, "its certificate is not valid"},
		{tls.RecordHeaderError{Msg: "SECRET"}, "it did not answer in TLS"},
		{http.ErrSchemeMismatch, "it did not answer in TLS"},
		{&net.OpError{Op: "remote error", Err: secret}, "it ended the TLS handshake"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, "the connection was refused"},
		{&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, "the connection was reset"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, "it cannot be reached from this machine"},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, "it cannot be reached from this machine"},
		{io.EOF, "the connection closed before it answered"},
		{fmt.Errorf("SECRET: %w", io.ErrUnexpectedEOF), "the connection closed before it answered"},
		{timeoutError{}, "timed out"},
		{errors.New(`failed to parse Location header "http://[SECRET": missing ']' in host`), ""},
	} {
		if got := failureCause(tc.err); got != tc.want {
			t.Errorf("%T %v: %q, want %q", tc.err, tc.err, got, tc.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "SECRET" }
func (timeoutError) Timeout() bool { return true }
