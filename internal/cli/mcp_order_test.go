package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// conductor mcp keeps the order of every call that is not a permission
// call: a client that does not wait for answers (an agent may send several
// tool calls in one message) has them reach the server in the order it
// sent them, a permission call held beside them all the while; pings are
// answered meanwhile.
func TestMCPKeepsTheOrderOfOtherCalls(t *testing.T) {
	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/s1/permission":
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"behavior":"allow"}`)
		case "/api/sessions/s1/events", "/api/sessions/s1/attention":
			var body struct {
				Message string `json:"message"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			order = append(order, body.Message)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	c := startMCP(t, &mcpServer{getenv: chatEnv(srv.URL)})
	c.send(permissionCall(1, "toolu_1"))
	const pairs = 100
	report := func(id int, message string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"report","arguments":{"event":"progress","message":%q}}}`, id, message)
	}
	var want []string
	for i := range pairs {
		// Two calls sent back to back, neither waited for.
		first, second := fmt.Sprintf("first %d", i), fmt.Sprintf("second %d", i)
		c.send(report(10+2*i, first))
		c.send(report(11+2*i, second))
		want = append(want, first, second)
		c.byID(10 + 2*i)
		c.byID(11 + 2*i)
	}
	c.send(pingLine(2))
	c.byID(2)
	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	if !slices.Equal(got, want) {
		for i := range got {
			if i >= len(want) || got[i] != want[i] {
				t.Fatalf("the server met the calls in another order, from %d: %v", i, got[i:min(i+4, len(got))])
			}
		}
		t.Fatalf("the server met %d calls, want %d", len(got), len(want))
	}
}
