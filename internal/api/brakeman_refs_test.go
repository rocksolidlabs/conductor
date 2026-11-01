package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// refsFixture is a server holding one record of every kind a ref names: a
// managed run in the ledger (its task, attempt, check receipt, command,
// event and message), a commit on the attempt's branch, a server session
// asking a question with a file in its touched index, a Dispatch ticket
// with a comment, and Brakeman's own records with one briefed request.
type refsFixture struct {
	e        *testEnv
	desk     *brakeman.Store
	run      string
	base     string
	commit   string
	logSHA   string
	session  string
	local    *session.Local
	question string
	file     string
	ticket   string
	comment  string
	op       string
	earlier  string // a briefed request's id
}

func newRefsFixture(t *testing.T) *refsFixture {
	t.Helper()
	e := dispatchEnv(t)
	e.gitRepo(t)
	f := &refsFixture{e: e, run: "team-0000aaaa"}
	ctx := t.Context()
	f.base = gitIn(t, e.root, "rev-parse", "HEAD")
	gitIn(t, e.root, "checkout", "-q", "-b", "attempt-build-1")
	if err := os.WriteFile(filepath.Join(e.root, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.root, "add", "fix.txt")
	gitIn(t, e.root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "the fix")
	f.commit = gitIn(t, e.root, "rev-parse", "HEAD")
	gitIn(t, e.root, "checkout", "-q", "-")

	log := []byte("FAIL: TestIt\n")
	sum := sha256.Sum256(log)
	f.logSHA = hex.EncodeToString(sum[:])
	crewJSON, _ := json.Marshal(map[string]string{"id": "team", "cwd": e.root})
	if _, err := e.srv.ledger.Transition(ctx, f.run, func(tx *ledger.Tx) error {
		now := tx.Now()
		steps := []error{
			tx.PutRun(ledger.RunRow{ID: f.run, CrewID: "team", Crew: string(crewJSON), Base: f.base, State: ledger.RunRunning, CreatedAt: now}),
			tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: "work", State: "failed", Attempts: 1}),
			tx.PutAttempt(ledger.AttemptRow{TaskID: "build", N: 1, Member: "lead", Branch: "attempt-build-1", StartCommit: f.base, ResultCommit: f.commit, State: ledger.AttemptFailed, ReservedAt: now, ClosedAt: now}),
			tx.PutCheck(ledger.CheckRow{TaskID: "build", N: 1, ID: "test", K: 1, Argv: []string{"go", "test"}, Subject: f.commit, State: ledger.CheckFinished, Exit: 1, Log: log, LogSHA256: f.logSHA, StartedAt: now, FinishedAt: now}),
			tx.Event("task.failed", map[string]string{"task": "build"}),
			tx.PutMessage(ledger.MessageRow{Seq: 1, From: "lead", To: "reviewer", Text: "over to you", CreatedAt: now, DeliveredAt: now}),
		}
		for _, err := range steps {
			if err != nil {
				return err
			}
		}
		if _, _, err := tx.Command(adminActor, "c1", "task.retry", "fp"); err != nil {
			return err
		}
		return tx.Settle(adminActor, "c1", ledger.CommandDone, map[string]string{"task": "build"})
	}); err != nil {
		t.Fatal(err)
	}
	// The run's record, as the engine leaves one, so Dispatch takes a link
	// to it.
	if err := e.srv.records.Save(crew.Run{ID: f.run, CrewID: "team", Name: "team", Cwd: e.root, State: crew.RunStopped, Managed: &crew.Managed{Revision: 1}}); err != nil {
		t.Fatal(err)
	}

	f.session = e.createSession("cat")
	d, _ := e.srv.registry.Get(f.session)
	f.local = d.(*session.Local)
	f.local.SetAttentionFull(session.AttentionNeedsInput, "Proceed?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Yes", Input: "y"}})
	f.question = f.local.Info().Attention.ID
	if f.question == "" {
		t.Fatal("no question")
	}
	f.file = filepath.Join(e.root, "README")
	if !f.local.Record(session.ActivityEntry{At: time.Now().UTC(), Type: session.ActivityFile, Op: session.FileOpEdit, Path: f.file, Tool: "Edit"}) {
		t.Fatal("the file event was not recorded")
	}

	ds := e.srv.dispatch
	done := doneOf(t)
	done(ds.CreateProject(ctx, adminOp("Ann"), dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	op := adminOp("Ann")
	f.op = op.ID
	f.ticket = done(ds.CreateTicket(ctx, op, dispatch.NewTicket{Project: "CND", Title: "The build fails"})).Key
	f.comment = done(ds.AddComment(ctx, adminOp("Ann"), f.ticket, "It fails on main too.")).ID
	done(ds.AddLink(ctx, adminOp("Ann"), f.ticket, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: f.run}))

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.desk, err = brakeman.Open(st, brakeman.Options{DefaultCwd: e.root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.desk.Close() })
	f.earlier = f.keptRequest(t, brakeman.DefaultConfig(e.root), []brakeman.Item{fact("the build fails", ref(t, "run:"+f.run))}, brakeman.Read{Ref: ref(t, "run:"+f.run), Class: brakeman.ClassRuns, Rev: "1", At: time.Now()})
	return f
}

