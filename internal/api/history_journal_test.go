package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
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

// recordedLocal is a server session over proc in e's registry with its
// record begun as createLocalSession begins one, its hooks (o's, else the
// server's) feeding it after them (recordHooks): a hook o holds holds the
// record's part too. Its registration completes as createLocalSession's
// does (registered).
func recordedLocal(t *testing.T, e *testEnv, proc session.Process, o session.Options) *session.Local {
	t.Helper()
	return recordedLocalWith(t, e, proc, o, nil)
}

// recordedLocalWith is recordedLocal, with between run once the session is
// made and before the registry holds it.
func recordedLocalWith(t *testing.T, e *testEnv, proc session.Process, o session.Options, between func(*session.Local)) *session.Local {
	t.Helper()
	id := session.NewID()
	info := session.Info{ID: id, Name: id, Kind: session.KindServer, AgentID: "cat", Cwd: e.root, Cols: 80, Rows: 24, Status: session.StatusRunning, CreatedAt: time.Now().UTC()}
	rc := e.srv.beginServerRecord(info, session.Launched{AgentID: "cat", Cwd: e.root}, "", "cat", "", ext.WhereNative, gitcli.Revision{}, nil)
	if rc == nil {
		t.Fatal("no record begun")
	}
	if o.ScrollbackBytes == 0 {
		o.ScrollbackBytes = 4096
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.OnChange == nil {
		o.OnChange = e.srv.localChange
	}
	if o.OnActivity == nil {
		o.OnActivity = e.srv.localActivity
	}
	if o.OnChat == nil {
		o.OnChat = e.srv.localChat
	}
	e.srv.recordHooks(rc, &o)
	l := session.NewLocal(info, proc, o)
	if between != nil {
		between(l)
	}
	e.srv.made(rc, l)
	if err := e.srv.registry.Add(l); err != nil {
		t.Fatal(err)
	}
	e.srv.registered(rc)
	e.srv.router.mark(id)
	return l
}

// recordedFakeSession is a fake session (newFakeSession) recorded as
// createLocalSession records a server session (recordedLocal), so a test
// drives a real session's questions over a process in memory.
func recordedFakeSession(t *testing.T, e *testEnv, o session.Options) *fakeSession {
	t.Helper()
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	o.InputWait = testWait
	fs := &fakeSession{proc: proc}
	fs.l = recordedLocal(t, e, proc, o)
	return fs
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

// A hosted session gets no record (PR-12 gives it one): a host registers
// a session, reports activity and a chat line, and ends it; no record file
// is made, no route lists or reads it, and the session's own page answers
// as it does without the journal.
func TestAHostedSessionGetsNoRecord(t *testing.T) {
	e := newTestEnv(t, nil)
	hs, _ := registerHost(t, e, "instance-a", "1")
	id := hs.Info().ID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	hs.HostActivity(proto.Activity{Type: "progress", Message: "built", At: now}, "")
	hs.HostChat(proto.ChatMessage{ID: "m1", Kind: proto.ChatKindMessage, By: proto.ChatBy{Role: string(session.RoleAgent), Name: "claude"}, Text: "done", At: now})
	if resp, got := e.do("GET", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("the hosted session's own page: %d %v", resp.StatusCode, got)
	}
	code := 0
	hs.HostStatus(session.StatusExited, &code)
	e.srv.sessionRecords.Flush()
	if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, journal.Dir, journal.SessionsDir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("a hosted session has a record file: %v", err)
	}
	resp, list := e.do("GET", "/api/history/sessions", adminToken, nil)
	if recs, _ := list["records"].([]any); resp.StatusCode != http.StatusOK || len(recs) != 0 {
		t.Fatalf("the list: %d %v", resp.StatusCode, list)
	}
	if resp, _ := e.do("GET", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the hosted session's record read: %d", resp.StatusCode)
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

// sessionIn starts a cat session in cwd and answers its id; it is stopped
// when the test ends.
func sessionIn(t *testing.T, e *testEnv, cwd string) string {
	t.Helper()
	resp, out := e.do("POST", "/api/sessions", adminToken, map[string]any{"agentId": "cat", "cwd": cwd})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create in %s: %d %v", cwd, resp.StatusCode, out)
	}
	id := out["id"].(string)
	t.Cleanup(func() {
		if d, ok := e.srv.registry.Get(id); ok {
			_ = d.Stop(context.Background())
		}
	})
	return id
}

// repoAt makes dir a repository with one commit, answering its id.
func repoAt(t *testing.T, dir, what string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	return commitAt(t, dir, what)
}

// commitAt commits in dir, answering the commit's id.
func commitAt(t *testing.T, dir, what string) string {
	t.Helper()
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", what)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// The end commit is the launch repository's, found by what the session's
// folder is, never by the text of a path: a session started through a
// link has its repository's commit; one whose folder was moved away and
// another repository made at its path has its own folder's commit where
// the system reads a folder through its handle (Linux), and none
// elsewhere, never the other's; one whose repository was made again at
// the same path, the paths alike to the letter, has none.
func TestTheEndCommitIsTheLaunchRepositorys(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	e := newTestEnv(t, nil)
	t.Run("through a link", func(t *testing.T) {
		real := filepath.Join(e.root, "linked-real")
		repoAt(t, real, "first")
		link := filepath.Join(e.root, "linked")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		id := sessionIn(t, e, link)
		head := commitAt(t, real, "during the session")
		e.stopAndWait(id)
		if r := endedRecord(t, e, id); r.End != head {
			t.Fatalf("the end commit %q, want %q", r.End, head)
		}
	})
	t.Run("its folder moved, another put at its path", func(t *testing.T) {
		w := filepath.Join(e.root, "moved")
		repoAt(t, w, "first")
		id := sessionIn(t, e, w)
		head := commitAt(t, w, "during the session")
		if err := os.Rename(w, w+".was"); err != nil {
			t.Fatal(err)
		}
		other := repoAt(t, w, "another repository")
		e.stopAndWait(id)
		r := endedRecord(t, e, id)
		want := head
		if runtime.GOOS != "linux" {
			want = ""
		}
		if r.End != want || r.End == other {
			t.Fatalf("the end commit %q, want %q (the other repository's %s)", r.End, want, other)
		}
	})
	t.Run("its repository made again at the same path", func(t *testing.T) {
		w := filepath.Join(e.root, "remade")
		repoAt(t, w, "first")
		id := sessionIn(t, e, w)
		if err := os.RemoveAll(filepath.Join(w, ".git")); err != nil {
			t.Fatal(err)
		}
		other := repoAt(t, w, "made again")
		e.stopAndWait(id)
		if r := endedRecord(t, e, id); r.End != "" {
			t.Fatalf("the end commit %q of a repository made again (%s)", r.End, other)
		}
	})
	t.Run("removed before the writer reads it", func(t *testing.T) {
		// Every hint dropped, so the end is first settled by the removal's
		// reconcile, after the registry's delete: the writer finds no
		// session to read the end commit through (S28).
		drop := func(func()) {}
		e.srv.recordDeliver.Store(&drop)
		t.Cleanup(func() { e.srv.recordDeliver.Store(nil) })
		w := filepath.Join(e.root, "removed-first")
		repoAt(t, w, "first")
		id := sessionIn(t, e, w)
		commitAt(t, w, "during the session")
		e.stopAndWait(id)
		<-e.local(id).ProcessDone()
		e.srv.registry.Remove(id)
		if r := endedRecord(t, e, id); r.End != "" || !strings.HasPrefix(r.Ending, "stopped") {
			t.Fatalf("a session removed before its end commit's read: %q %q", r.End, r.Ending)
		}
	})
}

// stubbornProc is a process whose stop fails: it runs on until it ends
// by itself (End).
type stubbornProc struct{ *sessiontest.FakeProc }

func (stubbornProc) Stop(context.Context, time.Duration) error {
	return errors.New("the process did not stop")
}

// stubbornSession is a recorded server session over a stubbornProc, its
// hooks o's (recordedLocal).
func stubbornSession(t *testing.T, e *testEnv, o session.Options) (*session.Local, stubbornProc) {
	t.Helper()
	proc := stubbornProc{sessiontest.NewFakeProc()}
	t.Cleanup(func() { proc.End(0) })
	return recordedLocal(t, e, proc, o), proc
}

// holdEnding is an OnActivity that holds a server session's status entry
// that is its end on its way to the record (the record's part comes after
// the session's own hooks, recordHooks) until release is closed; reached is
// closed once it holds it.
func holdEnding(e *testEnv) (hook func(string, session.ActivityEntry, session.AttentionState), reached <-chan struct{}, release func()) {
	in, gate := make(chan struct{}), make(chan struct{})
	var once, held sync.Once
	return func(id string, en session.ActivityEntry, st session.AttentionState) {
		e.srv.localActivity(id, en, st)
		if en.Type == session.ActivityStatus && isEnding(en.Message) {
			held.Do(func() { close(in) })
			<-gate
		}
	}, in, func() { once.Do(func() { close(gate) }) }
}

// A stop that failed is no end: the session reads stopped (Local.Stop
// marks it whatever the process did) while its process runs, and its
// record stays open; once the process is gone the record ends (the wait
// for the process is a hint). A session removed from the listing while its
// process runs (a second DELETE, its status entry held on its way to the
// record meanwhile) is reconciled at its removal, final: lost, and its
// process ending afterwards changes nothing, nor does its status entry
// let go. At a shutdown with the process still running the record is
// marked lost, never ended.
func TestAFailedStopIsNoEnd(t *testing.T) {
	e := newTestEnv(t, nil)
	l, proc := stubbornSession(t, e, session.Options{})
	id := l.Info().ID
	if err := l.Stop(t.Context()); err == nil {
		t.Fatal("the stop did not fail")
	}
	if r, _ := recordFile(t, e, id); !r.EndedAt.IsZero() || r.End != "" {
		t.Fatalf("a failed stop ended the record: %+v", r)
	}
	proc.End(0)
	if r := endedRecord(t, e, id); !strings.HasPrefix(r.Ending, "stopped") {
		t.Fatalf("the end once the process was gone: %+v", r)
	}

	hook, reached, release := holdEnding(e)
	t.Cleanup(release)
	removed, removedProc := stubbornSession(t, e, session.Options{OnActivity: hook})
	rid := removed.Info().ID
	stopped := make(chan error, 1)
	go func() { stopped <- removed.Stop(context.Background()) }()
	<-reached // stopped, its process running, its status entry held
	if resp, _ := e.do("DELETE", "/api/sessions/"+rid, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("removing the stopped session: %d", resp.StatusCode)
	}
	if r, _ := recordFile(t, e, rid); !r.EndedAt.IsZero() || r.LostAt.IsZero() || r.Ending != journal.EndingLost {
		t.Fatalf("a removed session whose process runs: %+v", r)
	}
	release()
	if err := <-stopped; err == nil {
		t.Fatal("the stop did not fail")
	}
	removedProc.End(0)
	<-removed.Ended()
	if r, _ := recordFile(t, e, rid); !r.EndedAt.IsZero() || r.LostAt.IsZero() || r.Ending != journal.EndingLost {
		t.Fatalf("the removed session's record after its process ended: %+v", r)
	}

	still, _ := stubbornSession(t, e, session.Options{})
	sid := still.Info().ID
	if err := still.Stop(t.Context()); err == nil {
		t.Fatal("the stop did not fail")
	}
	e2 := restartServer(t, e)
	r, _ := readRecordFile(t, e2.srv.cfg.DataDir, sid)
	if !r.EndedAt.IsZero() || r.LostAt.IsZero() || r.Ending != journal.EndingLost {
		t.Fatalf("a session whose process outlived the shutdown: %+v", r)
	}
}

// holdChat is an OnChat that holds a server session's withdrawn line on
// its way to the record (after the server's own hook) until release is
// closed; reached is closed once it holds it.
func holdChat(e *testEnv) (hook func(string, session.ChatMessage), reached <-chan struct{}, release func()) {
	in, gate := make(chan struct{}), make(chan struct{})
	var once, held sync.Once
	return func(id string, m session.ChatMessage) {
		e.srv.localChat(id, m)
		if m.Event == "withdrawn" {
			held.Do(func() { close(in) })
			<-gate
		}
	}, in, func() { once.Do(func() { close(gate) }) }
}

// withdrawnOnce says r holds the question q and exactly one end of it,
// withdrawn.
func withdrawnOnce(r journal.Record, q string) bool {
	asked, ends := false, 0
	for _, en := range r.Entries {
		switch {
		case en.Kind == journal.KindQuestion && en.ID == q:
			asked = true
		case en.Kind == journal.KindAnswer && en.Ref == q:
			ends++
			if en.Outcome != journal.OutcomeWithdrawn {
				return false
			}
		}
	}
	return asked && ends == 1
}

// shutdown shuts e's server down, as long as 30 s allow.
func shutdown(e *testEnv) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Shutdown(ctx)
}

// A session's last activity and change, and the agent's question it
// withdrew as it ended, are told after its Ended closes, so a shutdown can
// come before them (its status entry held here on its way to the record).
// The shutdown waits for none of it: it reads the session, whose end is
// observed, and records that end, which ends the question still open as
// withdrawn; the next start finds the record ended, not lost.
func TestAShutdownKeepsAnEndWhoseHooksAreLate(t *testing.T) {
	e := newTestEnv(t, nil)
	hook, reached, release := holdEnding(e)
	t.Cleanup(release)
	fs := recordedFakeSession(t, e, session.Options{OnActivity: hook})
	id := fs.l.Info().ID
	q := fs.askOwn(t, "Which port?", nil)
	if q == "" {
		t.Fatal("no question asked")
	}
	fs.proc.End(0)
	<-reached // the process gone, its status entry held, the withdrawal queued behind it
	shutdown(e)
	release()
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, id)
	if r.EndedAt.IsZero() || r.Ending != "exited (exit 0)" || !r.LostAt.IsZero() {
		t.Fatalf("the record after the shutdown: %+v", r)
	}
	if !withdrawnOnce(r, q) {
		t.Fatalf("the question and its withdrawal after the shutdown: %+v", r.Entries)
	}
	e2 := e.restart()
	if r, _ := readRecordFile(t, e2.srv.cfg.DataDir, id); r.EndedAt.IsZero() || !r.LostAt.IsZero() || r.Ending != "exited (exit 0)" {
		t.Fatalf("the record after the next start: %+v", r)
	}
}

// Codex round 4, finding 5: a session that ended with a question open is
// removed (DELETE) while its status entry is held on its way to the record
// and its withdrawal queued behind it, then the server shuts down. The
// removal reconciles the record at once, final: the session's observed
// end, the question ended with it; nothing is owed after the removal, so
// the shutdown has nothing to wait for.
func TestARemovedSessionsEndKeepsItsQuestion(t *testing.T) {
	e := newTestEnv(t, nil)
	hook, reached, release := holdEnding(e)
	t.Cleanup(release)
	fs := recordedFakeSession(t, e, session.Options{OnActivity: hook})
	id := fs.l.Info().ID
	q := fs.askOwn(t, "Which port?", nil)
	fs.proc.End(0)
	<-reached
	if resp, _ := e.do("DELETE", "/api/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("removing the ended session: %d", resp.StatusCode)
	}
	shutdown(e)
	release()
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, id)
	if r.EndedAt.IsZero() || r.Ending != "exited (exit 0)" || !withdrawnOnce(r, q) {
		t.Fatalf("the removed session's record after the shutdown: %+v", r)
	}
}

