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
	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// h2Resetter is an HTTP/2 server, spoken frame by frame, that reads each
// request whole and answers it with a stream reset of the given code: the
// answer after which net/http's HTTP/2 client may send a request again.
type h2Resetter struct {
	ln   net.Listener
	code http2.ErrCode
	// whole counts the requests read to their end; conns the connections.
	whole, conns atomic.Int64
	// goaway answers a request read whole with a GOAWAY naming no stream
	// as processed, instead of a stream reset.
	goaway atomic.Bool
	wg     sync.WaitGroup
	mu     sync.Mutex
	open   []net.Conn
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
				s.answer(fr, f.StreamID)
			}
		case *http2.DataFrame:
			if f.StreamEnded() {
				s.answer(fr, f.StreamID)
			}
		}
	}
}

func (s *h2Resetter) answer(fr *http2.Framer, stream uint32) {
	s.whole.Add(1)
	if s.goaway.Load() {
		_ = fr.WriteGoAway(0, http2.ErrCodeNo, nil)
		return
	}
	_ = fr.WriteRSTStream(stream, s.code)
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
			cred := mustToken(t, "github.com", false)
			_, err := a.Comment(ctx, cred.ForWrite(tagFor("p1", 1, posted("p1", "hello"))), target, posted("p1", "hello"))
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
	_, err := r.post(ctx, "p1", 1, target, "hello")
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
			_, _ = w.Write([]byte(`{"data":{"addComment":{"subject":{"id":"I_kwDOFixture42"},"commentEdge":{"node":{"fullDatabaseId":"7700001","databaseId":7700001,"url":"https://github.com/octo-example/widgets/issues/42#issuecomment-7700001","createdAt":"2026-11-10T12:00:01Z","repository":{"fullDatabaseId":"4000001"},"author":{"login":"mona-example","databaseId":9000001}}}}}}`))
		})
		r.reply(http.MethodGet, "/elsewhere", 401, `{"message":"Bad credentials"}`)
		r.reply(http.MethodPost, "/elsewhere", 200, `{"data":{"addComment":null}}`)
		_, err := r.post(ctx, "p1", 1, target, "hello")
		if n := r.fake.Writes("graphql AddComment"); n != 1 || len(r.fake.Requests()) != 1 {
			t.Errorf("%d: %d AddComment writes, %d requests", status, n, len(r.fake.Requests()))
		}
		if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
			t.Errorf("%d: %v", status, err)
		}
	}
}

// A write with no body is sent once as one with a body is: over HTTP/2 a
// request without a body is one net/http sends again after a reset or a
// GOAWAY, so a write never goes without one.
func TestBodilessWriteSentOnceOverHTTP2(t *testing.T) {
	for _, mode := range []string{"PROTOCOL_ERROR", "REFUSED_STREAM", "GOAWAY"} {
		t.Run(mode, func(t *testing.T) {
			code := http2.ErrCodeProtocol
			if mode == "REFUSED_STREAM" {
				code = http2.ErrCodeRefusedStream
			}
			s := newH2Resetter(t, code)
			s.goaway.Store(mode == "GOAWAY")
			cred, _ := credFor("github.com")
			err := github.BodilessWrite(ctx, s.adapter(), cred)
			if n := s.whole.Load(); n != 1 {
				t.Fatalf("one write reached the server as %d requests (%d connections): %v", n, s.conns.Load(), err)
			}
			if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
				t.Fatalf("%v", err)
			}
		})
	}
}

// A GOAWAY after the write was read is a write sent once too.
func TestWriteSentOnceAfterAGoAway(t *testing.T) {
	s := newH2Resetter(t, http2.ErrCodeNo)
	s.goaway.Store(true)
	a := s.adapter()
	cred, _ := credFor("github.com")
	_, err := a.Comment(ctx, cred.ForWrite(tagFor("p1", 1, posted("p1", "hello"))), target, posted("p1", "hello"))
	if n := s.whole.Load(); n != 1 || err == nil || tracker.NothingWritten(err) {
		t.Fatalf("Comment: %d requests, %v", n, err)
	}
	err = a.Close(ctx, cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), target, tracker.ReasonCompleted)
	if n := s.whole.Load(); n != 2 || err == nil || tracker.NothingWritten(err) {
		t.Fatalf("Close: %d requests, %v", n, err)
	}
}

// Over HTTP/1.1, a connection kept from an earlier answer and closed after
// the write was read is a write sent once, its outcome not known.
func TestWriteSentOnceOnAReusedConnection(t *testing.T) {
	r := newRig(t)
	var calls atomic.Int64
	r.fake.Handle(http.MethodPost, api, "/graphql", func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(githubtest.Body(commentAnswer(6700000001, 9000001)))
			return
		}
		reset(w, req)
	})
	if _, err := r.post(ctx, "p1", 1, target, "hello"); err != nil {
		t.Fatal(err)
	}
	_, err := r.post(ctx, "p2", 1, target, "hello")
	if n := r.fake.Writes("graphql AddComment"); n != 2 {
		t.Fatalf("two Comments reached the fake %d times", n)
	}
	if err == nil || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
		t.Fatalf("a reset on a kept connection: %v", err)
	}
}

// swallow is a base round tripper that takes a request whole and fails
// without a word of what it did: no trace callback, no answer.
type swallow struct{ n atomic.Int64 }

func (s *swallow) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	s.n.Add(1)
	return nil, errors.New("swallow: the connection was reset")
}

// "Nothing written" needs a fact: a base transport that took the write and
// failed tells nothing of whether it was sent, so the outcome is not
// known; only a write the base was never handed is nothing written.
func TestWriteUnknownWhenTheBaseFails(t *testing.T) {
	b := &swallow{}
	a := github.New(github.Options{Logger: quiet, Transport: b})
	cred, _ := credFor("github.com")
	_, err := a.Comment(ctx, cred.ForWrite(tagFor("p1", 1, posted("p1", "hello"))), target, posted("p1", "hello"))
	if b.n.Load() != 1 || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
		t.Fatalf("Comment: %d requests, %v", b.n.Load(), err)
	}
	err = a.Close(ctx, cred.ForWrite(tracker.Write{Kind: tracker.WriteClose, Publication: "p1", Try: 1}), target, tracker.ReasonCompleted)
	if b.n.Load() != 2 || tracker.NothingWritten(err) || !errors.Is(err, tracker.ErrWriteUnknown) {
		t.Fatalf("Close: %d requests, %v", b.n.Load(), err)
	}
}