// keptRequest makes a request through Brakeman's store, starts it under
// cfg, notes its reads and keeps a brief of items: a briefed request, as a
// turn leaves one.
func (f *refsFixture) keptRequest(t *testing.T, cfg brakeman.Config, items []brakeman.Item, reads ...brakeman.Read) string {
	t.Helper()
	r, _, err := f.desk.Create(brakeman.Request{OpID: fmt.Sprintf("ask-%d", time.Now().UnixNano()), Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.desk.Start(r.ID, cfg, brakeman.Stamp{AgentID: "claude"}); err != nil {
		t.Fatal(err)
	}
	if len(reads) > 0 {
		if _, err := f.desk.NoteReads(r.ID, reads...); err != nil {
			t.Fatal(err)
		}
	}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "Overnight", Summary: "One run failed.", Items: items}
	if _, err := f.desk.KeepBrief(r.ID, brakeman.Kept{OpID: "brief-1", Brief: b}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func ref(t *testing.T, s string) brakeman.Ref {
	t.Helper()
	r, err := brakeman.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fact(text string, refs ...brakeman.Ref) brakeman.Item {
	return brakeman.Item{Kind: brakeman.ItemFact, Fact: &brakeman.Fact{Text: text, Evidence: refs}}
}

func panel(refs ...brakeman.Ref) brakeman.Item {
	return brakeman.Item{Kind: brakeman.ItemEvidence, Evidence: &brakeman.EvidencePanel{Title: "evidence", Refs: refs}}
}

// resolve resolves the brief of items for a request begun under cfg, read
// under now, with the turn's reads.
func (f *refsFixture) resolve(t *testing.T, cfg, now brakeman.Config, items []brakeman.Item, reads ...brakeman.Read) ([]brakeman.Resolution, error) {
	t.Helper()
	req := brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: items}
	return f.e.srv.resolveBriefRefs(t.Context(), f.desk, req, b, brakeman.ReadLog{Reads: reads}, now)
}

// badOf is the bad refs of err, by ref, failing t for any other error.
func badOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var bad *badRefsError
	if !errors.As(err, &bad) {
		t.Fatalf("want bad refs, got %v", err)
	}
	out := map[string]string{}
	for _, b := range bad.Refs {
		out[b.Ref] = b.Why
	}
	return out
}

// everyKind is one ref of every kind the fixture holds.
func (f *refsFixture) everyKind() []string {
	return []string{
		"run:" + f.run,
		"task:" + f.run + "/build",
		"attempt:" + f.run + "/build/1",
		"check:" + f.run + "/build/1/test/1",
		"command:" + f.run + "/c1",
		"event:" + f.run + "/1",
		"message:" + f.run + "/1",
		"commit:" + f.run + "/" + f.commit,
		"session:" + f.session,
		"question:" + f.session + "/" + f.question,
		"file:" + f.session + "/" + f.file,
		"ticket:" + f.ticket,
		"comment:" + f.ticket + "/" + f.comment,
		"op:" + adminActor + "/" + f.op,
		"brief:" + f.earlier + "/0",
	}
}

// Every ref kind resolves against its own store, to the url, state and
// revision its record holds; what the turn read is beside it, and a ref
// it never read is unobserved.
func TestEachRefKindResolves(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	var refs []brakeman.Ref
	for _, s := range f.everyKind() {
		refs = append(refs, ref(t, s))
	}
	readAt := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(refs...), fact("again", refs[0])}, brakeman.Read{Ref: refs[0], Class: brakeman.ClassRuns, State: "running", Rev: "1", At: readAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(refs) {
		t.Fatalf("%d resolutions for %d distinct refs", len(got), len(refs))
	}
	short := gitIn(t, f.e.root, "rev-parse", "--short", f.commit)
	ticketPage := "/dispatch/CND/1"
	want := map[string]brakeman.RefState{
		"run:" + f.run:                             {URL: "/runs/" + f.run, State: crew.RunStopped, Rev: "1"},
		"task:" + f.run + "/build":                 {URL: "/runs/" + f.run + "?task=build", State: "failed", Rev: "1"},
		"attempt:" + f.run + "/build/1":            {URL: "/runs/" + f.run + "?task=build&attempt=1", State: ledger.AttemptFailed, Rev: f.commit},
		"check:" + f.run + "/build/1/test/1":       {URL: "/api/runs/" + f.run + "/tasks/build/attempts/1/steps/test/1/log", State: "exit:1", Rev: f.commit, SHA256: f.logSHA},
		"command:" + f.run + "/c1":                 {URL: "/api/runs/" + f.run + "/commands/c1", State: ledger.CommandDone},
		"event:" + f.run + "/1":                    {URL: "/runs/" + f.run, State: "task.failed"},
		"message:" + f.run + "/1":                  {URL: "/runs/" + f.run, State: "delivered"},
		"commit:" + f.run + "/" + f.commit:         {URL: "/runs/" + f.run, State: "present", Rev: short},
		"session:" + f.session:                     {URL: "/sessions/" + f.session, State: string(f.local.Info().Status) + ":needs_input"},
		"question:" + f.session + "/" + f.question: {URL: "/sessions/" + f.session, State: questionOpen},
		"file:" + f.session + "/" + f.file:         {URL: "/sessions/" + f.session + "?tab=files&open=" + strings.ReplaceAll(f.file, "/", "%2F"), State: session.FileOpEdit, Rev: "1"},
		"ticket:" + f.ticket:                       {URL: ticketPage, State: "backlog", Rev: ""},
		"comment:" + f.ticket + "/" + f.comment:    {URL: ticketPage + "#comment-" + f.comment, State: "posted", Rev: "1"},
		"op:" + adminActor + "/" + f.op:            {URL: ticketPage, State: dispatch.StatusDone},
		"brief:" + f.earlier + "/0":                {URL: "/brakeman?request=" + f.earlier + "#item-0", State: brakeman.StateBriefed},
	}
	tk, err := f.e.srv.dispatch.Ticket(t.Context(), f.ticket)
	if err != nil {
		t.Fatal(err)
	}
	tw := want["ticket:"+f.ticket]
	tw.State, tw.Rev = tk.Status, fmt.Sprint(tk.Rev)
	want["ticket:"+f.ticket] = tw
	for i, res := range got {
		if res.Ref != refs[i] {
			t.Fatalf("resolution %d is %s, want %s: the brief's order", i, res.Ref, refs[i])
		}
		w := want[res.Ref.String()]
		n := res.Now
		if n.URL != w.URL || n.State != w.State || (w.Rev != "" && n.Rev != w.Rev) || n.SHA256 != w.SHA256 && w.SHA256 != "" || n.At.IsZero() {
			t.Errorf("%s: now %+v, want %+v", res.Ref, n, w)
		}
		switch {
		case i == 0:
			if res.Seen != (brakeman.RefState{URL: w.URL, State: "running", Rev: "1", At: readAt}) {
				t.Errorf("%s: seen %+v, want what the turn read", res.Ref, res.Seen)
			}
		case res.Seen != (brakeman.RefState{State: refUnobserved}):
			t.Errorf("%s: seen %+v, want unobserved", res.Ref, res.Seen)
		}
	}
	// An earlier brief's hash is its item's, as kept.
	b, _, err := f.desk.Brief(f.earlier)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := store.Encode(b.Items[0])
	sum := sha256.Sum256(enc)
	if last := got[len(got)-1]; last.Now.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("the brief's sha256 is %q", last.Now.SHA256)
	}

	// Each part of a ref that names nothing: refused, every one named.
	missing := []string{
		"run:team-0000ffff", "task:" + f.run + "/deploy", "attempt:" + f.run + "/build/2", "check:" + f.run + "/build/1/test/2",
		"command:" + f.run + "/c9", "event:" + f.run + "/9", "message:" + f.run + "/9", "commit:" + f.run + "/" + strings.Repeat("ab", 20),
		"session:0123456789abcdef", "question:" + f.session + "/0123456789abcdef", "file:" + f.session + "//no/such/file",
		"ticket:CND-9", "comment:" + f.ticket + "/0123456789abcdef", "op:admin/nope", "brief:br-77/0", "brief:" + f.earlier + "/5",
	}
	var mrefs []brakeman.Ref
	for _, s := range missing {
		mrefs = append(mrefs, ref(t, s))
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(mrefs...)})
	bad := badOf(t, err)
	for _, s := range missing {
		if !strings.HasPrefix(bad[s], "resolves to nothing") {
			t.Errorf("%s: %q, want it to resolve to nothing", s, bad[s])
		}
	}
	if len(bad) != len(missing) {
		t.Errorf("%d bad refs for %d missing: %v", len(bad), len(missing), bad)
	}
}

