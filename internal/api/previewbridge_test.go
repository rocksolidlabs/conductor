package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// The preview bridge's route (docs/protocol.md, Preview bridge).

// fakeBridge is a built bridge as the web build leaves it: a script that
// reads its origins from the wrapper's parameter.
const fakeBridge = "start(conductorWorkbenchOrigins);\n"

func bridgeEnv(t *testing.T, mutate func(*config.Config), opts ...Option) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, opts...)
}

// fakeMachine has the machine's addresses be addrs for the test.
func fakeMachine(t *testing.T, addrs ...string) {
	t.Helper()
	old := machineAddrs
	t.Cleanup(func() { machineAddrs = old })
	machineAddrs = func() ([]net.Addr, error) {
		var out []net.Addr
		for _, a := range addrs {
			_, n, err := net.ParseCIDR(a)
			if err != nil {
				t.Fatal(err)
			}
			n.IP, _, _ = net.ParseCIDR(a)
			out = append(out, n)
		}
		return out, nil
	}
}

// getBridge is GET /preview-bridge.js with no credential: its status, its
// body and its headers.
func getBridge(t *testing.T, e *testEnv) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(e.http.URL + "/preview-bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b), resp.Header
}

// originsIn is the origins the wrapper around script calls it with in body,
// which must be the wrapper's head, script byte for byte and its tail.
func originsIn(t *testing.T, body, script string) []string {
	t.Helper()
	rest, ok := strings.CutPrefix(body, bridgeHead+script+bridgeTail)
	data, end := strings.CutSuffix(rest, bridgeEnd)
	if !ok || !end {
		t.Fatalf("not the script in its wrapper: %q", body)
	}
	var out []string
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		t.Fatalf("the origins are no JSON array: %v", err)
	}
	return out
}

