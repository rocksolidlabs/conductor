package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/signal"
)

// The session journal's part in the API (docs/plans/history.md, PR-1).

// recordFile is the record of id as its file holds it, once the journal
// has written what it held, and the file's bytes.
func recordFile(t *testing.T, e *testEnv, id string) (journal.Record, []byte) {
	t.Helper()
	e.srv.sessionRecords.Flush()
	return readRecordFile(t, e.srv.cfg.DataDir, id)
}

func readRecordFile(t *testing.T, dataDir, id string) (journal.Record, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, journal.Dir, journal.SessionsDir, id+".json"))
	if err != nil {
		t.Fatalf("the record of %s: %v", id, err)
	}
	var r journal.Record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, b
}

// endedRecord waits for the session's end to reach its record (a
// session's Ended closes before its status entry and its last change are
// told), then answers the record as written.
func endedRecord(t *testing.T, e *testEnv, id string) journal.Record {
	t.Helper()
	until(t, "the record's end", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && !r.EndedAt.IsZero()
	})
	r, _ := recordFile(t, e, id)
	return r
}

func entryKinds(r journal.Record) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Kind)
	}
	return out
}

// recordedFakeSession is newFakeSession with its record begun as
// createLocalSession begins a server session's, and its chat wired to the
// server's, so a test drives a real session's questions over a process in
// memory.
func recordedFakeSession(t *testing.T, e *testEnv, o session.Options) *fakeSession {
	t.Helper()
	id := session.NewID()
	info := session.Info{ID: id, Name: id, Kind: session.KindServer, AgentID: "cat", Cwd: e.root, CreatedAt: time.Now().UTC()}
	e.srv.beginServerRecord(info, session.Launched{AgentID: "cat", Cwd: e.root}, "", "cat", "", ext.WhereNative, gitcli.Revision{})
	o.OnChat = e.srv.localChat
	return newFakeSession(t, e, id, o)
}

// Every server session gets a record from its start, on disk before its
// process has said anything: its agent, its host, where it ran, its folder
// and its launch; the routes list and read it, live while it runs, and its
// end is kept.
func TestEveryServerSessionGetsARecord(t *testing.T) {
	e := newTestEnv(t, nil)
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "sh", "args": []string{"-c", "cat"}, "name": "the shell"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, id)
	if r.Name != "the shell" || r.Kind != "server" || r.Agent.ID != "sh" || r.Host.Hosted || r.Host.User != serverUser() || r.Where != "native" || r.Cwd != e.root {
		t.Fatalf("the record as begun: %+v", r)
	}
	if l := r.Launch; l == nil || !l.ArgsKept || !slices.Equal(l.Args, []string{"-c", "cat"}) || l.ArgCount != 2 {
		t.Fatalf("the launch %+v", r.Launch)
	}
	resp, list := e.do("GET", "/api/history/sessions", adminToken, nil)
	recs, _ := list["records"].([]any)
	if resp.StatusCode != http.StatusOK || len(recs) != 1 || recs[0].(map[string]any)["id"] != id {
		t.Fatalf("the list: %d %v", resp.StatusCode, list)
	}
	resp, got := e.do("GET", "/api/history/sessions/"+id, adminToken, nil)
	if resp.StatusCode != http.StatusOK || got["live"] != true || got["record"].(map[string]any)["id"] != id {
		t.Fatalf("the record read: %d %v", resp.StatusCode, got)
	}
	e.stopAndWait(id)
	r = endedRecord(t, e, id)
	if r.EndedAt.IsZero() || !strings.HasPrefix(r.Ending, "stopped") || !r.LostAt.IsZero() {
		t.Fatalf("the record's end: %+v", r)
	}
	if _, got = e.do("GET", "/api/history/sessions/"+id, adminToken, nil); got["live"] != false {
		t.Fatalf("an ended session read live: %v", got)
	}
	if resp, _ := e.do("GET", "/api/history/sessions/0123456789abcdef", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a record that is not there: %d", resp.StatusCode)
	}
}