// A ref outside the request's scope, or of a class it may not see, is
// refused with its words: by the snapshot the turn began under, and by a
// configuration narrowed since; a brief: ref to a brief made under a
// wider scope or visibility is refused as outside the scope.
func TestARefOutsideTheScopeIsRefused(t *testing.T) {
	f := newRefsFixture(t)
	all := brakeman.DefaultConfig(f.e.root)
	with := func(mut func(*brakeman.Config)) brakeman.Config {
		c := brakeman.DefaultConfig(f.e.root)
		mut(&c)
		return c
	}
	runRef, sessRef, ticketRef := ref(t, "run:"+f.run), ref(t, "session:"+f.session), ref(t, "ticket:"+f.ticket)
	items := []brakeman.Item{panel(runRef, sessRef, ticketRef)}
	if _, err := f.resolve(t, all, all, items); err != nil {
		t.Fatalf("all of scope: %v", err)
	}
	top := gitIn(t, f.e.root, "rev-parse", "--show-toplevel")
	for _, c := range []struct {
		name      string
		cfg, now  brakeman.Config
		refused   []brakeman.Ref
		admitted  []brakeman.Ref
		wordsWant string
	}{
		{"another run", with(func(c *brakeman.Config) { c.Scope.Runs = []string{"team-0000bbbb"} }), all, []brakeman.Ref{runRef, sessRef, ticketRef}, nil, "outside the request's scope"},
		{"another crew", with(func(c *brakeman.Config) { c.Scope.Crews = []string{"other"} }), all, []brakeman.Ref{runRef, sessRef, ticketRef}, nil, "outside the request's scope"},
		{"its own crew", with(func(c *brakeman.Config) { c.Scope.Crews = []string{"team"} }), all, []brakeman.Ref{sessRef}, []brakeman.Ref{runRef, ticketRef}, "outside the request's scope"},
		{"another project", with(func(c *brakeman.Config) { c.Scope.Projects = []string{"OPS"} }), all, []brakeman.Ref{runRef, sessRef, ticketRef}, nil, "outside the request's scope"},
		{"its project", with(func(c *brakeman.Config) { c.Scope.Projects = []string{"CND"} }), all, []brakeman.Ref{sessRef}, []brakeman.Ref{runRef, ticketRef}, "outside the request's scope"},
		{"another repository", with(func(c *brakeman.Config) { c.Scope.Repos = []string{"/elsewhere"} }), all, []brakeman.Ref{runRef, sessRef}, []brakeman.Ref{ticketRef}, "outside the request's scope"},
		{"its repository", with(func(c *brakeman.Config) { c.Scope.Repos = []string{top} }), all, nil, []brakeman.Ref{runRef, sessRef, ticketRef}, ""},
		{"the session alone", with(func(c *brakeman.Config) { c.Scope.Sessions = []string{f.session} }), all, nil, []brakeman.Ref{runRef, sessRef, ticketRef}, ""},
		{"another session", with(func(c *brakeman.Config) { c.Scope.Sessions = []string{"0123456789abcdef"} }), all, []brakeman.Ref{sessRef}, []brakeman.Ref{runRef, ticketRef}, "outside the request's scope"},
		{"narrowed since the turn began", all, with(func(c *brakeman.Config) { c.Scope.Runs = []string{"team-0000bbbb"} }), []brakeman.Ref{runRef, sessRef, ticketRef}, nil, "outside the request's scope"},
		{"sessions hidden", with(func(c *brakeman.Config) { c.Visibility.Sessions = false }), all, []brakeman.Ref{sessRef}, []brakeman.Ref{runRef, ticketRef}, "not visible"},
		{"Dispatch hidden since", all, with(func(c *brakeman.Config) { c.Visibility.Dispatch = false }), []brakeman.Ref{ticketRef}, []brakeman.Ref{runRef, sessRef}, "not visible"},
		{"runs hidden", with(func(c *brakeman.Config) { c.Visibility.Runs = false }), all, []brakeman.Ref{runRef}, []brakeman.Ref{sessRef, ticketRef}, "not visible"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.resolve(t, c.cfg, c.now, items)
			if len(c.refused) == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			bad := badOf(t, err)
			for _, r := range c.refused {
				if !strings.HasPrefix(bad[r.String()], c.wordsWant) {
					t.Errorf("%s: %q, want %q", r, bad[r.String()], c.wordsWant)
				}
			}
			for _, r := range c.admitted {
				if why, ok := bad[r.String()]; ok {
					t.Errorf("%s refused: %s", r, why)
				}
			}
		})
	}

	t.Run("an earlier brief", func(t *testing.T) {
		briefRef := ref(t, "brief:"+f.earlier+"/0")
		// The earlier turn read a run under all of scope: narrowed to its
		// run, the brief is still covered; to another run, or with runs
		// hidden, it is withheld.
		if _, err := f.resolve(t, all, with(func(c *brakeman.Config) { c.Scope.Runs = []string{f.run} }), []brakeman.Item{panel(briefRef)}); err == nil {
			t.Fatal("a brief made under all of scope given under a narrowed one")
		}
		narrow := with(func(c *brakeman.Config) { c.Scope.Runs = []string{f.run} })
		within := f.keptRequest(t, narrow, []brakeman.Item{fact("x", runRef)}, brakeman.Read{Ref: runRef, Class: brakeman.ClassRuns, At: time.Now()})
		withinRef := ref(t, "brief:"+within+"/0")
		if _, err := f.resolve(t, narrow, narrow, []brakeman.Item{panel(withinRef)}); err != nil {
			t.Fatalf("a brief within the grant: %v", err)
		}
		for name, c := range map[string]brakeman.Config{
			"another run": with(func(c *brakeman.Config) { c.Scope.Runs = []string{"team-0000bbbb"} }),
			"runs hidden": with(func(c *brakeman.Config) { c.Scope.Runs = []string{f.run}; c.Visibility.Runs = false }),
		} {
			_, err := f.resolve(t, c, c, []brakeman.Item{panel(withinRef)})
			if why := badOf(t, err)[withinRef.String()]; !strings.HasPrefix(why, "outside the request's scope") {
				t.Errorf("%s: %q", name, why)
			}
		}
		// A class the earlier turn never read hidden now withholds nothing.
		noTerminals := with(func(c *brakeman.Config) { c.Scope.Runs = []string{f.run}; c.Visibility.Touched = false })
		if _, err := f.resolve(t, noTerminals, noTerminals, []brakeman.Item{panel(withinRef)}); err != nil {
			t.Fatalf("a class it never read: %v", err)
		}
	})
}

