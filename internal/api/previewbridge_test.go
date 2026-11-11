package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/config"
)

// The preview bridge's route (docs/protocol.md, Preview bridge).

// fakeBridge is a built bridge as the web build leaves it: its origins' marker once.
const fakeBridge = "(function(){\n\tvar ORIGINS = \"__CONDUCTOR_WORKBENCH_ORIGINS__\";\n\tstart(ORIGINS);\n})();\n"

func bridgeEnv(t *testing.T, mutate func(*config.Config), opts ...Option) *testEnv {
	t.Helper()
	return newTestEnvAgents(t, mutate, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, opts...)
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

// originsIn is the origins the served bridge holds in the marker's place.
func originsIn(t *testing.T, body string) []string {
	t.Helper()
	const pre = "var ORIGINS = "
	i := strings.Index(body, pre)
	j := strings.Index(body, ";\n\tstart")
	if i < 0 || j < i {
		t.Fatalf("no origins in %q", body)
	}
	var out []string
	if err := json.Unmarshal([]byte(body[i+len(pre):j]), &out); err != nil {
		t.Fatalf("the origins are no JSON array: %v", err)
	}
	return out
}

// The built bridge (a fake embed) is served to anyone, no credential, with
// its headers: a script of no cache any page may load (a dev server that
// sets COEP too), its marker replaced by the origins, nothing else of it
// changed.
func TestTheBridgeIsServedWithItsHeaders(t *testing.T) {
	for _, script := range []string{fakeBridge} {
		e := bridgeEnv(t, nil, WithPreviewBridge([]byte(script)))
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
		if strings.Contains(body, "__CONDUCTOR_WORKBENCH_ORIGINS__") || !strings.HasPrefix(body, "(function(){\n\tvar ORIGINS = [") || !strings.HasSuffix(body, "];\n\tstart(ORIGINS);\n})();\n") {
			t.Fatalf("the served bridge: %q", body)
		}
		if got := originsIn(t, body); len(got) == 0 {
			t.Fatal("no origins")
		}
	}
}

// The origins are every one the server answers the workbench on, written
// as a browser writes an origin (scheme and host in lower case, the
// scheme's default port left out, an IPv6 host in brackets): each
// listener's at the port it is bound to (one on a loopback address that
// address and the loopback's three names, one on every address the three
// names, one on another address that address alone), and publicUrl's;
// each once, each within its bound. The engine is given the bridge's URL
// on the loopback when the listener is there (or on every address), none
// when it is not.
func TestTheBridgeNamesTheServersOrigins(t *testing.T) {
	loop := func(scheme, port string) []string {
		return []string{scheme + "://127.0.0.1" + port, scheme + "://localhost" + port, scheme + "://[::1]" + port}
	}
	for _, tc := range []struct {
		name, listen, public string
		tls                  string // the TLS listener's bound address, "" for none
		want                 []string
		engine               string
	}{
		{"every address", "0.0.0.0:8080", "http://127.0.0.1:8080", "", loop("http", ":8080"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a LAN address and TLS on the loopback", "192.168.1.5:8080", "https://conductor.example:9443/base/", "127.0.0.1:41443",
			append(append([]string{"http://192.168.1.5:8080"}, loop("https", ":41443")...), "https://conductor.example:9443"), ""},
		{"TLS on every address", "127.0.0.1:8080", "http://localhost:8080", "[::]:8443", append(loop("http", ":8080"), loop("https", ":8443")...), "http://127.0.0.1:8080/preview-bridge.js"},
		{"TLS on a LAN address", "127.0.0.1:8080", "http://localhost:8080", "10.0.0.7:8443", append(loop("http", ":8080"), "https://10.0.0.7:8443"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"the IPv6 loopback", "[::1]:7000", "http://[::1]:7000/", "", []string{"http://[::1]:7000", "http://127.0.0.1:7000", "http://localhost:7000"}, "http://[::1]:7000/preview-bridge.js"},
		{"another loopback address", "127.0.0.2:8080", "http://127.0.0.2:8080/", "", append([]string{"http://127.0.0.2:8080"}, loop("http", ":8080")...), "http://127.0.0.2:8080/preview-bridge.js"},
		{"the default ports", "127.0.0.1:80", "http://127.0.0.1/", "0.0.0.0:443", append(loop("http", ""), loop("https", "")...), "http://127.0.0.1:80/preview-bridge.js"},
		{"a public URL in capitals with its default port", "127.0.0.1:8080", "HTTPS://Conductor.EXAMPLE:443/", "", append(loop("http", ":8080"), "https://conductor.example"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a plain public URL with its default port", "127.0.0.1:8080", "http://Conductor.Example:80", "", append(loop("http", ":8080"), "http://conductor.example"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public IPv6 address", "127.0.0.1:8080", "https://[2001:DB8::1]:443/", "", append(loop("http", ":8080"), "https://[2001:db8::1]"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public URL past its bound", "127.0.0.1:8080", "https://" + strings.Repeat("a", 260) + ".example", "", loop("http", ":8080"), "http://127.0.0.1:8080/preview-bridge.js"},
		{"a public URL with a user", "127.0.0.1:8080", "https://me:pw@conductor.example", "", loop("http", ":8080"), "http://127.0.0.1:8080/preview-bridge.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = tc.public }, WithPreviewBridge([]byte(fakeBridge)))
			if tc.tls != "" {
				e.srv.cfg.TLS.Listen, e.srv.cfg.TLS.CertFile, e.srv.cfg.TLS.KeyFile = ":0", "cert.pem", "key.pem"
				e.srv.SetTLSListen(tc.tls)
			}
			e.srv.SetListen(tc.listen)
			_, body, _ := getBridge(t, e)
			got := originsIn(t, body)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("origins %q, want %q", got, tc.want)
			}
			if len(got) > maxBridgeOrigins {
				t.Fatalf("%d origins", len(got))
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
		})
	}
}

// A server whose embedded app has no bridge (a build without make
// web-build, or make dev's) answers 404 no_bridge and names no bridge to a
// tree; so does one handed a build whose marker is not there once, and a
// switchyard, which frames no preview.
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
		{"no marker", nil, []Option{WithPreviewBridge([]byte("(function(){})()"))}},
		{"the marker twice", nil, []Option{WithPreviewBridge([]byte(fakeBridge + fakeBridge))}},
		{"the marker in single quotes", nil, []Option{WithPreviewBridge([]byte(strings.ReplaceAll(fakeBridge, `"__CONDUCTOR_WORKBENCH_ORIGINS__"`, `'__CONDUCTOR_WORKBENCH_ORIGINS__'`)))}},
		{"the marker between a double and a single quote", nil, []Option{WithPreviewBridge([]byte(strings.ReplaceAll(fakeBridge, `"__CONDUCTOR_WORKBENCH_ORIGINS__"`, `"__CONDUCTOR_WORKBENCH_ORIGINS__'`)))}},
		{"the marker between a single and a double quote", nil, []Option{WithPreviewBridge([]byte(strings.ReplaceAll(fakeBridge, `"__CONDUCTOR_WORKBENCH_ORIGINS__"`, `'__CONDUCTOR_WORKBENCH_ORIGINS__"`)))}},
		{"the marker's name in a comment as well", nil, []Option{WithPreviewBridge([]byte(fakeBridge + "// __CONDUCTOR_WORKBENCH_ORIGINS__\n"))}},
		{"the marker's name in another literal as well", nil, []Option{WithPreviewBridge([]byte(fakeBridge + "var x = 'see __CONDUCTOR_WORKBENCH_ORIGINS__';\n"))}},
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

// What replaces the marker is data: origins holding quotes of every kind,
// a backslash, <, </script>, U+2028, U+2029, a line break and ${ are
// written as one array of strings that reads back as they were, with none
// of those characters raw in it, so no origin can end the string, the
// line, a comment or the script around the marker. A publicUrl holding
// those a URL may hold is served the same way.
func TestTheBridgesOriginsAreData(t *testing.T) {
	hostile := []string{`https://a"b`, `https://a'b`, "https://a`b", `https://a\b`, `https://a</script><script>x()</script>`, "https://a b c", "https://a\nb\r", "https://a${x}b", "https://a*/b//c"}
	text := originsText(hostile)
	var back []string
	if err := json.Unmarshal(text, &back); err != nil || !slices.Equal(back, hostile) {
		t.Fatalf("%s reads back as %q (%v)", text, back, err)
	}
	for _, raw := range []string{"<", ">", "'", "`", "$", " ", " ", "\n", "\r"} {
		if strings.Contains(string(text), raw) {
			t.Errorf("%q is raw in %s", raw, text)
		}
	}
	for _, public := range []string{`https://a"b</script>`, `https://x'+(pwned=1)+'.example`, "https://a b.example"} {
		e := bridgeEnv(t, func(c *config.Config) { c.PublicURL = public }, WithPreviewBridge([]byte(fakeBridge)))
		e.srv.SetListen("127.0.0.1:8080")
		_, body, _ := getBridge(t, e)
		got := originsIn(t, body)
		if last := got[len(got)-1]; !strings.HasPrefix(public, last) {
			t.Fatalf("publicUrl %q is served as %q", public, last)
		}
		for _, raw := range []string{"<", "'", " "} {
			if strings.Contains(body, raw) {
				t.Errorf("publicUrl %q: %q is raw in %s", public, raw, body)
			}
		}
	}
}
