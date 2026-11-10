package github_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/net/http2"

	"github.com/rocksolidlabs/conductor/internal/tracker"
	"github.com/rocksolidlabs/conductor/internal/tracker/github"
)

// h2Resetter is an HTTP/2 server, spoken frame by frame, that reads each
// request whole and answers it with a stream reset of the given code: the
// answer after which net/http's HTTP/2 client may send a request again.
type h2Resetter struct {
	ln   net.Listener
	code http2.ErrCode
	// whole counts the requests read to their end; conns the connections.
	whole, conns atomic.Int64
	wg           sync.WaitGroup
	mu           sync.Mutex
	open         []net.Conn
}

func newH2Resetter(t *testing.T, code http2.ErrCode) *h2Resetter {
	t.Helper()
	// httptest's certificate, taken from a server started for it alone.
	ts := httptest.NewUnstartedServer(http.NotFoundHandler())
	ts.EnableHTTP2 = true
	ts.StartTLS()
	certs := ts.TLS.Certificates
	ts.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	s := &h2Resetter{ln: ln, code: code}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns.Add(1)
			s.mu.Lock()
			s.open = append(s.open, c)
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(c)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for _, c := range s.open {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

func (s *h2Resetter) serve(c net.Conn) {
	defer c.Close()
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(c, preface); err != nil || string(preface) != http2.ClientPreface {
		return
	}
	fr := http2.NewFramer(c, c)
	if fr.WriteSettings() != nil {
		return
	}
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = fr.WriteSettingsAck()
			}
		case *http2.HeadersFrame:
			if f.StreamEnded() {
				s.whole.Add(1)
				_ = fr.WriteRSTStream(f.StreamID, s.code)
			}
		case *http2.DataFrame:
			if f.StreamEnded() {
				s.whole.Add(1)
				_ = fr.WriteRSTStream(f.StreamID, s.code)
			}
		}
	}
}

// adapter is an adapter whose every connection reaches s over HTTP/2.
func (s *h2Resetter) adapter() *github.Adapter {
	var d net.Dialer
	addr := s.ln.Addr().String()
	return github.New(github.Options{Logger: quiet, Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // the test server's own certificate
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
	}})
}

// A write's request reaches GitHub once, whatever the layers under the
// adapter would do: over HTTP/2, a stream reset after the request was read
// (a protocol error, a refused stream) is a write whose outcome is not
// known, never a request sent again.
func TestWriteSentOnceOverHTTP2(t *testing.T) {
	for _, code := range []http2.ErrCode{http2.ErrCodeProtocol, http2.ErrCodeRefusedStream} {
		t.Run(code.String(), func(t *testing.T) {
			s := newH2Resetter(t, code)
			a := s.adapter()
			cred := tracker.Token("github.com", secret)
			_, err := a.Comment(ctx, cred.ForWrite(tracker.Write{Kind: tracker.WriteComment, Publication: "p1", Try: 1}), target, "hello")
			if n := s.whole.Load(); n != 1 {
				t.Fatalf("one Comment reached the server as %d requests read whole (%d connections): %v", n, s.conns.Load(), err)
			}
			if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
				t.Fatalf("a reset after the request was read: %v", err)
			}
			err = a.Close(ctx, cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), target, tracker.ReasonCompleted)
			if n := s.whole.Load(); n != 2 || err == nil || tracker.NothingWritten(err) {
				t.Fatalf("Close: %d requests, %v", n, err)
			}
		})
	}
}

// Over HTTP/1.1 too: a connection closed after the request was read is a
// write sent once, its outcome not known.
func TestWriteSentOnceOnAReset(t *testing.T) {
	r := newRig(t)
	r.fake.Handle(http.MethodPost, api, "/graphql", reset)
	_, err := r.a.Comment(ctx, r.commentCred("p1", 1), target, "hello")
	if n := r.fake.Writes("graphql AddComment"); n != 1 {
		t.Fatalf("one Comment reached the fake %d times", n)
	}
	if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
		t.Fatalf("a reset: %v", err)
	}
}

// A write follows no redirect: a 307 or 308 is not sent again, and a 301,
// 302 or 303 is not turned into a GET whose refusal would be read as the
// write's. Every 3xx to a write is a write whose outcome is not known.
func TestWriteFollowsNoRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		r := newRig(t)
		calls := 0
		r.fake.Handle(http.MethodPost, api, "/graphql", func(w http.ResponseWriter, req *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Location", "https://api.github.com/elsewhere")
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"addComment":{"commentEdge":{"node":{"fullDatabaseId":"7700001","databaseId":7700001,"url":"https://github.com/octo-example/widgets/issues/42#issuecomment-7700001","createdAt":"2026-11-10T12:00:01Z","author":{"login":"mona-example","databaseId":9000001}}}}}}`))
		})
		r.reply(http.MethodGet, "/elsewhere", 401, `{"message":"Bad credentials"}`)
		r.reply(http.MethodPost, "/elsewhere", 200, `{"data":{"addComment":null}}`)
		_, err := r.a.Comment(ctx, r.commentCred("p1", 1), target, "hello")
		if n := r.fake.Writes("graphql AddComment"); n != 1 || len(r.fake.Requests()) != 1 {
			t.Errorf("%d: %d AddComment writes, %d requests", status, n, len(r.fake.Requests()))
		}
		if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
			t.Errorf("%d: %v", status, err)
		}
	}
}