// The built bridge (a fake embed) is served to anyone, no credential, with
// its headers: a script of no cache any page may load (a dev server that
// sets COEP too), whole and unchanged inside its wrapper.
func TestTheBridgeIsServedWithItsHeaders(t *testing.T) {
	e := bridgeEnv(t, nil, WithPreviewBridge([]byte(fakeBridge)))
	e.srv.SetListen("127.0.0.1:8080")
	code, body, h := getBridge(t, e)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	for k, want := range map[string]string{
		"Content-Type":                 "text/javascript; charset=utf-8",
		"Cache-Control":                "no-cache",
		"Cross-Origin-Resource-Policy": "cross-origin",
		"X-Content-Type-Options":       "nosniff",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if got := originsIn(t, body, fakeBridge); len(got) == 0 {
		t.Fatal("no origins")
	}
}

// The origins are every one the server answers the workbench on, written
// as a browser writes an origin: each listener's at the port it is bound
// to (one on a loopback address that address and the loopback's three
// names, one on every address the three names and the machine's addresses
// as they are when the script is served, one on another address that
// address alone), and publicUrl's only when it is already written as a
// browser writes it and names another machine. Each once, each within its
// bound. The engine is given the bridge's URL on the loopback when the
// listener is there (or on every address), none when it is not.
func TestTheBridgeNamesTheServersOrigins(t *testing.T) {
	fakeMachine(t, "127.0.0.1/8", "192.168.1.5/24", "fd00::5/64", "fe80::1/64", "::1/128")
	loop := func(scheme, port string) []string {
		return []string{scheme + "://127.0.0.1" + port, scheme + "://localhost" + port, scheme + "://[::1]" + port}
	}
	machine := func(scheme, port string) []string {
		return []string{scheme + "://192.168.1.5" + port, scheme + "://[fd00::5]" + port}
	}
	cat := func(parts ...[]string) []string { return slices.Concat(parts...) }
	const lp = "127.0.0.1:8080"
	for _, tc := range []struct {
		name, listen, public string
		tls                  string // the TLS listener's bound address, "" for none
		want                 []string
		engine               string
	}{
		{"every address", "0.0.0.0:8080", "http://127.0.0.1:8080", "", cat(loop("http", ":8080"), machine("http", ":8080")), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a LAN address and TLS on the loopback", "192.168.1.5:8080", "https://conductor.example:9443/base/", "127.0.0.1:41443",
			cat([]string{"http://192.168.1.5:8080"}, loop("https", ":41443"), []string{"https://conductor.example:9443"}), ""},
		{"a LAN address and a local public URL", "192.168.1.5:8080", "http://localhost:8080", "", []string{"http://192.168.1.5:8080"}, ""},
		{"a LAN address and a public URL on the loopback", "192.168.1.5:8080", "http://127.0.0.2:8080", "", []string{"http://192.168.1.5:8080"}, ""},
		{"TLS on every address", lp, "http://localhost:8080", "[::]:8443", cat(loop("http", ":8080"), loop("https", ":8443"), machine("https", ":8443")), "http://127.0.0.1:8080/preview-bridge.js"},
		{"TLS on a LAN address", lp, "http://localhost:8080", "10.0.0.7:8443", append(loop("http", ":8080"), "https://10.0.0.7:8443"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"the IPv6 loopback", "[::1]:7000", "http://[::1]:7000/", "", []string{"http://[::1]:7000", "http://127.0.0.1:7000", "http://localhost:7000"}, "http://[::1]:7000/preview-bridge.js"},
		{"another loopback address", "127.0.0.2:8080", "http://127.0.0.2:8080/", "", append([]string{"http://127.0.0.2:8080"}, loop("http", ":8080")...), "http://127.0.0.2:8080/preview-bridge.js"},
		{"the default ports", "127.0.0.1:80", "http://127.0.0.1/", "0.0.0.0:443", cat(loop("http", ""), loop("https", ""), machine("https", "")), "http://127.0.0.1:80/preview-bridge.js"},
		{"a public name", lp, "https://conductor.example/x?y", "", append(loop("http", ":8080"), "https://conductor.example"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public name in its xn-- form", lp, "https://xn--bcher-kva.example", "", append(loop("http", ":8080"), "https://xn--bcher-kva.example"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public IPv4 address", lp, "http://10.0.0.7:9000", "", append(loop("http", ":8080"), "http://10.0.0.7:9000"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public IPv6 address", lp, "https://[2001:db8::1]/", "", append(loop("http", ":8080"), "https://[2001:db8::1]"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public URL past its bound", lp, "https://" + strings.Repeat("a", 260) + ".example", "", loop("http", ":8080"), "http://127.0.0.1:8080/preview-bridge.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = tc.public }, WithPreviewBridge([]byte(fakeBridge)))
			if tc.tls != "" {
				e.srv.cfg.TLS.Listen, e.srv.cfg.TLS.CertFile, e.srv.cfg.TLS.KeyFile = ":0", "cert.pem", "key.pem"
				e.srv.SetTLSListen(tc.tls)
			}
			e.srv.SetListen(tc.listen)
			_, body, _ := getBridge(t, e)
			got := originsIn(t, body, fakeBridge)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("origins %q, want %q", got, tc.want)
			}
			for _, o := range got {
				u, err := url.Parse(o)
				if err != nil || len(o) > maxBridgeOrigin || u.Path != "" || u.User != nil {
					t.Fatalf("an origin %q", o)
				}
			}
			if b := e.srv.runs.PreviewBridge(); b != tc.engine {
				t.Fatalf("the engine's bridge %q, want %q", b, tc.engine)
			}
			if e.srv.PublicOriginLeftOut() {
				t.Fatal("publicUrl is said to be left out")
			}
		})
	}
}

// A public URL not already written as a browser writes its origin is left
// out of the origins (it fails closed), and the server says so.
func TestAPublicURLNotInABrowsersFormIsLeftOut(t *testing.T) {
	for _, public := range []string{
		"HTTPS://conductor.example", "https://Conductor.example", "https://conductor.example:443/", "http://conductor.example:80",
		"https://conductor.example:0443", "https://conductor.example:08443", "https://conductor.example:", "https://bücher.example",
		"https://127.1", "https://0x7f.0.0.1:8443", "https://2130706433", "https://10.0.0.007", "https://[2001:DB8::1]",
		"https://[::ffff:10.0.0.7]:9443", "https://[::ffff:a00:7]:9443", "https://[fe80::1%25en0]:8080", "https://conductor.example.",
		"https://conductor.example.123", "https://a_b.example", "https://me:pw@conductor.example",
		`https://a"b</script>`, `https://x'+(pwned=1)+'.example`,
	} {
		t.Run(public, func(t *testing.T) {
			e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = public }, WithPreviewBridge([]byte(fakeBridge)))
			e.srv.SetListen("127.0.0.1:8080")
			_, body, _ := getBridge(t, e)
			if got := originsIn(t, body, fakeBridge); len(got) != 3 {
				t.Fatalf("origins %q", got)
			}
			if !e.srv.PublicOriginLeftOut() {
				t.Fatal("not said to be left out")
			}
		})
	}
	// No bridge, nothing to say.
	e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = "HTTPS://conductor.example" })
	if e.srv.PublicOriginLeftOut() {
		t.Fatal("said with no bridge")
	}
}