// What a person types is never kept: a reply-box submission, keys typed
// into the terminal, a typed answer to a question; a line Conductor typed
// is kept as typed.
func TestWhatAPersonTypesIsNotKept(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := recordedFakeSession(t, e, session.Options{SubmitPause: time.Millisecond})
	id := fs.l.Info().ID
	sub := fs.owner(t, "Nate")
	ctx := t.Context()
	if _, err := fs.l.Submit(ctx, session.Submission{By: sub, Text: "reply-box-secret-1"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.l.Input(sub, []byte("terminal-keys-secret-2")); err != nil {
		t.Fatal(err)
	}
	q := fs.ask(t, "What is the password?", nil)
	if _, err := fs.l.Submit(ctx, session.Submission{By: sub, Text: "typed-answer-secret-3", Question: q}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.l.Submit(ctx, session.Submission{ByName: "Conductor", Text: "build the login page"}); err != nil {
		t.Fatal(err)
	}
	fs.written()
	until(t, "the answer's line in the chat", func() bool {
		return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == q && m.Event == "answered" })
	})
	r, raw := recordFile(t, e, id)
	for _, secret := range []string{"reply-box-secret-1", "terminal-keys-secret-2", "typed-answer-secret-3"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the record keeps %q:\n%s", secret, raw)
		}
	}
	i := slices.IndexFunc(r.Entries, func(e journal.Entry) bool { return e.Kind == journal.KindTyped })
	if i < 0 || r.Entries[i].Message != "build the login page" {
		t.Fatalf("the line Conductor typed: %+v", r.Entries)
	}
	a := slices.IndexFunc(r.Entries, func(e journal.Entry) bool { return e.Kind == journal.KindAnswer })
	if a < 0 || r.Entries[a].Ref != q || r.Entries[a].Outcome != journal.OutcomeAnswered || r.Entries[a].Message != "" {
		t.Fatalf("the typed answer's entry: %+v", r.Entries)
	}
}

// A typed answer a provider passes on (ext.Host's Submit naming the
// question, as a decision router answers one: no By, the person's name)
// never reaches the record or the session's own log: its text may be a
// credential, whoever sent it. The answer's own entry, the question's
// words, is recorded after where the line's would have been, so once it
// is in the log the line's would be too.
func TestARoutedTypedAnswerIsNotKept(t *testing.T) {
	const secret = "routed-answer-secret-4"
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	l := e.local(id)
	h := testHost(e.srv, t.Context(), "switchman")
	l.SetAttentionFull(session.AttentionNeedsInput, "What is the password?", session.SourceAPI, session.KindPrompt, nil)
	q := l.Info().Attention.ID
	if res, err := h.Submit(t.Context(), ext.SubmitRequest{Session: id, Question: q, Text: secret, By: ada}); err != nil || res.Status != "entered" {
		t.Fatalf("the routed answer: %+v %v", res, err)
	}
	until(t, "the answer's entry in the session's log", func() bool {
		return slices.Contains(inputs(l), "What is the password? by Ada")
	})
	for _, in := range inputs(l) {
		if strings.Contains(in, secret) {
			t.Fatalf("the session's log keeps the answer: %q", inputs(l))
		}
	}
	until(t, "the answer in the record", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && slices.ContainsFunc(r.Entries, func(e journal.Entry) bool { return e.Kind == journal.KindAnswer && e.Ref == q })
	})
	if _, raw := recordFile(t, e, id); bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("the record keeps the routed answer:\n%s", raw)
	}
	resp, out := e.do("GET", "/api/history/sessions/"+id, adminToken, nil)
	if b, _ := json.Marshal(out); resp.StatusCode != http.StatusOK || bytes.Contains(b, []byte(secret)) {
		t.Fatalf("the record served: %d %s", resp.StatusCode, b)
	}
}

// A typed answer a route sends a real `conductor host` session
// (answer_as, which the host types naming its question, no By) never
// reaches the hosted record: the host keeps no entry of its text, so none
// reaches the server.
func TestAHostedRoutedTypedAnswerIsNotKept(t *testing.T) {
	const secret = "hosted-answer-secret-5"
	seams := watchRouter(t)
	f := &fakeRouter{route: func(_ context.Context, d ext.Decision) (ext.Route, error) {
		return ext.Route{Answer: &ext.Answer{Text: secret, By: ada}}, nil
	}}
	e := routerEnv(t, f)
	out := filepath.Join(e.root, "read")
	id := startHost(t, e, []string{"/bin/sh", "-c", `exec cat > "$0"`, out})
	until(t, "the host says answerAs", func() bool { return hostedSession(t, e, id).TakesAnswers() })
	q := hostedQuestion(t, e, f, id, map[string]any{"state": "needs_input", "message": "What is the password?", "kind": "prompt"})
	if got := seams.wait(t, "applied", q); got.err != nil {
		t.Fatalf("applied: %v", got.err)
	}
	until(t, "the program read the answer", func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), secret)
	})
	until(t, "the answer in the record", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && slices.ContainsFunc(r.Entries, func(e journal.Entry) bool { return e.Kind == journal.KindAnswer && e.Ref == q })
	})
	seams.settle(t, e, id)
	if _, raw := recordFile(t, e, id); bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("the hosted record keeps the routed answer:\n%s", raw)
	}
}