// A record the turn read and that is gone since resolves as deleted (a
// Dispatch tombstone, a run no store holds any longer), never dropped,
// beside what the turn read; one the turn never read and that is gone
// names nothing.
func TestADeletedTargetResolvesAsDeleted(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	ctx := t.Context()
	doneOf(t)(f.e.srv.dispatch.DeleteComment(ctx, adminOp("Ann"), f.ticket, f.comment))
	doneOf(t)(f.e.srv.dispatch.DeleteTicket(ctx, adminOp("Ann"), f.ticket))
	gone := ref(t, "run:team-0000cccc")
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	ticketRef, commentRef := ref(t, "ticket:"+f.ticket), ref(t, "comment:"+f.ticket+"/"+f.comment)
	got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(ticketRef, commentRef, gone)},
		brakeman.Read{Ref: gone, Class: brakeman.ClassRuns, State: "running", Rev: "4", At: at},
		brakeman.Read{Ref: ticketRef, Class: brakeman.ClassDispatch, State: "backlog", Rev: "1", At: at})
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range got {
		if res.Now.State != refDeleted || res.Now.Rev != "" || res.Now.URL == "" {
			t.Errorf("%s: now %+v, want deleted", res.Ref, res.Now)
		}
	}
	if got[0].Seen.Rev != "1" || got[2].Seen.Rev != "4" || got[1].Seen.State != refUnobserved {
		t.Errorf("seen: %+v %+v %+v", got[0].Seen, got[1].Seen, got[2].Seen)
	}
	// Under a scope of projects nothing proves the gone run in.
	narrow := cfg
	narrow.Scope.Projects = []string{"CND"}
	_, err = f.resolve(t, narrow, narrow, []brakeman.Item{panel(gone)}, brakeman.Read{Ref: gone, Class: brakeman.ClassRuns, At: at})
	if why := badOf(t, err)[gone.String()]; !strings.HasPrefix(why, "outside the request's scope") {
		t.Errorf("a gone run under a project scope: %q", why)
	}
	// Never read, a run that is gone names nothing.
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(gone)})
	if why := badOf(t, err)[gone.String()]; why != "resolves to nothing" {
		t.Errorf("an unread gone run: %q", why)
	}
}