// A listener on every address names the machine's addresses as they are
// when the script is served, never older.
func TestTheBridgesAddressesAreTheMachinesNow(t *testing.T) {
	fakeMachine(t, "192.168.1.5/24")
	e := bridgeEnv(t, nil, WithPreviewBridge([]byte(fakeBridge)))
	e.srv.SetListen("[::]:8080")
	_, body, _ := getBridge(t, e)
	if got := originsIn(t, body, fakeBridge); !slices.Contains(got, "http://192.168.1.5:8080") {
		t.Fatalf("origins %q", got)
	}
	fakeMachine(t, "10.1.2.3/8", "2001:db8::7/64")
	_, body, _ = getBridge(t, e)
	got := originsIn(t, body, fakeBridge)
	if slices.Contains(got, "http://192.168.1.5:8080") || !slices.Contains(got, "http://10.1.2.3:8080") || !slices.Contains(got, "http://[2001:db8::7]:8080") {
		t.Fatalf("origins %q", got)
	}
}

// A server whose embedded app has no bridge (a build without make
// web-build, or make dev's) answers 404 no_bridge and names no bridge to a
// tree; so does a switchyard, which frames no preview.
func TestNoBridgeInTheEmbedIsA404AndNoName(t *testing.T) {
	sy := func(c *config.Config) {
		relay := true
		c.Switchyard.Enabled = true
		c.Switchyard.Relay = &relay
	}
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		opts   []Option
	}{
		{"none", nil, nil},
		{"empty", nil, []Option{WithPreviewBridge(nil)}},
		{"a switchyard", sy, []Option{WithPreviewBridge([]byte(fakeBridge))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bridgeEnv(t, tc.mutate, tc.opts...)
			e.srv.SetListen("127.0.0.1:8080")
			code, body, h := getBridge(t, e)
			var got struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal([]byte(body), &got)
			if code != http.StatusNotFound || got.Error.Code != "no_bridge" || !strings.Contains(got.Error.Message, "make web-build") || strings.HasPrefix(h.Get("Content-Type"), "text/javascript") {
				t.Fatalf("%d %s", code, body)
			}
			if b := e.srv.runs.PreviewBridge(); b != "" {
				t.Fatalf("the engine was given %q", b)
			}
		})
	}
}

// bridgeServedFixture is the scripts the route wraps, written by
// TestTheBridgeIsServedWhole (CONDUCTOR_WRITE_FIXTURES=1) and run by
// web/app/utils/protocol.test.ts: each served as the server serves it,
// with origins holding what could end a string, a line, a comment or a
// script.
const bridgeServedFixture = "testdata/preview-bridge-served.json"