// A question and how it ended come from the chat with their ids: one with
// choices answered by a choice (its label), one the agent moved past
// (withdrawn), each answer naming its question.
func TestQuestionsAndTheirEndsComeFromTheChat(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := recordedFakeSession(t, e, session.Options{})
	id := fs.l.Info().ID
	sub := fs.owner(t, "Nate")
	choices := []session.Option{{Label: "Postgres", Input: "1"}, {Label: "SQLite", Input: "2"}}
	q1 := fs.ask(t, "Which database?", choices)
	fs.answer(t, sub, q1, "2")
	q2 := fs.ask(t, "Which port?", nil)
	fs.report(t, session.AttentionWorking, "")
	until(t, "the second question withdrawn", func() bool {
		return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == q2 && m.Event == "withdrawn" })
	})
	until(t, "the first answered", func() bool {
		return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == q1 && m.Event == "answered" })
	})
	r, _ := recordFile(t, e, id)
	byID := map[string]journal.Entry{}
	var answers []journal.Entry
	for _, en := range r.Entries {
		switch en.Kind {
		case journal.KindQuestion:
			byID[en.ID] = en
		case journal.KindAnswer:
			answers = append(answers, en)
		}
	}
	if q := byID[q1]; q.Message != "Which database?" || !slices.Equal(q.Choices, []string{"Postgres", "SQLite"}) {
		t.Fatalf("the first question %+v", q)
	}
	if _, ok := byID[q2]; !ok {
		t.Fatalf("the second question is not kept: %+v", r.Entries)
	}
	if len(answers) != 2 || answers[0].Ref != q1 || answers[0].Outcome != journal.OutcomeAnswered || answers[0].Message != "SQLite" || answers[0].ByName != "Nate" ||
		answers[1].Ref != q2 || answers[1].Outcome != journal.OutcomeWithdrawn {
		t.Fatalf("the answers %+v", answers)
	}
	for _, m := range fs.l.ChatHistory() {
		if m.Kind == "question" && byID[m.ID].At.IsZero() {
			t.Fatalf("the chat's question %s is not in the record", m.ID)
		}
	}
}

// The chat is kept: what a person posted, and the mark that it was sent
// to the agent, naming the message.
func TestTheChatIsKeptWithWhatWasSentToTheAgent(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := recordedFakeSession(t, e, session.Options{SubmitPause: time.Millisecond})
	id := fs.l.Info().ID
	sub := fs.owner(t, "Nate")
	m, err := fs.l.Chat(sub, proto.ChatPost{Text: "look at the login redirect test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.l.ChatSend(t.Context(), sub, proto.ChatSend{Ref: m.ID}); err != nil {
		t.Fatal(err)
	}
	fs.written()
	r, _ := recordFile(t, e, id)
	var posted, sent *journal.Entry
	for i := range r.Entries {
		en := &r.Entries[i]
		switch {
		case en.Kind == journal.KindChat && en.ID == m.ID:
			posted = en
		case en.Kind == journal.KindChat && en.Event == "sent_to_agent":
			sent = en
		}
	}
	if posted == nil || posted.Message != "look at the login redirect test" || posted.ByName != "Nate" {
		t.Fatalf("the message %+v in %+v", posted, r.Entries)
	}
	if sent == nil || sent.Ref != m.ID || sent.ByName != "Nate" {
		t.Fatalf("the mark %+v", sent)
	}
	if slices.ContainsFunc(r.Entries, func(en journal.Entry) bool { return strings.Contains(en.Message, "joined") }) {
		t.Fatalf("a join line is kept: %+v", r.Entries)
	}
}

// A question answered by keys typed in the terminal is answered there, no
// keys kept, never withdrawn; one the agent moved past is withdrawn.
func TestATerminalAnswerIsNotAWithdrawal(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := recordedFakeSession(t, e, session.Options{Questions: codexQuestions(t)})
	id := fs.l.Info().ID
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	a := shownDialog(t, fs)
	if err := fs.l.Input(fs.owner(t, "Nate"), []byte("y")); err != nil {
		t.Fatal(err)
	}
	until(t, "answered in the terminal", func() bool {
		at := fs.l.Info().Attention
		return at.ID == a.ID && at.Answered == session.AnsweredTerminal
	})
	if err := fs.proc.Print(codexApprovalGone); err != nil {
		t.Fatal(err)
	}
	until(t, "the dialog gone", func() bool {
		return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == a.ID && m.Event == "withdrawn" })
	})
	q := fs.ask(t, "Which port?", nil)
	fs.report(t, session.AttentionWorking, "")
	until(t, "the question moved past", func() bool {
		return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == q && m.Event == "withdrawn" })
	})
	r, _ := recordFile(t, e, id)
	outcomes := map[string]journal.Entry{}
	for _, en := range r.Entries {
		if en.Kind == journal.KindAnswer {
			outcomes[en.Ref] = en
		}
	}
	if o := outcomes[a.ID]; o.Outcome != journal.OutcomeAnsweredInTerminal || o.Message != "" {
		t.Fatalf("the terminal's answer %+v", o)
	}
	if o := outcomes[q]; o.Outcome != journal.OutcomeWithdrawn {
		t.Fatalf("the question moved past %+v", o)
	}
}