// A question resolves while its session is listed and the question in its
// window; once the window has moved past it, or the session has left the
// registry, it is unavailable beside what the turn observed, and a
// question the turn never observed then names nothing.
func TestAnEvictedQuestionIsUnavailable(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	q := ref(t, "question:"+f.session+"/"+f.question)
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	read := brakeman.Read{Ref: q, Class: brakeman.ClassSessions, State: questionOpen, At: at}

	// Answered by moving on: withdrawn, still in the window.
	f.local.SetAttention(session.AttentionNone, "", session.SourceAPI)
	got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(q)}, read)
	if err != nil || got[0].Now.State != questionWithdrawn {
		t.Fatalf("a question gone by: %v %+v", err, got)
	}
	// The window moves past it: as many questions as the chat keeps.
	for i := range session.MaxChat {
		f.local.SetAttentionFull(session.AttentionNeedsInput, fmt.Sprintf("Question %d?", i), session.SourceAPI, session.KindPrompt, nil)
		f.local.SetAttention(session.AttentionNone, "", session.SourceAPI)
	}
	got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(q)}, read)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Now.State != refUnavailable || got[0].Seen.State != questionOpen || !got[0].Seen.At.Equal(at) {
		t.Fatalf("an evicted question: %+v", got[0])
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(q)})
	if why := badOf(t, err)[q.String()]; why != "resolves to nothing" {
		t.Fatalf("an evicted question never observed: %q", why)
	}

	// The session leaves the registry: its question and its file are
	// unavailable, the session itself deleted.
	file, sess := ref(t, "file:"+f.session+"/"+f.file), ref(t, "session:"+f.session)
	f.e.srv.registry.Remove(f.session)
	got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(q, file, sess)}, read,
		brakeman.Read{Ref: file, Class: brakeman.ClassTouched, State: "edit", At: at},
		brakeman.Read{Ref: sess, Class: brakeman.ClassSessions, State: "running", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Now.State != refUnavailable || got[1].Now.State != refUnavailable || got[2].Now.State != refDeleted {
		t.Fatalf("a session gone: %+v", got)
	}
}

