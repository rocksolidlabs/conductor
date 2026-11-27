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

// The stub's second mode (web/e2e/stub-protocol-codex.mjs), Codex's app
// server, speaks what the Codex driver reads: its initialize answer passes
// the shape check, the thread is resumed, a message is taken and answered in
// a turn with words and, asked, a command; an approval it asks is answered
// for its own id and the step runs; a held turn, interrupted, ends
// interrupted. What the driver wrote is the stable surface's alone.
func TestStubCodexSpeaksTheDriver(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node to run the stub agent: ", err)
	}
	stub, err := filepath.Abs(filepath.Join("..", "..", "web", "e2e", "stub-protocol.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".stub-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "go")
	version := exec.Command("node", stub, "--version")
	version.Env = append(os.Environ(), "STUB_PROTOCOL_AGENT=codex")
	if out, err := version.Output(); err != nil || strings.TrimSpace(string(out)) != "codex-cli "+CodexMinVersion {
		t.Fatalf("--version: %q %v", out, err)
	}
	cmd := exec.Command("node", stub, "app-server", "--listen", "stdio://")
	cmd.Env = append(os.Environ(), "HOME="+home, "STUB_PROTOCOL_HOLD="+gate)
	cmd.Dir = t.TempDir()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	c := NewCodex(stdin, stdout, CodexOptions{Resume: CodexResume{Thread: fixtureThread}})
	rec := &recorder{}
	ended := make(chan event, 4)
	texts := make(chan struct{}, 64)
	rec.after = func(e event) {
		switch e.kind {
		case "turn_ended":
			ended <- e
		case "text":
			texts <- struct{}{}
		case "permission":
			// On a goroutine of its own, as the session replies.
			go func() { _ = c.Reply(context.Background(), e.ref, session.Verdict{Allow: true}) }()
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
	if err := c.Send(ctx, session.Message{Ref: "r1", Text: "run a tool and ask command"}); err != nil {
		t.Fatal(err)
	}
	if e := <-ended; e.out != "done" || e.use.OutputTokens != 7 {
		t.Fatalf("the turn ended %+v", e)
	}
	r := rec.render()
	for _, want := range []string{"conversation " + fixtureThread, "accepted r1", `commandExecution summary="ls"`, `output="notes.txt\n"`,
		`permission codex-req-1 call=call_stub_`, `output="ran: echo allowed\n"`, `"stub chat: run a tool and ask command"`} {
		if !strings.Contains(r, want) {
			t.Fatalf("no %q in\n%s", want, r)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".stub-sessions", fixtureThread+".allowed")); strings.TrimSpace(string(b)) == "" {
		t.Fatal("the stub was not told the approval")
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
	surf := loadSurface(t)
	wire, err := os.ReadFile(filepath.Join(home, ".stub-sessions", fixtureThread+".wire"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(wire)), "\n") {
		if why := surf.stableLine(l); why != "" {
			t.Errorf("%s: %s", why, l)
		}
	}
}
