package agentproto

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
)

// The stub agent the e2e suite and the server's tests run in place of
// Claude Code (web/e2e/stub-protocol.mjs) speaks what the driver reads: its
// initialize answer passes the shape check, a message is replayed under
// the uuid written and answered in a turn with words and, asked, a tool
// step; its interrupt receipt and aborted turn end the turn interrupted.
func TestStubProtocolSpeaksTheDriver(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node to run the stub agent: ", err)
	}
	stub, err := filepath.Abs(filepath.Join("..", "..", "web", "e2e", "stub-protocol.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "go")
	cmd := exec.Command("node", stub, "-p", "--input-format", "stream-json", "--resume", "11111111-2222-4333-8444-555555555555")
	cmd.Env = append(os.Environ(), "STUB_PROTOCOL_HOLD="+gate)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	c := NewClaude(stdin, stdout, ClaudeOptions{})
	rec := &recorder{}
	ended := make(chan event, 4)
	texts := make(chan struct{}, 64)
	rec.after = func(e event) {
		switch e.kind {
		case "turn_ended":
			ended <- e
		case "text":
			texts <- struct{}{}
		}
	}
	read := make(chan error, 1)
	go func() { read <- c.Read(context.Background(), rec) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, session.Message{Ref: "r1", Text: "run a tool"}); err != nil {
		t.Fatal(err)
	}
	if e := <-ended; e.out != "done" || e.use.OutputTokens == 0 {
		t.Fatalf("the turn ended %+v", e)
	}
	r := rec.render()
	for _, want := range []string{"conversation 11111111-2222-4333-8444-555555555555", "accepted r1", `Bash summary="ls"`, `output="notes.txt"`, `"stub chat: run a tool"`} {
		if !strings.Contains(r, want) {
			t.Fatalf("no %q in\n%s", want, r)
		}
	}
	// A turn held at its gate, interrupted.
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	for len(texts) > 0 {
		<-texts
	}
	if err := c.Send(ctx, session.Message{Ref: "r2", Text: "wait"}); err != nil {
		t.Fatal(err)
	}
	<-texts
	if err := c.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if e := <-ended; e.out != "interrupted" {
		t.Fatalf("the interrupted turn ended %q", e.out)
	}
	_ = c.CloseInput()
	if err := <-read; !errors.Is(err, io.EOF) {
		t.Fatalf("Read: %v", err)
	}
}
