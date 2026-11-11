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
	"strings"
	"sync"
)

// The preview bridge (docs/protocol.md, Preview bridge): the script a
// previewed page loads, or the desktop shell puts into it, to read the page
// for the workbench that frames it. The web build emits it (PV1 PR-2b;
// until it does, the embed has none and the route answers 404 no_bridge);
// GET /preview-bridge.js serves it whole, unchanged, inside a wrapper the
// server writes: a function whose one parameter is the server's own
// workbench origins, called with them, so the bridge answers a parent
// window at those origins alone. The route is unauthenticated (a script
// tag carries no header) and holds no secret.

// The bridge's origins: at most maxBridgeOrigins, each at most
// maxBridgeOrigin bytes.
const (
	maxBridgeOrigins = 16
	maxBridgeOrigin  = 256
)

// The wrapper: bridgeHead, the script, bridgeTail, the origins as data,
// bridgeEnd. The script reads its origins from the parameter alone.
const (
	bridgeHead = "(function (conductorWorkbenchOrigins) {\n"
	bridgeTail = "\n})("
	bridgeEnd  = ");\n"
)

// previewBridge is the built bridge (nil: the embedded app has none) and
// the addresses the plain and the TLS listener got (SetListen,
// SetTLSListen), under mu.
type previewBridge struct {
	script    []byte
	mu        sync.Mutex
	listen    string
	tlsListen string
}

// WithPreviewBridge gives the server the built bridge (internal/web's
// PreviewBridge); an empty one is none, and the route answers 404
// no_bridge.
func WithPreviewBridge(script []byte) Option {
	return func(o *options) { o.previewBridge = script }
}

// machineAddrs is the machine's addresses, for a listener on every
// address (a test replaces it).
var machineAddrs = net.InterfaceAddrs

// SetListen gives the server the address its plain listener got (conductor
// serve, once it listens, before it serves): the bridge's origins are read
// from it, and when the embedded app has the bridge and the listener is on
// the loopback (or every address), the engine names the bridge's URL there
// to every tree with a preview resolved after it.
func (s *Server) SetListen(addr string) {
	s.bridge.mu.Lock()
	s.bridge.listen = addr
	s.bridge.mu.Unlock()
	if s.bridge.script == nil || s.cfg.Switchyard.Enabled {
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
// one (MessageEvent.origin), "" for a host or a port it would write
// otherwise: an IP address in its own form (an IPv6 one in brackets), a
// name of lower-case letters, digits and hyphens whose last label begins
// with a letter (no internationalised name but its xn-- form), a port of
// 1 to 65535 left out when it is the scheme's default.
func webOrigin(scheme, host, port string) string {
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	} else if !browserName.MatchString(host) {
		return ""
	}
	n, err := strconv.Atoi(port)
	switch {
	case err != nil || n <= 0 || n > 65535:
		return ""
	case scheme == "http" && n == 80 || scheme == "https" && n == 443:
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + strconv.Itoa(n)
}

// browserName is a host name a browser keeps as it is.
var browserName = regexp.MustCompile(`^([a-z0-9-]+\.)*[a-z][a-z0-9-]*$`)

// publicOrigin is publicUrl's origin; "" when publicUrl names this machine
// alone (a local address enters the list only through a listener that
// holds it), or, left, when it does not already write its origin as a
// browser does (capitals, the default port, a port of leading zeros, a
// name not in ASCII, an address in another spelling, a user).
func (s *Server) publicOrigin() (o string, left bool) {
	pub := s.cfg.PublicURL
	u, err := url.Parse(pub)
	if err != nil || s.cfg.PublicURLIsLocal() || net.ParseIP(u.Hostname()).IsLoopback() {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	o = webOrigin(u.Scheme, u.Hostname(), port)
	if u.User != nil || o == "" || !strings.HasPrefix(pub, o) || len(pub) > len(o) && !strings.ContainsRune("/?#", rune(pub[len(o)])) {
		return "", true
	}
	return o, false
}

// PublicOriginLeftOut reports whether the bridge leaves publicUrl out of
// its origins as not written as a browser writes one (conductor serve
// says so once).
func (s *Server) PublicOriginLeftOut() bool {
	_, left := s.publicOrigin()
	return s.bridge.script != nil && !s.cfg.Switchyard.Enabled && left
}

// bridgeOrigins are the origins the server answers the workbench on,
// written as a browser writes them: each listener's (the plain one's and
// the TLS one's, at the port each is bound to) and publicUrl's; each once,
// each within maxBridgeOrigin, at most maxBridgeOrigins. A listener on a
// loopback address names that address and the loopback's three names,
// 127.0.0.1, localhost and [::1], at its port; one on every address the
// three names and, last, the machine's addresses as they are now, both
// families; one on another address that address alone.
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
	var every []string // the wildcard listeners, scheme and port
	at := func(scheme, addr string) {
		host, port, err := net.SplitHostPort(addr)
		ip := net.ParseIP(host)
		all := host == "" || ip.IsUnspecified()
		if err != nil {
			return
		} else if all {
			every = append(every, scheme, port)
		} else {
			add(webOrigin(scheme, host, port))
		}
		if all || ip.IsLoopback() || host == "localhost" {
			for _, name := range []string{"127.0.0.1", "localhost", "::1"} {
				add(webOrigin(scheme, name, port))
			}
		}
	}
	at("http", listen)
	if s.cfg.TLSEnabled() {
		at("https", tlsListen)
	}
	pub, _ := s.publicOrigin()
	add(pub)
	var addrs []net.Addr
	if len(every) > 0 {
		addrs, _ = machineAddrs()
	}
	for i := 0; i < len(every); i += 2 {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsMulticast() && !(n.IP.To4() == nil && n.IP.IsLinkLocalUnicast()) {
				add(webOrigin(every[i], n.IP.String(), every[i+1]))
			}
		}
	}
	return out
}

// originsText is origins as the wrapper's call holds them: a JSON array of
// strings, with every character that could end a string of any quote, a
// line, a comment or a script tag escaped (json.Marshal escapes ", \, <, >,
// &, control characters, U+2028 and U+2029; ', ` and $ are escaped here),
// so no origin's text is anything but data.
func originsText(origins []string) []byte {
	b, _ := json.Marshal(origins)
	esc := func(hex string) string { return `\` + "u00" + hex }
	return []byte(strings.NewReplacer(`'`, esc("27"), "`", esc("60"), `$`, esc("24")).Replace(string(b)))
}

// wrapBridge is script, byte for byte, in the wrapper called with origins.
func wrapBridge(script []byte, origins []string) []byte {
	return bytes.Join([][]byte{[]byte(bridgeHead), script, []byte(bridgeTail), originsText(origins), []byte(bridgeEnd)}, nil)
}

// handlePreviewBridge serves the bridge in its wrapper with the server's
// origins (GET /preview-bridge.js): 404 no_bridge for a server whose
// embedded app has none, and in switchyard mode, which frames no preview.
func (s *Server) handlePreviewBridge(w http.ResponseWriter, r *http.Request) {
	if s.bridge.script == nil || s.cfg.Switchyard.Enabled {
		writeError(w, http.StatusNotFound, "no_bridge", "this server was built without the web app's preview bridge: run make web-build")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(wrapBridge(s.bridge.script, s.bridgeOrigins()))
}
