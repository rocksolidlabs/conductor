package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// The move is the operator's alone: no token, a share link's (view or
// control) and the session's own agent token are answered 401 and nothing
// moves; the operator's body is bounded at 64 bytes and read strictly.
func TestMoveIsTheOperators(t *testing.T) {
	c := newMoveEnv(t, nil, nil)
	l := c.launch(t)
	id := l.Info().ID
	c.turnEnded(t, id)
	var tokens []string
	for _, role := range []string{"view", "control"} {
		resp, out := c.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": role})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("a %s link: %d %v", role, resp.StatusCode, out)
		}
		u := out["url"].(string)
		tokens = append(tokens, u[strings.LastIndex(u, "/")+1:])
	}
	tokens = append(tokens, c.agentToken(id), "")
	for i, tok := range tokens {
		resp, _ := c.do("POST", "/api/sessions/"+id+"/way", tok, map[string]any{"to": "protocol"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %d: %d, want 401", i, resp.StatusCode)
		}
	}
	if l.Moving() || l.Way() != session.WayTerminal || len(c.pipeSpecs()) != 0 {
		t.Fatal("a refused request moved the session")
	}
	post := func(body string) int {
		req, _ := http.NewRequest("POST", c.http.URL+"/api/sessions/"+id+"/way", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// 64 bytes exactly is read; one more is refused unread.
	at := `{"to":"protocol"}` + strings.Repeat(" ", 64-len(`{"to":"protocol"}`))
	if n := post(at + " "); n != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body of %d bytes: %d, want 413", len(at)+1, n)
	}
	if n := post(`{"to":"protocol","by":"x"}`); n != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d, want 400", n)
	}
	if n := post(`{"to":"chat"}`); n != http.StatusBadRequest {
		t.Fatalf("no such way: %d, want 400", n)
	}
	if n := post(at); n != http.StatusOK {
		t.Fatalf("the operator's, at the bound: %d", n)
	}
	moved(t, l, session.WayProtocol)
}