// registerHost registers a hosted session on e's hub as a host would, under
// instance and localID.
func registerHost(t *testing.T, e *testEnv, instance, localID string) (*signal.HostedSession, *signal.HostConn) {
	t.Helper()
	conn := signal.NewHostConn()
	hs, _, err := e.srv.hosts.Register(proto.Register{T: proto.HostRegister, Proto: proto.ProtoVersion,
		Host:    proto.HostInfo{Name: "laptop", User: "ada", Instance: instance},
		Session: proto.HostSession{AgentID: "claude", Command: []string{"claude"}, Cwd: e.root, Branch: "main", Cols: 80, Rows: 24, LocalID: localID}}, conn)
	if err != nil {
		t.Fatal(err)
	}
	return hs, conn
}

// The host's chat, through HostChat as the host's messages reach it: the
// agent's question with its choices, its answered line, a line for keys
// typed in the terminal and one for the agent moving on, each in the record
// with its ids; refused or cut: the agent's role on a plain message, a
// seventh choice, a label past 60 runes, a withdrawn line's own words.
func TestAHostedQuestionAndItsEndsReachTheRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	agent := proto.ChatBy{ID: "agent", Name: "claude", Role: "agent"}
	ada := proto.ChatBy{ID: "v1", Name: "Ada", Role: "control"}
	var opts []proto.AttentionOption
	for i := range 7 {
		opts = append(opts, proto.AttentionOption{Label: fmt.Sprintf("choice %d %s", i, strings.Repeat("long ", 20)), Input: fmt.Sprint(i + 1)})
	}
	at := time.Now().UTC()
	stamp := func(n int) string { return at.Add(time.Duration(n) * time.Second).Format(time.RFC3339Nano) }
	hs.HostChat(proto.ChatMessage{ID: "q1", At: stamp(1), Kind: "question", By: agent, Text: "Allow Bash?", Options: opts})
	hs.HostChat(proto.ChatMessage{ID: "a1", At: stamp(2), Kind: "system", Event: "answered", Ref: "q1", Text: "choice 0", By: ada})
	hs.HostChat(proto.ChatMessage{ID: "q2", At: stamp(3), Kind: "question", By: agent, Text: "Allow Edit?"})
	hs.HostChat(proto.ChatMessage{ID: "w2", At: stamp(4), Kind: "system", Event: "withdrawn", Ref: "q2", Text: "terminal", By: proto.ChatBy{Name: "Conductor", Role: "control"}})
	hs.HostChat(proto.ChatMessage{ID: "q3", At: stamp(5), Kind: "question", By: agent, Text: "Allow Write?"})
	hs.HostChat(proto.ChatMessage{ID: "w3", At: stamp(6), Kind: "system", Event: "withdrawn", Ref: "q3", Text: "agent", By: proto.ChatBy{Name: "Conductor", Role: "control"}})
	hs.HostChat(proto.ChatMessage{ID: "w4", At: stamp(7), Kind: "system", Event: "withdrawn", Ref: "q1", Text: "the host's own words", By: proto.ChatBy{Name: "Conductor", Role: "control"}})
	hs.HostChat(proto.ChatMessage{ID: "m1", At: stamp(8), Kind: "message", By: agent, Text: "the agent posting as a person"})
	r, raw := recordFile(t, e, id)
	if bytes.Contains(raw, []byte("the host's own words")) || bytes.Contains(raw, []byte("the agent posting")) {
		t.Fatalf("the record keeps what was refused:\n%s", raw)
	}
	if want := []string{"question", "answer", "question", "answer", "question", "answer", "answer"}; !slices.Equal(entryKinds(r), want) {
		t.Fatalf("kinds %v, want %v", entryKinds(r), want)
	}
	q1 := r.Entries[0]
	if q1.ID != "q1" || len(q1.Choices) != session.MaxAttentionOptions {
		t.Fatalf("the first question %+v", q1)
	}
	for _, c := range q1.Choices {
		if n := len([]rune(c)); n > session.MaxOptionLabel {
			t.Fatalf("a choice of %d runes", n)
		}
	}
	got := []string{}
	for _, en := range r.Entries {
		if en.Kind == journal.KindAnswer {
			got = append(got, en.Ref+" "+en.Outcome)
		}
	}
	if want := []string{"q1 answered", "q2 answered_in_terminal", "q3 withdrawn", "q1 withdrawn"}; !slices.Equal(got, want) {
		t.Fatalf("answers %q, want %q", got, want)
	}
}