// Codex round 4, finding 6: the session's withdrawal taken off its queue
// by another goroutine (a rename's change) and held on its way to the
// record, the session's own end let go after it, and then the shutdown:
// the end, read from the session, ends the question, so what the other
// goroutine still carries is owed nothing. Let go after the shutdown, it
// changes nothing.
func TestAWithdrawalCarriedElsewhereIsTheEndsToo(t *testing.T) {
	e := newTestEnv(t, nil)
	status, statusHeld, releaseStatus := holdEnding(e)
	chat, chatHeld, releaseChat := holdChat(e)
	t.Cleanup(releaseStatus)
	t.Cleanup(releaseChat)
	fs := recordedFakeSession(t, e, session.Options{OnActivity: status, OnChat: chat})
	id := fs.l.Info().ID
	q := fs.askOwn(t, "Which port?", nil)
	fs.proc.End(0)
	<-statusHeld // the withdrawal queued, the session's end held at its status entry
	renamed := make(chan struct{})
	go func() {
		fs.l.Rename("renamed as it ended")
		close(renamed)
	}()
	<-chatHeld // the rename's change took the withdrawal, held on its way
	releaseStatus()
	until(t, "the end in the record", func() bool {
		r, _, err := e.srv.sessionRecords.Get(id)
		return err == nil && !r.EndedAt.IsZero()
	})
	shutdown(e)
	releaseChat()
	<-renamed
	r, _ := readRecordFile(t, e.srv.cfg.DataDir, id)
	if r.Ending != "exited (exit 0)" || !withdrawnOnce(r, q) {
		t.Fatalf("the record after the shutdown: %+v", r)
	}
}

