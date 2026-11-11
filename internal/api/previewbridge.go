package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The preview bridge (docs/protocol.md, Preview bridge): the script a
// previewed page loads, or the desktop shell puts into it, to read the page
// for the workbench that frames it. The web build emits it with one marker
// (PV1 PR-2b; until it does, the embed has none and the route answers
// 404 no_bridge); GET /preview-bridge.js serves it with the server's
// own workbench origins written in its place, so the bridge answers a
// parent window at those origins alone. The route is unauthenticated (a
// script tag carries no header) and holds no secret.

// The bridge's origins: at most maxBridgeOrigins, each at most
// maxBridgeOrigin bytes.
const (
	maxBridgeOrigins = 16
	maxBridgeOrigin  = 256
)

// bridgeMarkerName is the name the web build leaves for the origins, as
// one double-quoted string literal: bridgeMarker.
const bridgeMarkerName = `__CONDUCTOR_WORKBENCH_ORIGINS__`

var bridgeMarker = []byte(`"` + bridgeMarkerName + `"`)

// previewBridge is the built bridge split at its marker (nil before: the
// embedded app has none) and the addresses the plain and the TLS listener
// got (SetListen, SetTLSListen), under mu.
type previewBridge struct {
	before, after []byte
	mu            sync.Mutex
	listen        string
	tlsListen     string
}

// WithPreviewBridge gives the server the built bridge (internal/web's
// PreviewBridge): one that does not hold its marker as one double-quoted
// literal, exactly once, with the marker's name nowhere else, is none, and
// the route answers 404 no_bridge.
func WithPreviewBridge(script []byte) Option {
	return func(o *options) { o.previewBridge = script }
}

// splitBridge is script split at its one marker; nil, nil for none: no
// marker, the marker twice, its name in single quotes, between mismatched
// quotes, or anywhere beside the one literal (a comment, another string).
func splitBridge(script []byte) (before, after []byte) {
	if bytes.Count(script, bridgeMarker) != 1 || bytes.Count(script, []byte(bridgeMarkerName)) != 1 {
		return nil, nil
	}
	at := bytes.Index(script, bridgeMarker)
	return slices.Clone(script[:at]), slices.Clone(script[at+len(bridgeMarker):])
}

// SetListen gives the server the address its plain listener got (conductor
// serve, once it listens, before it serves): the bridge's origins are read
// from it, and when the embedded app has the bridge and the listener is on
// the loopback (or every address), the engine names the bridge's URL there
// to every tree with a preview resolved after it.
func (s *Server) SetListen(addr string) {
	s.bridge.mu.Lock()
	s.bridge.listen = addr
	s.bridge.mu.Unlock()
	if s.bridge.before == nil || s.cfg.Switchyard.Enabled {
		return
	}
	if u := loopbackBase(addr); u != "" {
		s.runs.SetPreviewBridge(u + "/preview-bridge.js")
	}
}

// loopbackBase is http://<loopback>:<port> for a listener at addr that the
// loopback reaches, "" for one it does not.
func loopbackBase(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	switch {
	case host == "" || ip != nil && ip.IsUnspecified():
		return "http://127.0.0.1:" + port
	case ip != nil && ip.IsLoopback():
		return "http://" + net.JoinHostPort(ip.String(), port)
	}
	return ""
}

// SetTLSListen gives the server the address its TLS listener got
// (conductor serve, once that listens): the bridge's https origins are at
// its port, the one it is bound to, never the configured text (:0 is a
// port drawn at listening).
func (s *Server) SetTLSListen(addr string) {
	s.bridge.mu.Lock()
	s.bridge.tlsListen = addr
	s.bridge.mu.Unlock()
}

// webOrigin is the origin of scheme, host and port as a browser writes
// one (MessageEvent.origin): scheme and host in lower case, an IPv6 host
// in brackets, the scheme's default port left out.
func webOrigin(scheme, host, port string) string {
	scheme, host = asciiLower(scheme), asciiLower(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	if port == "" || scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		if strings.Contains(host, ":") {
			return scheme + "://[" + host + "]"
		}
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// bridgeOrigins are the origins the server answers the workbench on,
// written as a browser writes them: each listener's (the plain one's and
// the TLS one's, at the port each is bound to) and publicUrl's; each once,
// each within maxBridgeOrigin, at most maxBridgeOrigins. A listener on a
// loopback address names that address and the loopback's three names,
// 127.0.0.1, localhost and [::1], at its port; one on every address the
// three names; one on another address that address alone.
func (s *Server) bridgeOrigins() []string {
	s.bridge.mu.Lock()
	listen, tlsListen := s.bridge.listen, s.bridge.tlsListen
	s.bridge.mu.Unlock()
	var out []string
	add := func(o string) {
		if o != "" && len(o) <= maxBridgeOrigin && len(out) < maxBridgeOrigins && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	at := func(scheme, addr string) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return
		}
		if n, err := strconv.Atoi(port); err != nil || n <= 0 {
			return
		}
		ip := net.ParseIP(host)
		loopback := ip != nil && ip.IsLoopback() || asciiLower(host) == "localhost"
		if host != "" && (ip == nil || !ip.IsUnspecified()) {
			add(webOrigin(scheme, host, port))
		}
		if host == "" || ip != nil && ip.IsUnspecified() || loopback {
			for _, name := range []string{"127.0.0.1", "localhost", "::1"} {
				add(webOrigin(scheme, name, port))
			}
		}
	}
	at("http", listen)
	if s.cfg.TLSEnabled() {
		at("https", tlsListen)
	}
	if u, err := url.Parse(s.cfg.PublicURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil {
		add(webOrigin(u.Scheme, u.Hostname(), u.Port()))
	}
	return out
}

// originsText is origins as the script holds them: a JSON array of
// strings, with every character that could end a string of any quote, a
// line, a comment or a script tag escaped (json.Marshal escapes ", \, <, >,
// &, control characters, U+2028 and U+2029; ', ` and $ are escaped here),
// so no origin's text is anything but data.
func originsText(origins []string) []byte {
	b, _ := json.Marshal(origins)
	esc := func(hex string) string { return `\` + "u00" + hex }
	return []byte(strings.NewReplacer(`'`, esc("27"), "`", esc("60"), `$`, esc("24")).Replace(string(b)))
}

// handlePreviewBridge serves the bridge with the server's origins written
// in (GET /preview-bridge.js): 404 no_bridge for a server whose embedded app
// has none, and in switchyard mode, which frames no preview.
func (s *Server) handlePreviewBridge(w http.ResponseWriter, r *http.Request) {
	if s.bridge.before == nil || s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusNotFound, "no_bridge", "this server was built without the web app's preview bridge: run make web-build")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(bytes.Join([][]byte{s.bridge.before, originsText(s.bridgeOrigins()), s.bridge.after}, nil))
}
