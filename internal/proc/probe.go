package proc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// probeClient is the client of one try of a probe over HTTP: the probe's
// interval its timeout, no proxy and no connection kept, and no redirect
// followed (http.ErrUseLastResponse: a 3xx is the try's answer), so a
// service, or a manifest at a result commit, answering a redirect off the
// loopback makes the server fetch nothing there.
func probeClient(interval time.Duration) *http.Client {
	return &http.Client{
		Timeout:       interval,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Probe is one try of a probe over HTTP on this machine: one GET of url
// through a client of its own (probeClient), its status, the body closed
// unread, so nothing of it is kept.
func Probe(ctx context.Context, url string, interval time.Duration) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := probeClient(interval).Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// FreePort draws a port of this machine's loopback: a bind of 127.0.0.1:0,
// its number taken and the listener closed, so that a tree's own can bind
// it. Nothing holds the port on the host meanwhile; the engine's lease
// keeps two live trees of one server off one number.
func FreePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || addr.Port == 0 {
		return 0, fmt.Errorf("the loopback gave no port: %v", l.Addr())
	}
	return addr.Port, nil
}
