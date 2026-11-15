package hostagent

import (
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// A hosted session's sign-in screen (#902): the host's own session takes
// a controller's line to it through the answer the server relays, under
// the session's own rules. The live attention its viewers are sent marks
// the question as one that takes a line; a view role's line is refused;
// at the folder's trust question that follows, a line is refused in the
// words it has today. Every code here is invented.
func TestAHostsSignInScreenTakesAControllersLine(t *testing.T) {
	claude, _ := catalog.Default().Get("claude")
	w, err := agents.CompileRecipe(agents.Recipe(claude))
	if err != nil {
		t.Fatal(err)
	}
	proc := sessiontest.NewFakeProc()
	local := session.NewLocal(session.Info{ID: "s", Cwd: t.TempDir(), Cols: 80, Rows: 24}, proc, session.Options{
		TrustPattern: w.TrustPattern, TrustAnswers: w.TrustAnswers, Questions: w.Questions,
		SubmitPause: 30 * time.Millisecond, FileEdit: "off", Log: discardLog,
	})
	t.Cleanup(func() { proc.End(0) })
	a := &agent{opts: Options{}, local: local, peers: map[string]*peer{}, log: discardLog}
	viewer := func(id, name string, role session.Role) *answerViewer {
		p := newPeer(a, id, role, "", "", role == session.RoleControl)
		frames := make(chan []byte, 1024)
		sub, err := local.AttachWith(session.AttachOptions{ID: id, Role: role, Name: name}, sinkFunc(func(f []byte) { frames <- f }))
		if err != nil {
			t.Fatal(err)
		}
		p.sub = sub
		t.Cleanup(p.close)
		return &answerViewer{t: t, p: p, sub: sub, frames: frames}
	}
	nate := viewer("0123456789abcdef", "nate", session.RoleControl)
	guest := viewer("fedcba9876543210", "guest", session.RoleView)
	attention := func(ok func(session.Attention) bool) session.Attention {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if att := local.Info().Attention; ok(att) {
				return att
			}
			if time.Now().After(deadline) {
				t.Fatalf("attention never matched, last %+v", local.Info().Attention)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	nothingWritten := func() {
		t.Helper()
		select {
		case b := <-proc.Input:
			t.Fatalf("written %q", b)
		default:
		}
	}

	proc.Print("\x1b[2J\x1b[HWelcome to Claude Code\r\n\r\n Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\nhttps://example.invalid/sign-in?code=true\r\n\r\n\r\n Paste code here if prompted > ")
	signIn := attention(func(a session.Attention) bool { return a.Source == session.SourceTrust && a.Line })
	// The live attention a viewer's page takes its question bar from says so.
	if m := untilControl(t, nate.frames, func(m map[string]any) bool {
		return m["t"] == proto.CtlAttention && m["id"] == signIn.ID
	}); m["line"] != true {
		t.Fatalf("the live attention: %v", m)
	}

	guest.answer("g1", signIn.ID, "", "e2e-Guest-0000")
	if m := guest.verdict("g1"); m["code"] != proto.ErrCodeReadOnly {
		t.Fatalf("a view role's line: %v", m)
	}
	nothingWritten()

	nate.answer("n1", signIn.ID, "", "e2e-Code-7Hq2Wx9Kp4")
	if m := nate.verdict("n1"); m["t"] != proto.CtlAnswerOK {
		t.Fatalf("the controller's line: %v", m)
	}
	for _, want := range []string{"e2e-Code-7Hq2Wx9Kp4", "\r"} {
		select {
		case b := <-proc.Input:
			if string(b) != want {
				t.Fatalf("written %q, want %q", b, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%q not written", want)
		}
	}

	// Signed in, the folder's trust question comes next: a line to it is
	// refused in today's words, nothing typed.
	proc.Print("\x1b[2J\x1b[H Do you trust the files in this folder?\r\n\r\n Is this a project you created or one you trust?\r\n\r\n ❯ 1. Yes, I trust this folder\r\n   2. No, exit\r\n\r\n Enter to confirm · Esc to cancel")
	trust := attention(func(a session.Attention) bool { return a.Source == session.SourceTrust && len(a.Options) > 0 })
	if trust.Line {
		t.Fatalf("the trust question takes a line: %+v", trust)
	}
	nate.answer("n2", trust.ID, "", "e2e-Code-7Hq2Wx9Kp4")
	if m := nate.verdict("n2"); m["code"] != proto.ErrCodeNotSent || !strings.Contains(m["message"].(string), session.TrustQuestionWords) {
		t.Fatalf("a line to the trust question: %v", m)
	}
	nothingWritten()
}
