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

// Row 12: the stub takes a message without client_composed as the real
// agent does (the shapes of hand-built-expand.jsonl), and a marked one as
// it always did; each message's mark is kept beside its conversation, so
// that a spec reads how each line went. Every message is accepted, none
// left to go unconfirmed.
func TestStubProtocolExpands(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node to run the stub agent: ", err)
	}
	stub, err := filepath.Abs(filepath.Join("..", "..", "web", "e2e", "stub-protocol.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	home, dir := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".stub-sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := "11111111-2222-4333-8444-555555555555"
	cmd := exec.Command("node", stub, "-p", "--input-format", "stream-json", "--resume", conv)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Dir = dir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	c := NewClaude(stdin, stdout, ClaudeOptions{})
	rec := &recorder{}
	ended := make(chan event, 8)
	rec.after = func(e event) {
		if e.kind == "turn_ended" {
			ended <- e
		}
	}
	read := make(chan error, 1)
	go func() { read <- c.Read(context.Background(), rec) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := []session.Message{
		{Ref: "r1", Text: "/compact"},
		{Ref: "r2", Text: "/compact", Expand: true},
		{Ref: "r3", Text: "/paint", Expand: true},
		{Ref: "r4", Text: "/fix-issue 12", Expand: true},
		{Ref: "r5", Text: "What is in @notes.txt and @../outside.txt?", Expand: true},
		{Ref: "r6", Text: "What is in @notes.txt?"},
	}
	for _, m := range msgs {
		if err := c.Send(ctx, m); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ended:
		case <-ctx.Done():
			t.Fatalf("%s: no turn's end", m.Ref)
		}
	}
	var acc []string
	for _, e := range rec.kinds("accepted") {
		acc = append(acc, e.ref)
	}
	if strings.Join(acc, " ") != "r1 r2 r3 r4 r5 r6" {
		t.Fatalf("accepted %q\n%s", acc, rec.render())
	}
	r := rec.render()
	for _, want := range []string{`"stub chat: /compact"`, `"Compacted"`, `"/paint isn't available in this environment."`, `"stub ran: /fix-issue 12"`, `"stub attached: notes.txt"`, `"stub chat: What is in @notes.txt?"`} {
		if !strings.Contains(r, want) {
			t.Fatalf("no %s in\n%s", want, r)
		}
	}
	marks, err := os.ReadFile(filepath.Join(home, ".stub-sessions", conv+".marks"))
	if err != nil || string(marks) != "marked\nunmarked\nunmarked\nunmarked\nunmarked\nmarked\n" {
		t.Fatalf("the marks: %q %v", marks, err)
	}
	// Its own commands: listed in the initialize answer, and a change once
	// the file beside its conversation says one more.
	if got := rec.kinds("commands"); len(got) == 0 || len(got[0].cmds) != 4 || got[0].cmds[3].Name != "fix-issue" || got[0].cmds[3].Hint != "<number>" {
		t.Fatalf("the initialize answer's commands: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".stub-sessions", conv+".commands"), []byte("release|Write the notes|<version>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		if got := rec.kinds("commands"); len(got[len(got)-1].cmds) == 5 {
			if c := got[len(got)-1].cmds[4]; c.Name != "release" || c.Hint != "<version>" {
				t.Fatalf("the change: %+v", c)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no change of the commands: %+v", rec.kinds("commands"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = c.CloseInput()
	if err := <-read; !errors.Is(err, io.EOF) {
		t.Fatalf("Read: %v", err)
	}
}