type servedCase struct {
	Name   string `json:"name"`
	Script string `json:"script"`
	Served string `json:"served"`
}

type servedFile struct {
	Comment string       `json:"comment"`
	Origins []string     `json:"origins"`
	Cases   []servedCase `json:"cases"`
}

// The route serves the built script's bytes unchanged: the wrapper's head,
// the script byte for byte, the wrapper's tail with the origins as one
// JSON array of strings (none of the characters that could end a string,
// a line, a comment or a script raw in it), whatever the script holds. The
// vitest runs each served script and the script itself, and finds them
// the same, with the origins read back as configured.
func TestTheBridgeIsServedWhole(t *testing.T) {
	hostile := []string{`https://a"b`, `https://a'b`, "https://a`b", `https://a\b`, `https://a</script><script>x()</script>`,
		"https://a\xe2\x80\xa8b\xe2\x80\xa9c", "https://a\nb\r", "https://a\x00b", "https://a${x}b", "https://a*/b//c"}
	text := originsText(hostile)
	var back []string
	if err := json.Unmarshal(text, &back); err != nil || !slices.Equal(back, hostile) {
		t.Fatalf("%s reads back as %q (%v)", text, back, err)
	}
	for _, raw := range []string{"<", ">", "'", "`", "$", "\xe2\x80\xa8", "\xe2\x80\xa9", "\n", "\r", "\x00"} {
		if strings.Contains(string(text), raw) {
			t.Errorf("%q is raw in %s", raw, text)
		}
	}
	scripts := []struct{ name, script string }{
		{"a script that reads its origins", "report(conductorWorkbenchOrigins);\n"},
		{"a script ending in a line comment", "report(conductorWorkbenchOrigins.length, conductorWorkbenchOrigins); // the end"},
		{"a script beginning with a directive", "\"use strict\";\nreport((function () { return typeof this })(), conductorWorkbenchOrigins[0]);\n"},
		{"a script holding the marker's old text in a string, a comment and a regular expression",
			"var s = \"__CONDUCTOR_WORKBENCH_ORIGINS__\"; /* \"__CONDUCTOR_WORKBENCH_ORIGINS__\" */\nvar r = /\"__CONDUCTOR_WORKBENCH_ORIGINS__\"/;\n" +
				"report(s, r.source, conductorWorkbenchOrigins); // '__CONDUCTOR_WORKBENCH_ORIGINS__'\n"},
	}
	f := servedFile{Comment: "Written by internal/api's TestTheBridgeIsServedWhole (CONDUCTOR_WRITE_FIXTURES=1) and run by web/app/utils/protocol.test.ts: each script as GET /preview-bridge.js serves it, wrapped with origins.",
		Origins: hostile}
	for _, s := range scripts {
		served := string(wrapBridge([]byte(s.script), hostile))
		if !slices.Equal(originsIn(t, served, s.script), hostile) {
			t.Fatalf("%s: the origins do not read back", s.name)
		}
		f.Cases = append(f.Cases, servedCase{s.name, s.script, served})
		// The route serves the same bytes for the server's own origins.
		e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = "http://localhost:8080" }, WithPreviewBridge([]byte(s.script)))
		e.srv.SetListen("127.0.0.1:8080")
		if code, body, _ := getBridge(t, e); code != http.StatusOK || body != string(wrapBridge([]byte(s.script), []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"})) {
			t.Fatalf("%s: served %d %q", s.name, code, body)
		}
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CONDUCTOR_WRITE_FIXTURES") == "1" {
		if err := os.WriteFile(bridgeServedFixture, want.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(bridgeServedFixture)
	if err != nil || !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("%s is not what the cases build (%v): write it again with CONDUCTOR_WRITE_FIXTURES=1", bridgeServedFixture, err)
	}
}
