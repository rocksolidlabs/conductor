package agentproto

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/pty"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
)

// pipeChild is a chat's child for a driver on io.Pipes: done once stopped.
type pipeChild struct {
	once sync.Once
	done chan struct{}
}

func (c *pipeChild) Done() <-chan struct{} { return c.done }
func (c *pipeChild) Exit() pty.ExitStatus  { return pty.ExitStatus{Code: -1} }
func (c *pipeChild) Stop(context.Context, time.Duration) error {
	c.once.Do(func() { close(c.done) })
	return nil
}

type quiet struct{}

func (quiet) WriteFrame([]byte) error { return nil }
func (quiet) Close(error)             {}

// codexSession is a session in the protocol way whose driver is the Codex
// driver on a fake app server, on the fixtures' thread, a turn under way.
func codexSession(t *testing.T) (*session.Local, *codexServer) {
	t.Helper()
	s := session.NewLocal(session.Info{ID: "sess", Kind: session.KindServer, Cwd: t.TempDir(), Cols: 80, Rows: 24,
		AgentSession: &session.AgentSession{ID: fixtureThread, Resumable: true}}, sessiontest.NewFakeProc(), session.Options{ScrollbackBytes: 4096})
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	var srv *codexServer
	start := func(_ context.Context, conv string) (session.Child, session.Protocol, error) {
		c, fake := codexPipes(t, CodexOptions{Resume: CodexResume{Thread: conv}})
		srv = fake
		return &pipeChild{done: make(chan struct{})}, c, nil
	}
	if err := sessiontest.EnterProtocol(s, start); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(context.Background(), session.Submission{Text: "go"}); err != nil && !errors.Is(err, session.ErrSubmitPending) {
		t.Fatal(err)
	}
	// The turn under way once the session applied its start: what the test
	// writes after goes behind the fake's own answers on the one pipe.
	entriesNow(t, s, "the turn's start", func(es []session.TranscriptEntry) bool {
		for _, e := range es {
			if e.Kind == session.EntryTurn && e.Phase == "start" {
				return true
			}
		}
		return false
	})
	return s, srv
}

// entriesNow is the session's transcript, waited for until ok holds.
func entriesNow(t *testing.T, s *session.Local, what string, ok func([]session.TranscriptEntry) bool) []session.TranscriptEntry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		es, _ := s.Transcript(0, session.MaxTranscriptPage)
		if ok(es) {
			return es
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s: %+v", what, es)
		}
		time.Sleep(time.Millisecond)
	}
}

func requestOf(es []session.TranscriptEntry, ref string) (session.TranscriptEntry, bool) {
	for _, e := range es {
		if e.Kind == session.EntryPermission && e.Ref == ref {
			return e, true
		}
	}
	return session.TranscriptEntry{}, false
}

func stepOf(es []session.TranscriptEntry, ref string) session.TranscriptEntry {
	for _, e := range es {
		if e.Kind == session.EntryTool && e.Ref == ref {
			return e
		}
	}
	return session.TranscriptEntry{}
}

// Through the driver and its fake app server, a command's approval and a
// file change's approval are each the session's question bound to their
// item: the request's call is the item's id, its tool the name the item was
// announced under, its digest the announced step's; the operator's answer
// reaches the server for that request's id. A command's approval that names
// a folder other than its item's, and a file change's that asks for a root
// to write under, are bound to no call and denied, the server told decline.
func TestCodexApprovalsAreTheSessionsQuestionBoundToTheirItem(t *testing.T) {
	s, srv := codexSession(t)
	srv.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","item":{"type":"commandExecution","id":"call-1","command":"make test","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
	srv.write(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","itemId":"call-1","command":"make test","cwd":"/work","startedAtMs":1}}`)
	srv.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","item":{"type":"fileChange","id":"call-2","changes":[{"path":"a.txt","kind":{"type":"update","move_path":null},"diff":"-a\n+b\n"}],"status":"inProgress"}}}`)
	srv.write(`{"id":8,"method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","itemId":"call-2","startedAtMs":1}}`)
	es := entriesNow(t, s, "both requests", func(es []session.TranscriptEntry) bool {
		_, a := requestOf(es, "codex-req-1")
		_, b := requestOf(es, "codex-req-2")
		return a && b
	})
	for ref, want := range map[string]struct{ call, tool string }{"codex-req-1": {"call-1", "commandExecution"}, "codex-req-2": {"call-2", "fileChange"}} {
		r, _ := requestOf(es, ref)
		step := stepOf(es, want.call)
		if r.State != session.PermissionOpen || r.Call != want.call || r.Tool != want.tool || r.Question == "" || r.InputSha256 != step.InputSha256 || step.Tool != want.tool {
			t.Fatalf("%s: the request %+v, its step %+v", ref, r, step)
		}
	}
	if r, _ := requestOf(es, "codex-req-1"); r.Cwd != "/work" || r.Plain != "command" {
		t.Fatalf("the command's request %+v", r)
	}

	// The operator's answers reach the server for each request's own id.
	owner, err := s.AttachWith(session.AttachOptions{Role: session.RoleControl, Name: "Ada", Owner: true}, quiet{})
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range []string{"codex-req-1", "codex-req-2"} {
		r, _ := requestOf(es, ref)
		choice := proto.PermissionAllowOnce
		if i == 1 {
			choice = proto.PermissionDeny
		}
		if err := s.AnswerPermission(context.Background(), owner, proto.PermissionAnswer{T: proto.CtlPermissionAnswer, ReqID: "a" + ref, Question: r.Question, Call: r.Call, Sha256: r.InputSha256, Choice: choice}); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.await(`"id":7,`); got != `{"id":7,"result":{"decision":"accept"}}` {
		t.Fatalf("the command's answer %s", got)
	}
	if got := srv.await(`"id":8,`); got != `{"id":8,"result":{"decision":"decline"}}` {
		t.Fatalf("the file change's answer %s", got)
	}

	// What its item did not show is bound to no call.
	srv.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","item":{"type":"commandExecution","id":"call-3","command":"ls","cwd":"/work","status":"inProgress","commandActions":[]}}}`)
	srv.write(`{"id":9,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","itemId":"call-3","cwd":"/","startedAtMs":1}}`)
	srv.write(`{"method":"item/started","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","item":{"type":"fileChange","id":"call-4","changes":[{"path":"b.txt","kind":{"type":"add"},"diff":"x\n"}],"status":"inProgress"}}}`)
	srv.write(`{"id":10,"method":"item/fileChange/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","itemId":"call-4","grantRoot":"/","startedAtMs":1}}`)
	srv.write(`{"id":11,"method":"item/commandExecution/requestApproval","params":{"threadId":"` + fixtureThread + `","turnId":"turn-test-1","itemId":"call-never","command":"ls","cwd":"/work","startedAtMs":1}}`)
	for id, ref := range map[string]string{"9": "codex-req-3", "10": "codex-req-4", "11": "codex-req-5"} {
		if got := srv.await(`"id":` + id + `,`); got != `{"id":`+id+`,"result":{"decision":"decline"}}` {
			t.Fatalf("request %s's answer %s", id, got)
		}
		es := entriesNow(t, s, ref, func(es []session.TranscriptEntry) bool { _, ok := requestOf(es, ref); return ok })
		if r, _ := requestOf(es, ref); r.State != session.PermissionDenied || r.Why != session.NotACallWords || r.Question != "" {
			t.Fatalf("%s: %+v", ref, r)
		}
	}
	if n := strings.Count(strings.Join(srv.written(), "\n"), `"accept"`); n != 1 {
		t.Fatalf("%d accepts: only the operator's Allow is one", n)
	}
}
