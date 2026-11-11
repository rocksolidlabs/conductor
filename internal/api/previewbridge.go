package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

// bridgeMarker is the string literal, in either quote, the web build leaves
// for the origins.
var bridgeMarker = regexp.MustCompile(`["']__CONDUCTOR_WORKBENCH_ORIGINS__["']`)

// previewBridge is the built bridge split at its marker (nil before: the
// embedded app has none) and the address the plain listener got
// (SetListen), under mu.
type previewBridge struct {
	before, after []byte
	mu            sync.Mutex
	listen        string
}

// WithPreviewBridge gives the server the built bridge (internal/web's
// PreviewBridge): one that is not there, or does not hold its marker
// exactly once, is none, and the route answers 404 no_bridge.
func WithPreviewBridge(script []byte) Option {
	return func(o *options) { o.previewBridge = script }
}

// splitBridge is script split at its one marker; nil, nil for none.
func splitBridge(script []byte) (before, after []byte) {
	at := bridgeMarker.FindAllIndex(script, -1)
	if len(at) != 1 {
		return nil, nil
	}
	return slices.Clone(script[:at[0][0]]), slices.Clone(script[at[0][1]:])
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

// bridgeOrigins are the origins the server answers the workbench on: the
// plain listener's at its port (its own address, and the loopback's
// 127.0.0.1, localhost and [::1]), the TLS listener's loopback names at its
// port, and publicUrl's origin; each once, each within maxBridgeOrigin, at
// most maxBridgeOrigins.
func (s *Server) bridgeOrigins() []string {
	s.bridge.mu.Lock()
	listen := s.bridge.listen
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
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
			add(scheme + "://" + net.JoinHostPort(ip.String(), port))
		}
		for _, name := range []string{"127.0.0.1", "localhost", "::1"} {
			add(scheme + "://" + net.JoinHostPort(name, port))
		}
	}
	at("http", listen)
	if s.cfg.TLSEnabled() {
		tls := s.cfg.TLS.Listen
		if tls == "" {
			tls = ":8443"
		}
		at("https", tls)
	}
	if u, err := url.Parse(s.cfg.PublicURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil {
		add(u.Scheme + "://" + u.Host)
	}
	return out
}

// handlePreviewBridge serves the bridge with the server's origins written
// in (GET /preview-bridge.js): 404 no_bridge for a server whose embedded app
// has none, and in switchyard mode, which frames no preview.
func (s *Server) handlePreviewBridge(w http.ResponseWriter, r *http.Request) {
	if s.bridge.before == nil || s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusNotFound, "no_bridge", "this server was built without the web app's preview bridge: run make web-build")
		return
	}
	origins, _ := json.Marshal(s.bridgeOrigins())
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(bytes.Join([][]byte{s.bridge.before, origins, s.bridge.after}, nil))
}