// commitFirst is the native Runtime with a commit made in the folder
// before the agent's process starts, as an agent that commits at once
// would.
type commitFirst struct {
	ext.Runtime
	t *testing.T
}

func (c commitFirst) StartTerminal(ctx context.Context, s ext.TerminalSpec) (ext.Terminal, error) {
	gitIn(c.t, s.Dir, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "the agent's first commit")
	return c.Runtime.StartTerminal(ctx, s)
}

// The launch revision is read before the agent's process starts: the
// record's start commit is the one before the agent's first, with its
// repository, its common directory and its branch.
func TestTheStartCommitIsReadBeforeTheAgentRuns(t *testing.T) {
	e := newTestEnvAgents(t, nil, nil, nil, withNativeRuntime(commitFirst{Runtime: local.Native(), t: t}))
	e.gitRepo(t)
	before := gitIn(t, e.root, "rev-parse", "HEAD")
	id := e.createSession("cat")
	after := gitIn(t, e.root, "rev-parse", "HEAD")
	if after == before {
		t.Fatal("the agent's commit was not made")
	}
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, id)
	if r.Start != before || r.StartFrom != journal.StartFromLaunch || r.Repo != e.root || r.RepoCommon != filepath.Join(e.root, ".git") || r.Branch == "" {
		t.Fatalf("the launch revision: start %s (before %s, after %s) from %q repo %q common %q branch %q", r.Start, before, after, r.StartFrom, r.Repo, r.RepoCommon, r.Branch)
	}
	e.stopAndWait(id)
	if r = endedRecord(t, e, id); r.End != after {
		t.Fatalf("the end commit %q, want %q", r.End, after)
	}
}

// A managed run's attempt takes its start from the ledger (its attempt's
// own start commit), read by the index, never from a git read at launch:
// its record says so, with its run, task and attempt.
func TestACrewMembersStartIsItsAttempts(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	runID := launchOne(t, e, oneTaskCrew("solo", e.root))
	sid := runningAttempt(t, e, runID, "build", 1)["sessionId"].(string)
	r, _ := recordFile(t, e, sid)
	if r.StartFrom != journal.StartFromAttempt || r.Start != "" {
		t.Fatalf("the start of an attempt: %q from %q", r.Start, r.StartFrom)
	}
	if c := r.Crew; c == nil || c.RunID != runID || c.Task != "build" || c.Attempt != 1 || c.Member != "lead" {
		t.Fatalf("the attempt's crew %+v", r.Crew)
	}
}