// What the session's agent reported is the record's while the record is
// open, and dropped, counted, once its end is recorded: a file reported
// before the end is kept, its path made absolute under the session's own
// folder; one held on its way to the record past the session's end is
// dropped (the package's promise for the streams).
func TestAFileIsKeptUntilTheEnd(t *testing.T) {
	e := newTestEnv(t, nil)
	reached, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	fs := recordedFakeSession(t, e, session.Options{OnActivity: func(id string, en session.ActivityEntry, st session.AttentionState) {
		e.srv.localActivity(id, en, st)
		if en.Type == session.ActivityFile && en.Path == "late.go" {
			close(reached)
			<-gate
		}
	}})
	id := fs.l.Info().ID
	fs.l.Record(session.ActivityEntry{Type: session.ActivityFile, Op: "edit", Path: "main.go"})
	told := make(chan struct{})
	go func() {
		fs.l.Record(session.ActivityEntry{Type: session.ActivityFile, Op: "edit", Path: "late.go"})
		close(told)
	}()
	<-reached
	fs.proc.End(0)
	endedRecord(t, e, id)
	release()
	<-told
	r, _ := recordFile(t, e, id)
	if len(r.Files) != 1 || r.Files[0].Path != filepath.Join(e.root, "main.go") || r.EndedAt.IsZero() || r.Dropped != 1 {
		t.Fatalf("the files: %+v ended %v dropped %d", r.Files, r.EndedAt, r.Dropped)
	}
}