// A decision's subjectRevision and a proposal's expectedRevision must be
// the revision the turn read of their target, or, for a target it never
// read, the target's own: advice from an older read is refused, naming
// the revision read.
func TestARevisionOtherThanTheOneReadIsRefused(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	task := ref(t, "task:"+f.run+"/build")
	propose := func(rev string) brakeman.Item {
		return brakeman.Item{Kind: brakeman.ItemProposal, Proposal: &brakeman.Proposal{Text: "retry", Op: "task_retry", Target: task, ExpectedRevision: rev}}
	}
	decide := func(rev string) brakeman.Item {
		rec := -1
		return brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: task, Recommendation: &rec, Evidence: []brakeman.Ref{task}, SubjectRevision: rev}}
	}
	read := brakeman.Read{Ref: task, Class: brakeman.ClassRuns, State: "failed", Rev: "7", At: time.Now()}
	if _, err := f.resolve(t, cfg, cfg, []brakeman.Item{propose("7"), decide("7")}, read); err != nil {
		t.Fatalf("the revision read: %v", err)
	}
	_, err := f.resolve(t, cfg, cfg, []brakeman.Item{propose("1")}, read)
	if why := badOf(t, err)[task.String()]; !strings.Contains(why, "you read revision 7") {
		t.Fatalf("an older revision: %q", why)
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{decide("6")}, read)
	if why := badOf(t, err)[task.String()]; !strings.Contains(why, "you read revision 7") {
		t.Fatalf("a decision's older revision: %q", why)
	}
	// Never read: the target's own revision (the run's, 1) is the one.
	if _, err := f.resolve(t, cfg, cfg, []brakeman.Item{propose("1")}); err != nil {
		t.Fatalf("the target's own revision: %v", err)
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{propose("7")})
	if why := badOf(t, err)[task.String()]; !strings.Contains(why, "is at revision 1") {
		t.Fatalf("another revision of an unread target: %q", why)
	}
}