// A session started in a Runtime that is not native gets no git read on
// the server: its Where is kept, its repository and commits unknown.
func TestANonNativeSessionGetsNoGitReadOnTheServer(t *testing.T) {
	boxcar := &sessiontest.FakeRuntime{Place: "boxcar", Base: local.Native()}
	e := newTestEnvAgents(t, nil, nil, nil, withRuntime(boxcar))
	e.gitRepo(t)
	var reads atomic.Int32
	e.srv.readRevision = func(ctx context.Context, dir string) (gitcli.Revision, error) {
		reads.Add(1)
		return gitcli.RevisionOf(ctx, dir)
	}
	l, aerr := e.srv.createLocalSession(createSessionRequest{AgentID: "cat", where: "boxcar"}, nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	id := l.Info().ID
	t.Cleanup(func() { e.stopAndWait(id) })
	r, _ := recordFile(t, e, id)
	if r.Where != "boxcar" || r.Repo != "" || r.RepoCommon != "" || r.Start != "" || r.Branch != "" || r.StartFrom != "" {
		t.Fatalf("the record of a session on boxcar: %+v", r)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("the server read git %d times", n)
	}
	// A native one beside it is read.
	native := e.createSession("cat")
	if r, _ := recordFile(t, e, native); r.Start == "" || reads.Load() != 1 {
		t.Fatalf("a native session's start %q after %d reads", r.Start, reads.Load())
	}
}

// A hosted session gets a record at its add: its host's name, user and
// owner, its entries through the hub's activity, its chat through the
// hub's chat, its changes through the hub's OnChange.
func TestAHostedSessionGetsARecord(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	hs.HostActivity(proto.Activity{Type: "progress", Message: "built it", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	hs.HostActivity(proto.Activity{Type: "file", Op: "edit", Path: "main.go", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	hs.HostChat(proto.ChatMessage{ID: "m1", Kind: "message", By: proto.ChatBy{ID: "v", Name: "Ada", Role: "control"}, Text: "nice"})
	r, _ := recordFile(t, e, id)
	if !r.Host.Hosted || r.Host.Name != "laptop" || r.Host.User != "ada" || r.Host.Owner != hs.Owner() || r.Kind != "hosted" || r.Agent.ID != "claude" || r.Cwd != e.root || r.Branch != "main" {
		t.Fatalf("the hosted record %+v", r)
	}
	if want := []string{journal.KindReport, journal.KindChat}; !slices.Equal(entryKinds(r), want) {
		t.Fatalf("kinds %v", entryKinds(r))
	}
	if len(r.Files) != 1 || r.Files[0].Path != filepath.Join(e.root, "main.go") {
		t.Fatalf("files %+v", r.Files)
	}
	exit := 0
	hs.HostStatus(session.StatusExited, &exit)
	if r, _ = recordFile(t, e, id); r.Ending != "exited (exit 0)" || r.EndedAt.IsZero() {
		t.Fatalf("its end from its change: %+v", r)
	}
}

// A hosted record borrows nothing of the server's machine: no agent
// version from the server's probe, no agent session, no repository and no
// commit, though the hosted folder is a repository on the server's disk.
func TestAHostedRecordBorrowsNothingFromTheServer(t *testing.T) {
	e := newTestEnv(t, nil)
	e.gitRepo(t)
	var reads atomic.Int32
	e.srv.readRevision = func(ctx context.Context, dir string) (gitcli.Revision, error) {
		reads.Add(1)
		return gitcli.RevisionOf(ctx, dir)
	}
	hs, conn := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	exit := 0
	hs.HostStatus(session.StatusExited, &exit)
	hs.HostDisconnected(conn)
	r, _ := recordFile(t, e, id)
	if r.Agent.Version != "" || r.Agent.Session != nil || r.Repo != "" || r.RepoCommon != "" || r.Start != "" || r.End != "" || r.Where != "" || r.Launch != nil || reads.Load() != 0 {
		t.Fatalf("the hosted record took the server's facts (%d git reads): %+v", reads.Load(), r)
	}
}

// restartServer shuts e's server down and starts another over its store.
func restartServer(t *testing.T, e *testEnv) *testEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
	return e.restart()
}

// A hosted record outlives a restart of the server: marked host away as
// the new server starts, then taken up again when the same host registers
// again under the same instance and local id, one record, the entries from
// before and after in order, its Seq never back.
func TestAHostedRecordSurvivesAServerRestart(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	hs.HostActivity(proto.Activity{Type: "progress", Message: "before", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	before, _ := recordFile(t, e, id)
	e2 := restartServer(t, e)
	away, _ := readRecordFile(t, e2.srv.cfg.DataDir, id)
	if away.Ending != journal.EndingHostAway || !away.EndedAt.IsZero() || away.LostAt.IsZero() {
		t.Fatalf("after the restart: %+v", away)
	}
	again, _ := registerHost(t, e2, "instance-a", "1")
	if again.Info().ID != id {
		t.Fatalf("the id changed: %s", again.Info().ID)
	}
	again.HostActivity(proto.Activity{Type: "progress", Message: "after", At: time.Now().UTC().Format(time.RFC3339Nano)}, "")
	r, _ := recordFile(t, e2, id)
	var msgs []string
	for _, en := range r.Entries {
		msgs = append(msgs, en.Message)
	}
	if r.Reopened != 1 || r.Ending != "" || !r.LostAt.IsZero() || r.Seq <= away.Seq || away.Seq <= before.Seq || !slices.Equal(msgs, []string{"before", "taken up again by its host", "after"}) {
		t.Fatalf("taken up again: reopened %d ending %q seq %d (away %d, before %d) entries %q", r.Reopened, r.Ending, r.Seq, away.Seq, before.Seq, msgs)
	}
}

// A hosted session the hub lets go past DisconnectGrace is marked host
// away, which proves nothing of its process, and taken up again when its
// host registers once more.
func TestAHostedRecordLetGoByExpiryIsReopened(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, conn := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	hs.HostDisconnected(conn)
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
	if _, ok := e.srv.registry.Get(id); ok {
		t.Fatal("the hosted session was not let go")
	}
	r, _ := recordFile(t, e, id)
	if r.Ending != journal.EndingHostAway || !r.EndedAt.IsZero() || r.LostAt.IsZero() {
		t.Fatalf("let go: %+v", r)
	}
	registerHost(t, e, "instance-a", "1")
	if r, _ = recordFile(t, e, id); r.Reopened != 1 || r.Ending != "" || !r.LostAt.IsZero() {
		t.Fatalf("taken up again: %+v", r)
	}
}

// A hosted session's exit is kept from its Info when its status activity
// entry never arrived (dropped at its host's full queue): the record ends
// as the status message says, and the hub letting it go after leaves that
// end.
func TestAHostedExitIsKeptWithItsActivityDropped(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, conn := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	exit := 0
	hs.HostStatus(session.StatusExited, &exit)
	hs.HostDisconnected(conn)
	e.srv.hosts.Expire(time.Now().Add(signal.DisconnectGrace + time.Second))
	r, _ := recordFile(t, e, id)
	if r.Ending != "exited (exit 0)" || r.EndedAt.IsZero() || !r.LostAt.IsZero() {
		t.Fatalf("the hosted exit: %+v", r)
	}
}

// A record outlives its server: the server ended and another started on
// the same data directory, the record is listed, ended.
func TestTheRecordSurvivesARestart(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	e2 := restartServer(t, e)
	resp, list := e2.do("GET", "/api/history/sessions", adminToken, nil)
	recs, _ := list["records"].([]any)
	if resp.StatusCode != http.StatusOK || len(recs) != 1 {
		t.Fatalf("the list after the restart: %d %v", resp.StatusCode, list)
	}
	row := recs[0].(map[string]any)
	if row["id"] != id || row["endedAt"] == nil || !strings.HasPrefix(row["ending"].(string), "stopped") {
		t.Fatalf("the row %v", row)
	}
	if resp, got := e2.do("GET", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK || got["live"] != false {
		t.Fatalf("the record after the restart: %d %v", resp.StatusCode, got)
	}
}

// A session that forms a crew around itself after its launch has its crew
// in its record at once, without waiting for a flush, and it is still
// there after the server restarted.
func TestACrewAdoptedAfterLaunchIsRecorded(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	tok := agentOf(t, e, id)
	resp, out := e.do("POST", "/api/sessions/"+id+"/crew", tok, crewBody("lead", memberDef("lead", "cat", "manual")))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("form a crew: %d %v", resp.StatusCode, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	until(t, "the crew in the record's file", func() bool {
		b, err := os.ReadFile(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json"))
		return err == nil && bytes.Contains(b, []byte(`"runId": "`+runID+`"`))
	})
	e.stopAndWait(id)
	e2 := restartServer(t, e)
	r, _ := readRecordFile(t, e2.srv.cfg.DataDir, id)
	if r.Crew == nil || r.Crew.RunID != runID || r.Crew.Member != "lead" || r.EndedAt.IsZero() {
		t.Fatalf("the crew after the restart: %+v", r)
	}
}

// The History routes are the workbench's alone: a share link, a session's
// own token and no token reach none of them; Forget refuses a live session
// and forgets an ended one.
func TestHistorySessionRoutesAreAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, link := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "control"})
	linkToken, _ := link["token"].(string)
	if linkToken == "" {
		t.Fatalf("no link: %v", link)
	}
	agent := e.agentToken(id)
	for _, tok := range []string{"", linkToken, agent} {
		for _, r := range []struct{ method, path string }{
			{"GET", "/api/history/sessions"}, {"GET", "/api/history/sessions/" + id},
			{"DELETE", "/api/history/sessions/" + id}, {"DELETE", "/api/history/sessions"},
		} {
			if resp, _ := e.do(r.method, r.path, tok, nil); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s with %q: %d", r.method, r.path, tok, resp.StatusCode)
			}
		}
	}
	if resp, out := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusConflict || errorCode(out) != "live_session" {
		t.Fatalf("forget a live session: %d %v", resp.StatusCode, out)
	}
	e.stopAndWait(id)
	if resp, out := e.do("DELETE", "/api/history/sessions", adminToken, nil); resp.StatusCode != http.StatusOK || out["forgotten"] != float64(1) || out["kept"] != float64(0) {
		t.Fatalf("forget all: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("forget a forgotten one: %d", resp.StatusCode)
	}
	if resp, out := e.do("GET", "/api/history/sessions?limit=0", adminToken, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a limit of 0: %d %v", resp.StatusCode, out)
	}
	if resp, out := e.do("GET", "/api/history/sessions?after=nope", adminToken, nil); resp.StatusCode != http.StatusBadRequest || errorCode(out) != "bad_cursor" {
		t.Fatalf("a bad cursor: %d %v", resp.StatusCode, out)
	}
}

// With history.sessions off no record is begun, and the records kept are
// still listed and forgettable.
func TestHistorySessionsOffKeepsNoRecord(t *testing.T) {
	off := false
	e := newTestEnv(t, func(c *config.Config) { c.History.Sessions = &off })
	id := e.createSession("cat")
	hs, _ := registerHost(t, e, "instance-a", "1")
	e.srv.sessionRecords.Flush()
	for _, sid := range []string{id, hs.Info().ID} {
		if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, sid+".json")); !os.IsNotExist(err) {
			t.Fatalf("a record of %s: %v", sid, err)
		}
	}
	if resp, out := e.do("GET", "/api/history/sessions", adminToken, nil); resp.StatusCode != http.StatusOK || len(out["records"].([]any)) != 0 {
		t.Fatalf("the list: %d %v", resp.StatusCode, out)
	}
}

// A switchyard keeps no history: no journal, nothing under history/, and
// the routes answer history_unavailable.
func TestASwitchyardKeepsNoHistory(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if e.srv.sessionRecords != nil {
		t.Fatal("a switchyard opened the journal")
	}
	registerHost(t, e, "instance-a", "1")
	if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, journal.Dir)); !os.IsNotExist(err) {
		t.Fatalf("history/ on a switchyard: %v", err)
	}
	if resp, out := e.do("GET", "/api/history/sessions", adminToken, nil); resp.StatusCode != http.StatusNotFound || errorCode(out) != "history_unavailable" {
		t.Fatalf("the list on a switchyard: %d %v", resp.StatusCode, out)
	}
}

// The end commit is read at the folder's path once the process is gone,
// and kept only while that path is still the repository the launch was
// read in: a folder replaced by another repository lends no commit.
func TestTheEndCommitIsTheLaunchRepositorys(t *testing.T) {
	e := newTestEnv(t, nil)
	answer := gitcli.Revision{Top: "/w", Common: "/w/.git", Commit: "0123456789abcdef0123456789abcdef01234567"}
	e.srv.readRevision = func(context.Context, string) (gitcli.Revision, error) { return answer, nil }
	r := journal.Record{Where: string(ext.WhereNative), Cwd: "/w", Repo: "/w", RepoCommon: "/w/.git"}
	if got := e.srv.recordEndCommit(t.Context(), r); got != answer.Commit {
		t.Fatalf("the same repository's end commit: %q", got)
	}
	for _, other := range []gitcli.Revision{
		{Top: "/elsewhere", Common: "/elsewhere/.git", Commit: answer.Commit},
		{Top: "/w", Common: "/other/.git", Commit: answer.Commit},
	} {
		answer = other
		if got := e.srv.recordEndCommit(t.Context(), r); got != "" {
			t.Fatalf("a replaced folder's commit was kept: %+v gave %q", other, got)
		}
	}
}