// Codex round 4, finding 3: a server session whose process exits before
// the registry holds it (its pump starts with the session, and its end is
// told to its hooks before the recorder knows the session): the record is
// reconciled once the registration is complete, and the end is read then.
func TestAnExitBeforeTheRegistryHeldTheSessionIsRecorded(t *testing.T) {
	e := newTestEnv(t, nil)
	proc := sessiontest.NewFakeProc()
	t.Cleanup(func() { proc.End(0) })
	endTold := make(chan struct{})
	var once sync.Once
	o := session.Options{OnChange: func(info session.Info) {
		e.srv.localChange(info)
		if info.Status.Ended() {
			once.Do(func() { close(endTold) })
		}
	}}
	l := recordedLocalWith(t, e, proc, o, func(*session.Local) {
		proc.End(0)
		<-endTold // the end's change told to the session's hooks, before the recorder knows the session
		e.srv.sessionRecords.Flush()
	})
	id := l.Info().ID
	if r := endedRecord(t, e, id); r.Ending != "exited (exit 0)" {
		t.Fatalf("the record of a session that exited before it was listed: %+v", r)
	}
	if resp, _ := e.do("DELETE", "/api/history/sessions/"+id, adminToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgetting it: %d", resp.StatusCode)
	}
}

// isEnding says a status entry's message is a session's end.
func isEnding(msg string) bool {
	for _, st := range []session.Status{session.StatusExited, session.StatusStopped} {
		if msg == string(st) || strings.HasPrefix(msg, string(st)+" (exit ") {
			return true
		}
	}
	return false
}
