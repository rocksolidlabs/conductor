package proc

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The probe asks the loopback through a client of its own: a URL that
// answers is its status, the body read nothing of; a redirect is the try's
// answer and is never followed (the server it points at sees no request);
// a port nothing listens on is an error. (Moved from internal/crew, whose
// native executor calls it.)
func TestTheNativeProbeFollowsNoRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/elsewhere", http.StatusFound)
	}))
	defer moved.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", 1<<20))
	}))
	defer up.Close()
	if status, err := Probe(t.Context(), up.URL+"/", 10*time.Second); err != nil || status != http.StatusOK {
		t.Fatalf("a URL that answers: %d %v", status, err)
	}
	if status, err := Probe(t.Context(), moved.URL+"/", 10*time.Second); err != nil || status != http.StatusFound {
		t.Fatalf("a redirect: %d %v", status, err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the redirect was followed: %d requests elsewhere", n)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + l.Addr().String() + "/"
	l.Close()
	if status, err := Probe(t.Context(), closed, 10*time.Second); err == nil {
		t.Fatalf("a port nothing listens on answered %d", status)
	}
}

// FreePort draws a port of the loopback and lets it go: the number it
// gives is free to bind. Another process may take a free number between
// the two, so a number that cannot be bound is drawn again, three times at
// most: one that FreePort held would fail every time. (Moved from
// internal/crew, whose native executor calls it.)
func TestTheNativeExecutorDrawsAPortAndLetsItGo(t *testing.T) {
	var errs []string
	for range 3 {
		n, err := FreePort(t.Context())
		if err != nil || n < 1 || n > 65535 {
			t.Fatalf("drawn %d: %v", n, err)
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
		if err == nil {
			l.Close()
			return
		}
		errs = append(errs, err.Error())
	}
	t.Fatalf("no drawn port could be bound: %v", errs)
}