// Every snapshot is held to the bounds of br-<n>-refs.json: ref 512
// bytes, url 1 KiB, state 32, rev 64, sha256 64 hex digits; a field past
// its bound is cut where a character ends, a url past its own dropped,
// and a file ref of the longest path keeps a url within its bound.
func TestEveryResolutionIsWithinItsBounds(t *testing.T) {
	long := brakeman.RefState{URL: "/" + strings.Repeat("u", brakeman.MaxURL), State: strings.Repeat("é", 40), Rev: strings.Repeat("r", 100), SHA256: "not-a-hash"}
	got := fitState(long)
	if got.URL != "" || len(got.State) > brakeman.MaxRefState || len(got.Rev) > brakeman.MaxRevision || got.SHA256 != "" || !strings.HasPrefix(long.State, got.State) {
		t.Fatalf("%+v", got)
	}
	// Each é is two bytes in the ref and six in the url.
	p := "/" + strings.Repeat("é", 240)
	r := ref(t, "file:0123456789abcdef/"+p)
	if u := refURL(r); len(u) > brakeman.MaxURL || u != "/sessions/0123456789abcdef?tab=files" {
		t.Fatalf("a long path's url: %d bytes", len(u))
	}
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	var refs []brakeman.Ref
	for _, s := range f.everyKind() {
		refs = append(refs, ref(t, s))
	}
	res, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(refs...)})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range res {
		for _, s := range []brakeman.RefState{x.Seen, x.Now} {
			if len(x.Ref.String()) > brakeman.MaxRef || len(s.URL) > brakeman.MaxURL || len(s.State) > brakeman.MaxRefState || len(s.Rev) > brakeman.MaxRevision || (s.SHA256 != "" && len(s.SHA256) != 64) {
				t.Errorf("%s: %+v past its bounds", x.Ref, s)
			}
		}
	}
	// What the resolutions are is what Brakeman's store keeps.
	id, _, err := f.desk.Create(brakeman.Request{OpID: "bounds", Kind: brakeman.KindBriefing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.desk.Start(id.ID, cfg, brakeman.Stamp{}); err != nil {
		t.Fatal(err)
	}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{panel(refs...)}}
	if _, err := f.desk.KeepBrief(id.ID, brakeman.Kept{OpID: "b1", Brief: b, Refs: res}); err != nil {
		t.Fatalf("the store refused the resolutions: %v", err)
	}
}
