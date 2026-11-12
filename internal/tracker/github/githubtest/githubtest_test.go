package githubtest_test

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/tracker/github/githubtest"
)

// TestHeldRequestLeftIsNeverAnswered: a request held at a gate whose
// client went away gets no answer, as GitHub sends none to a client that
// left. A TLS client leaves in two steps, its close_notify first and its
// socket after, and an answer the fake wrote between them was read by the
// client's transport as GitHub's: net/http's own 200 with no body, written
// when the held handler returned having written nothing (#281: the
// adapter's write held past its own deadline said "200: an answer
// Conductor cannot read" instead of "timed out"). CloseWrite keeps that
// moment open, so the test reads whatever the fake sends in it, every run.
func TestHeldRequestLeftIsNeverAnswered(t *testing.T) {
	s := githubtest.New(t)
	s.Reply(http.MethodPost, "api.github.com", "/graphql", http.StatusOK, `{"data":{}}`)
	gate := s.Hold(http.MethodPost, "api.github.com", "/graphql")
	conn, err := tls.Dial("tcp", s.TLS.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true}) // the fake's own certificate
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := `{"query":"mutation AddComment($subjectId: ID!) { x }"}`
	if _, err := fmt.Fprintf(conn, "POST /graphql HTTP/1.1\r\nHost: api.github.com\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
	<-gate.Reached()
	// The client's close_notify, its socket still open for reading.
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
		resp.Body.Close()
		t.Fatalf("a held request whose client left was answered %s", resp.Status)
	}
	if n := s.Writes("graphql AddComment"); n != 1 {
		t.Fatalf("%d writes counted", n)
	}
}
