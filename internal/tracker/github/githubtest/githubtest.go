// Package githubtest is a fake GitHub for the tracker's tests: one
// httptest TLS server that every host's requests reach (Transport dials it
// whatever the address, so a fixture keeps github.com's hosts: the API
// host, the image hosts, a log's storage host), and a plain one for the
// loopback hosts the e2e harness uses. It serves the routes a test
// scripts, records every request with its headers, counts every write by
// route, and holds a request on a gate the test opens. Its fixtures
// (fixtures/) are GitHub's REST answers in GitHub's documented shapes,
// with invented owners, repositories, accounts and ids, and no token or
// signed URL's real query.
package githubtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

//go:embed fixtures
var fixtures embed.FS

// Fixture is a fixture's bytes (fixtures/<name>).
func Fixture(name string) []byte {
	b, err := fixtures.ReadFile("fixtures/" + name)
	if err != nil {
		panic("githubtest: no fixture " + name)
	}
	return b
}

// Request is a request the fake received.
type Request struct {
	Method   string
	Host     string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// GraphQL is a GraphQL request's parts.
type GraphQL struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// Gate holds the requests of a route until Open.
type Gate struct {
	reached chan struct{}
	once    sync.Once
	open    chan struct{}
	openO   sync.Once
}

// Reached is closed once a request has arrived at the gate.
func (g *Gate) Reached() <-chan struct{} { return g.reached }

// Open lets the held requests go on.
func (g *Gate) Open() { g.openO.Do(func() { close(g.open) }) }

// Server is the fake GitHub.
type Server struct {
	TLS   *httptest.Server
	Plain *httptest.Server

	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []Request
	writes   map[string]int
	gates    map[string]*Gate
}

// New starts the fake; the test's cleanup stops it.
func New(t testing.TB) *Server {
	s := &Server{routes: map[string]http.HandlerFunc{}, writes: map[string]int{}, gates: map[string]*Gate{}}
	h := http.HandlerFunc(s.serve)
	s.TLS = httptest.NewTLSServer(h)
	s.Plain = httptest.NewServer(h)
	t.Cleanup(func() {
		s.mu.Lock()
		for _, g := range s.gates {
			g.Open()
		}
		s.mu.Unlock()
		s.TLS.Close()
		s.Plain.Close()
	})
	return s
}

// PlainHost is the loopback host:port of the plain server.
func (s *Server) PlainHost() string { return strings.TrimPrefix(s.Plain.URL, "http://") }

// Transport is a base round tripper whose every connection reaches the
// fake: https ones its TLS server (its certificate is taken for any name:
// the fake answers as github.com's hosts), http ones its plain server.
func (s *Server) Transport() *http.Transport {
	tlsAddr := s.TLS.Listener.Addr().String()
	plainAddr := s.Plain.Listener.Addr().String()
	var d net.Dialer
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == plainAddr {
				return d.DialContext(ctx, network, plainAddr)
			}
			return d.DialContext(ctx, network, tlsAddr)
		},
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // the fake's own certificate, for every host it answers as
		ForceAttemptHTTP2: false,
	}
}

func key(method, host, path string) string { return method + " " + host + path }

// Handle serves method host path (the path without its query) with h.
func (s *Server) Handle(method, host, path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[key(method, host, path)] = h
}

// Reply serves a fixed answer: a status, a body (bytes, a string, or a
// value written as JSON) and headers in pairs.
func (s *Server) Reply(method, host, path string, status int, body any, header ...string) {
	b := Body(body)
	s.Handle(method, host, path, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		if w.Header().Get("Content-Type") == "" && len(b) > 0 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.WriteHeader(status)
		_, _ = w.Write(b)
	})
}

// Body is a reply's bytes: bytes and strings as they are, anything else
// as JSON.
func Body(body any) []byte {
	switch b := body.(type) {
	case nil:
		return nil
	case []byte:
		return b
	case string:
		return []byte(b)
	default:
		out, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		return out
	}
}

// Hold makes the requests of method host path wait at a gate until the
// test opens it (or the server stops).
func (s *Server) Hold(method, host, path string) *Gate {
	g := &Gate{reached: make(chan struct{}), open: make(chan struct{})}
	s.mu.Lock()
	s.gates[key(method, host, path)] = g
	s.mu.Unlock()
	return g
}

// Requests is every request received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Writes is the count of the writes made to a route: "POST <host><path>"
// for REST, "graphql <operation>" (AddComment, CloseIssue) for a GraphQL
// mutation.
func (s *Server) Writes(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes[route]
}

// AllWrites is the count of every write made.
func (s *Server) AllWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.writes {
		n += c
	}
	return n
}

// ParseGraphQL reads a GraphQL request's body.
func ParseGraphQL(body []byte) (GraphQL, error) {
	var g GraphQL
	err := json.Unmarshal(body, &g)
	return g, err
}

// operation is a GraphQL document's operation when it is a mutation.
func operation(body []byte) string {
	g, err := ParseGraphQL(body)
	if err != nil {
		return ""
	}
	q := strings.TrimSpace(g.Query)
	rest, ok := strings.CutPrefix(q, "mutation ")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "(")
	return strings.TrimSpace(name)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	host := r.Host
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if strings.HasSuffix(r.URL.Path, "graphql") {
			if op := operation(body); op != "" {
				s.writes["graphql "+op]++
			}
		} else {
			s.writes[r.Method+" "+host+r.URL.Path]++
		}
	}
	k := key(r.Method, host, r.URL.Path)
	h := s.routes[k]
	g := s.gates[k]
	s.mu.Unlock()
	if g != nil {
		g.once.Do(func() { close(g.reached) })
		select {
		case <-g.open:
		case <-r.Context().Done():
			return
		}
	}
	if h == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`)
		return
	}
	h(w, r)
}
