package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
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
	project  string // the op id of the project's creation
	earlier  string // a briefed request's id
	reqs     string // the folder Brakeman's requests are kept in
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
			tx.PutRun(ledger.RunRow{ID: f.run, CrewID: "team", Crew: string(crewJSON), Base: f.base, State: ledger.RunStopped, CreatedAt: now, ClosedAt: now}),
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
	pop := adminOp("Ann")
	f.project = pop.ID
	done(ds.CreateProject(ctx, pop, dispatch.NewProject{Key: "CND", Name: "Conductor"}))
	op := adminOp("Ann")
	f.op = op.ID
	f.ticket = done(ds.CreateTicket(ctx, op, dispatch.NewTicket{Project: "CND", Title: "The build fails"})).Key
	f.comment = done(ds.AddComment(ctx, adminOp("Ann"), f.ticket, "It fails on main too.")).ID
	done(ds.AddLink(ctx, adminOp("Ann"), f.ticket, dispatch.NewLink{Kind: dispatch.LinkRun, Ref: f.run}))

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.reqs = filepath.Join(st.Dir(), brakeman.Dir, brakeman.RequestsDir)
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
	return f.resolveCtx(t.Context(), cfg, now, items, reads...)
}

// resolveCtx is resolve under ctx.
func (f *refsFixture) resolveCtx(ctx context.Context, cfg, now brakeman.Config, items []brakeman.Item, reads ...brakeman.Read) ([]brakeman.Resolution, error) {
	req := brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}
	b := brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: items}
	return f.e.srv.resolveBriefRefs(ctx, f.desk, req, b, brakeman.ReadLog{Reads: reads}, now)
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
		"run:" + f.run:                             {URL: "/runs/" + f.run, State: ledger.RunStopped, Rev: "1"},
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
		// Objects of the repository that are no commit: a blob and a tree.
		"commit:" + f.run + "/" + gitIn(t, f.e.root, "rev-parse", f.commit+":fix.txt"),
		"commit:" + f.run + "/" + gitIn(t, f.e.root, "rev-parse", f.commit+"^{tree}"),
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

	// An earlier brief the turn read, its request pruned since: deleted
	// beside what the turn read; never read, it names nothing; under a
	// grant narrowed since the turn began, refused as outside the scope.
	pruned := f.keptRequest(t, cfg, []brakeman.Item{fact("x", ref(t, "run:"+f.run))}, brakeman.Read{Ref: ref(t, "run:"+f.run), Class: brakeman.ClassRuns, At: at})
	prunedRef := ref(t, "brief:"+pruned+"/0")
	briefRead := brakeman.Read{Ref: prunedRef, Class: brakeman.ClassRuns, State: brakeman.StateBriefed, At: at}
	if err := os.Remove(filepath.Join(f.reqs, pruned+".json")); err != nil {
		t.Fatal(err)
	}
	got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(prunedRef)}, briefRead)
	if err != nil {
		t.Fatalf("a pruned brief the turn read: %v", err)
	}
	if n := got[0].Now; n.State != refDeleted || n.URL == "" || got[0].Seen.State != brakeman.StateBriefed {
		t.Errorf("a pruned brief the turn read: %+v", got[0])
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(prunedRef)})
	if why := badOf(t, err)[prunedRef.String()]; why != "resolves to nothing" {
		t.Errorf("a pruned brief never read: %q", why)
	}
	narrowed := cfg
	narrowed.Scope.Runs = []string{f.run}
	_, err = f.resolve(t, cfg, narrowed, []brakeman.Item{panel(prunedRef)}, briefRead)
	if why := badOf(t, err)[prunedRef.String()]; !strings.HasPrefix(why, "outside the request's scope") {
		t.Errorf("a pruned brief under a narrowed grant: %q", why)
	}
	// A request that does not parse is a store that cannot be read: an
	// error, never a bad ref.
	damaged := f.keptRequest(t, cfg, []brakeman.Item{fact("y", ref(t, "run:"+f.run))})
	if err := os.WriteFile(filepath.Join(f.reqs, damaged+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	damagedRef := ref(t, "brief:"+damaged+"/0")
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(damagedRef)}, brakeman.Read{Ref: damagedRef, Class: brakeman.ClassRuns, At: at})
	var bad *badRefsError
	if !errors.Is(err, brakeman.ErrDamaged) || errors.As(err, &bad) {
		t.Errorf("a damaged request: %v", err)
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
	// Stopped first: Remove retires the session and leaves its process.
	if err := f.local.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
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
	// A decision that names no revision of a target that has one: refused,
	// read or not. A proposal's is the guide's to require (its operation's
	// route), and a question has none to name.
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{decide("")}, read)
	if why := badOf(t, err)[task.String()]; !strings.Contains(why, "names no revision, and you read revision 7") {
		t.Fatalf("a decision that names no revision: %q", why)
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{decide("")})
	if why := badOf(t, err)[task.String()]; !strings.Contains(why, "names no revision") || !strings.Contains(why, "is at revision 1") {
		t.Fatalf("a decision that names no revision of an unread target: %q", why)
	}
	if _, err := f.resolve(t, cfg, cfg, []brakeman.Item{propose("")}, read); err != nil {
		t.Fatalf("a proposal that names no revision: %v", err)
	}
	q := ref(t, "question:"+f.session+"/"+f.question)
	rec := -1
	ask := brakeman.Item{Kind: brakeman.ItemDecision, Decision: &brakeman.Decision{About: q, Recommendation: &rec, Evidence: []brakeman.Ref{q}}}
	if _, err := f.resolve(t, cfg, cfg, []brakeman.Item{ask}, brakeman.Read{Ref: q, Class: brakeman.ClassSessions, State: questionOpen, At: time.Now()}); err != nil {
		t.Fatalf("a decision about a question, which has no revision: %v", err)
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

// A question's state is what its session holds of it: answered through
// Conductor (the chat's answered line) or by keys typed in the terminal
// (the mark on the question shown, then the chat's line of it leaving) is
// resolved; one shown or kept without showing is open; one the agent moved
// on from is withdrawn; a hosted session's is its attention alone.
func TestAQuestionsStateIsItsSessions(t *testing.T) {
	t.Run("the states", func(t *testing.T) {
		since := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
		shown := func(answered string) session.Info {
			return session.Info{Attention: session.Attention{ID: "q1", State: session.AttentionNeedsInput, Since: &since, Answered: answered}}
		}
		asked := session.ChatMessage{ID: "q1", Kind: "question", At: since}
		left := func(why string) func() []session.ChatMessage {
			return func() []session.ChatMessage {
				return []session.ChatMessage{asked, {Event: "withdrawn", Ref: "q1", Text: why, At: since.Add(time.Minute)}}
			}
		}
		for _, c := range []struct {
			name    string
			info    session.Info
			history func() []session.ChatMessage
			want    string
		}{
			{"shown", shown(""), nil, questionOpen},
			{"answered in the terminal, still shown", shown(session.AnsweredTerminal), nil, questionResolved},
			{"kept", session.Info{Kept: []string{"q1"}}, nil, questionOpen},
			{"left, answered in the terminal", session.Info{}, left(session.ChatLeftTerminal), questionResolved},
			{"left, the agent moved on", session.Info{}, left(session.ChatLeftAgent), questionWithdrawn},
			{"hosted, gone", session.Info{}, nil, ""},
		} {
			if got, _ := questionState(c.info, c.history, "q1"); got != c.want {
				t.Errorf("%s: %q, want %q", c.name, got, c.want)
			}
		}
	})

	t.Run("answered through Conductor", func(t *testing.T) {
		f := newRefsFixture(t)
		cfg := brakeman.DefaultConfig(f.e.root)
		if err := f.local.AnswerAs(t.Context(), f.question, "y", session.Actor{Name: "Switchman"}); err != nil {
			t.Fatal(err)
		}
		until(t, "the chat's answered line", func() bool {
			return slices.ContainsFunc(f.local.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == f.question && m.Event == "answered" })
		})
		got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(ref(t, "question:"+f.session+"/"+f.question))})
		if err != nil || got[0].Now.State != questionResolved {
			t.Fatalf("answered: %v %+v", err, got)
		}
		// The agent's own question, set aside by work: kept, so open.
		f.local.Ask("Which database?", session.SourceAPI, session.KindPrompt, []session.Option{{Label: "Postgres", Input: "1"}})
		own := f.local.Info().Attention.ID
		f.local.SetAttention(session.AttentionWorking, "", session.SourceAPI)
		if kept := f.local.Info().Kept; !slices.Contains(kept, own) {
			t.Fatalf("not kept: %q", kept)
		}
		got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(ref(t, "question:"+f.session+"/"+own))})
		if err != nil || got[0].Now.State != questionOpen {
			t.Fatalf("kept: %v %+v", err, got)
		}
	})

	t.Run("answered in the terminal", func(t *testing.T) {
		f := newRefsFixture(t)
		cfg := brakeman.DefaultConfig(f.e.root)
		fs := newFakeSession(t, f.e, session.NewID(), session.Options{Questions: codexQuestions(t)})
		if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
			t.Fatal(err)
		}
		a := shownDialog(t, fs)
		q := ref(t, "question:"+fs.l.Info().ID+"/"+a.ID)
		if err := fs.l.Input(fs.owner(t, "Nate"), []byte("y")); err != nil {
			t.Fatal(err)
		}
		until(t, "answered in the terminal", func() bool {
			at := fs.l.Info().Attention
			return at.ID == a.ID && at.Answered == session.AnsweredTerminal
		})
		got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(q)})
		if err != nil || got[0].Now.State != questionResolved {
			t.Fatalf("its dialog still drawn: %v %+v", err, got)
		}
		if err := fs.proc.Print(codexApprovalGone); err != nil {
			t.Fatal(err)
		}
		until(t, "the dialog gone", func() bool {
			return slices.ContainsFunc(fs.l.ChatHistory(), func(m session.ChatMessage) bool { return m.Ref == a.ID && m.Event == "withdrawn" })
		})
		got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(q)})
		if err != nil || got[0].Now.State != questionResolved {
			t.Fatalf("its dialog gone: %v %+v", err, got)
		}
	})

	t.Run("hosted", func(t *testing.T) {
		f := newRefsFixture(t)
		cfg := brakeman.DefaultConfig(f.e.root)
		since := time.Now().UTC()
		h := &hostedDriver{info: session.Info{ID: session.NewID(), Name: "hosted", Kind: session.KindHosted, Cwd: f.e.root, Status: session.StatusRunning,
			Attention: session.Attention{ID: session.NewID(), State: session.AttentionNeedsInput, Message: "Proceed?", Since: &since}}}
		if err := f.e.srv.registry.Add(h); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.e.srv.registry.Remove(h.info.ID) })
		sess, q := ref(t, "session:"+h.info.ID), ref(t, "question:"+h.info.ID+"/"+h.info.Attention.ID)
		got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(sess, q)})
		if err != nil || got[1].Now.State != questionOpen {
			t.Fatalf("a hosted question shown: %v %+v", err, got)
		}
		// Its folder is its host's: a scope of the server's repositories
		// admits it by none.
		repos := cfg
		repos.Scope.Repos = []string{gitIn(t, f.e.root, "rev-parse", "--show-toplevel")}
		_, err = f.resolve(t, repos, repos, []brakeman.Item{panel(sess)})
		if why := badOf(t, err)[sess.String()]; !strings.HasPrefix(why, "outside the request's scope") {
			t.Errorf("a hosted session under a scope of repositories: %q", why)
		}
		// Gone from its attention, it is in no window the server holds.
		other := ref(t, "question:"+h.info.ID+"/"+session.NewID())
		got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(other)}, brakeman.Read{Ref: other, Class: brakeman.ClassSessions, State: questionOpen, At: since})
		if err != nil || got[0].Now.State != refUnavailable {
			t.Fatalf("a hosted question gone: %v %+v", err, got)
		}
	})
}

// hostedDriver is a hosted session as the registry holds one: its info,
// no chat of the server's, and its touched index when it has one.
type hostedDriver struct {
	info    session.Info
	touched *session.TouchedIndex
}

func (h *hostedDriver) TouchedFiles() []proto.TouchedFile {
	if h.touched == nil {
		return nil
	}
	return h.touched.Snapshot()
}

func (h *hostedDriver) Info() session.Info          { return h.info }
func (h *hostedDriver) Stop(context.Context) error  { return nil }
func (h *hostedDriver) DisconnectLink(string)       {}
func (h *hostedDriver) EndLink(string)              {}
func (h *hostedDriver) LinkViewers() map[string]int { return nil }

// A file the touched index let go while its session is still listed is
// unavailable beside what the turn read; never read, it names nothing.
// The session is a hosted one, whose index (session.TouchedIndex) the
// server keeps from its host's entries, so the test fills it past
// MaxTouched without the event bucket a server session's Record spends.
func TestAnEvictedFileIsUnavailable(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	h := &hostedDriver{info: session.Info{ID: session.NewID(), Name: "hosted", Kind: session.KindHosted, Cwd: f.e.root, Status: session.StatusRunning}, touched: &session.TouchedIndex{}}
	if err := f.e.srv.registry.Add(h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.e.srv.registry.Remove(h.info.ID) })
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	h.touched.Note(f.file, session.FileOpEdit, "Edit", "", at)
	file := ref(t, "file:"+h.info.ID+"/"+f.file)
	if got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(file)}); err != nil || got[0].Now.State != session.FileOpEdit {
		t.Fatalf("a touched file: %v %+v", err, got)
	}
	for i := range session.MaxTouched {
		h.touched.Note(filepath.Join(f.e.root, fmt.Sprintf("f%d", i)), session.FileOpEdit, "Edit", "", at.Add(time.Duration(i+1)*time.Second))
	}
	got, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(file)}, brakeman.Read{Ref: file, Class: brakeman.ClassTouched, State: "edit", At: at})
	if err != nil || got[0].Now.State != refUnavailable || got[0].Seen.State != "edit" {
		t.Fatalf("an evicted file: %v %+v", err, got)
	}
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(file)})
	if why := badOf(t, err)[file.String()]; why != "resolves to nothing" {
		t.Fatalf("an evicted file never read: %q", why)
	}
}

// A Dispatch receipt resolves to what it changed: a ticket's page, a
// project's for an operation of the project's own (admitted by that
// project), and pruned, its ticket named still by the change log.
func TestAReceiptResolvesToWhatItChanged(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	created := ref(t, "op:"+adminActor+"/"+f.project)
	cnd, ops := cfg, cfg
	cnd.Scope.Projects, ops.Scope.Projects = []string{"CND"}, []string{"OPS"}
	got, err := f.resolve(t, cnd, cnd, []brakeman.Item{panel(created)})
	if err != nil || got[0].Now.URL != "/dispatch/CND" || got[0].Now.State != dispatch.StatusDone {
		t.Fatalf("a project's receipt: %v %+v", err, got)
	}
	_, err = f.resolve(t, ops, ops, []brakeman.Item{panel(created)})
	if why := badOf(t, err)[created.String()]; !strings.HasPrefix(why, "outside the request's scope") {
		t.Errorf("a project's receipt under another project: %q", why)
	}
	// Pruned: the store keeps its newest receipts, and the change log
	// names the ticket after.
	db := openRaw(t, filepath.Join(f.e.srv.dispatch.Dir(), dispatch.File))
	if _, err := db.Exec("DELETE FROM ops WHERE actor = ? AND id = ?", adminActor, f.op); err != nil {
		t.Fatal(err)
	}
	op := ref(t, "op:"+adminActor+"/"+f.op)
	got, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(op)})
	if err != nil || got[0].Now.State != "pruned" || got[0].Now.URL != "/dispatch/CND/1" {
		t.Fatalf("a pruned receipt: %v %+v", err, got)
	}
}

// A command ref names the admin's receipt, the one its url answers the
// workbench token: another actor's receipt of the same id is not it.
func TestACommandIsTheAdminsReceipt(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	if _, err := f.e.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
		if _, _, err := tx.Command("user:ann", "c2", "task.retry", "fp"); err != nil {
			return err
		}
		return tx.Settle("user:ann", "c2", ledger.CommandDone, map[string]string{"task": "build"})
	}); err != nil {
		t.Fatal(err)
	}
	c2 := ref(t, "command:"+f.run+"/c2")
	_, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(c2)})
	if why := badOf(t, err)[c2.String()]; why != "resolves to nothing" {
		t.Fatalf("another actor's command: %q", why)
	}
}

// A task's state and the revision beside it are one read's: a transition
// committed between the task's read and its run's is read again, so the
// pair is one that was.
func TestATasksStateAndRevisionAreOneReads(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	rr := f.e.srv.newRefResolver(f.desk, brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}, brakeman.ReadLog{}, cfg)
	reads := 0
	rr.taskRead = func() {
		reads++
		if reads > 1 {
			return
		}
		if _, err := f.e.srv.ledger.Transition(t.Context(), f.run, func(tx *ledger.Tx) error {
			return tx.PutTask(ledger.TaskRow{ID: "build", Member: "lead", Kind: "work", State: "running", Attempts: 2})
		}); err != nil {
			t.Error(err)
		}
	}
	got, err := rr.resolveAll(t.Context(), brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{panel(ref(t, "task:"+f.run+"/build"))}})
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := f.e.srv.ledger.Run(t.Context(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if n := got[0].Now; n.State != "running" || n.Rev != fmt.Sprint(row.Revision) || reads != 2 {
		t.Fatalf("now %+v after %d reads, the run at revision %d", n, reads, row.Revision)
	}
}

// git that could not look (a context ended, an object store it cannot
// read) is an error, never a commit gone or a folder outside the scope.
func TestAGitFailureIsAnError(t *testing.T) {
	f := newRefsFixture(t)
	cfg := brakeman.DefaultConfig(f.e.root)
	var bad *badRefsError
	repos := cfg
	repos.Scope.Repos = []string{gitIn(t, f.e.root, "rev-parse", "--show-toplevel")}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.resolveCtx(ctx, repos, repos, []brakeman.Item{panel(ref(t, "session:"+f.session))})
	if !errors.Is(err, context.Canceled) || errors.As(err, &bad) {
		t.Errorf("a session's repository under an ended context: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads an object store whatever its mode")
	}
	objects := filepath.Join(f.e.root, ".git", "objects")
	fi, err := os.Stat(objects)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(objects, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(objects, fi.Mode().Perm()) })
	commit := ref(t, "commit:"+f.run+"/"+f.commit)
	_, err = f.resolve(t, cfg, cfg, []brakeman.Item{panel(commit)}, brakeman.Read{Ref: commit, Class: brakeman.ClassRuns, State: "present", At: time.Now()})
	if err == nil || errors.As(err, &bad) {
		t.Errorf("a commit in an object store git cannot read: %v", err)
	}
	// git calls the folder "not a git repository" then too: under a scope
	// of repositories a run or a session there is an error, not outside
	// the scope; under one that asks no repository, it resolves.
	for _, s := range []string{"run:" + f.run, "session:" + f.session} {
		r := ref(t, s)
		_, err := f.resolve(t, repos, repos, []brakeman.Item{panel(r)})
		if err == nil || errors.As(err, &bad) || !strings.Contains(err.Error(), "could not read it") {
			t.Errorf("%s under a scope of repositories, its repository unreadable: %v", s, err)
		}
		if _, err := f.resolve(t, cfg, cfg, []brakeman.Item{panel(r)}); err != nil {
			t.Errorf("%s under no scope of repositories: %v", s, err)
		}
	}
}

// git's "not a git repository" is the absence of one only where no .git
// is at the folder or above it.
func TestARepositoryIsAbsentOnlyWithoutAGitAboveIt(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "notes", "deep")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repoAbsent(plain); err != nil {
		t.Fatalf("a folder with no .git above it: %v", err)
	}
	// A .git directory with no HEAD is no repository's: git looks on.
	empty := filepath.Join(plain, ".git")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repoAbsent(plain); err != nil {
		t.Fatalf("an empty .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(empty, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repoAbsent(plain); err == nil {
		t.Fatal("a .git with a HEAD is a repository git could not read")
	}
	gitfile := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(gitfile, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitfile, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repoAbsent(filepath.Join(gitfile, "sub")); err == nil {
		t.Fatal("a .git file above the folder is a repository git could not read")
	}
}

// gatedLedger is the engine's ledger with a gate after the transaction
// that finishes the run: it commits, and the engine waits there, its
// run's revision not yet moved, until the test lets it go.
type gatedLedger struct {
	crew.Ledger
	ld       *ledger.Ledger
	gated    atomic.Bool
	held     chan struct{}
	released chan struct{}
}

func (g *gatedLedger) Transition(ctx context.Context, runID string, fn func(crew.LedgerTx) error) (int64, error) {
	rev, err := g.Ledger.Transition(ctx, runID, fn)
	if err != nil || g.gated.Load() {
		return rev, err
	}
	if row, ok, rerr := g.ld.Run(ctx, runID); rerr == nil && ok && row.State == ledger.RunFinished && g.gated.CompareAndSwap(false, true) {
		close(g.held)
		<-g.released
	}
	return rev, err
}

// A managed run's state and revision are the ledger's committed pair:
// resolved while the engine is between its final task's commit and its
// revision's move (its snapshot then pairs the finished run with the
// revision before), the run is finished at the revision that finished it.
func TestARunsStateAndRevisionAreOneCommits(t *testing.T) {
	e := readyEnv(t, nil)
	e.gitRepo(t)
	g := &gatedLedger{Ledger: crew.SQLLedger(e.srv.ledger), ld: e.srv.ledger, held: make(chan struct{}), released: make(chan struct{})}
	e.srv.runs.UseLedger(g)
	c := crew.Crew{ID: "solo", Name: "Solo", Goal: "build it", Cwd: e.root, Where: crew.WhereServer, Isolation: crew.IsolationWorktree,
		Members: []crew.Member{{Name: "lead", AgentID: "ready-cat", Start: crew.Start{When: crew.StartTask}}},
		Tasks:   []crew.Task{{ID: "build", Member: "lead", Kind: crew.TaskWork, Instructions: "Build it."}}}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), c, "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	runningAttempt(t, e, run.ID, "build", 1)
	cancelled := make(chan int, 1)
	go func() {
		resp, _ := e.do("POST", "/api/runs/"+run.ID+"/tasks/build/cancel", adminToken, map[string]any{"commandId": "c1"})
		cancelled <- resp.StatusCode
	}()
	released := false
	release2 := func() {
		if !released {
			released = true
			close(g.released)
		}
	}
	t.Cleanup(release2)
	select {
	case <-g.held:
	case <-t.Context().Done():
		t.Fatal("the run never finished")
	}
	row, _, err := e.srv.ledger.Run(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := e.srv.runs.Get(run.ID)
	if snap.Managed == nil || snap.Managed.Revision >= row.Revision {
		t.Fatalf("the engine's revision %+v is not behind the ledger's %d: the window is not open", snap.Managed, row.Revision)
	}
	cfg := brakeman.DefaultConfig(e.root)
	rr := e.srv.newRefResolver(nil, brakeman.Request{ID: "br-99", Config: brakeman.SnapshotOf(cfg)}, brakeman.ReadLog{}, cfg)
	got, err := rr.resolveAll(t.Context(), brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s",
		Items: []brakeman.Item{panel(ref(t, "run:"+run.ID), ref(t, "task:"+run.ID+"/build"))}})
	release2()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint(row.Revision)
	if n := got[0].Now; n.State != ledger.RunFinished || n.Rev != want {
		t.Errorf("the run: %+v, want finished at revision %s (the engine had %s at %d)", n, want, snap.State, snap.Managed.Revision)
	}
	if n := got[1].Now; n.State != "cancelled" || n.Rev != want {
		t.Errorf("the task: %+v, want cancelled at revision %s", n, want)
	}
	if code := <-cancelled; code != 200 {
		t.Fatalf("the cancel: %d", code)
	}
}

// A ledger-only run (its record gone past the records' retention) keeps
// the project it was launched from: the ledger's row keeps its launch
// package, which names the ticket, live link or not.
func TestALedgerOnlyRunKeepsItsLaunchProject(t *testing.T) {
	f := newRefsFixture(t)
	tk, err := f.e.srv.dispatch.Ticket(t.Context(), f.ticket)
	if err != nil {
		t.Fatal(err)
	}
	id := "team-0000bbbb"
	pkg, _ := json.Marshal(map[string]any{"version": crew.PackageVersion, "ticket": map[string]any{"id": tk.ID, "key": tk.Key}})
	if _, err := f.e.srv.ledger.Transition(t.Context(), id, func(tx *ledger.Tx) error {
		return tx.PutRun(ledger.RunRow{ID: id, CrewID: "team", Crew: `{"id":"team"}`, Base: f.base, State: ledger.RunFinished, CreatedAt: tx.Now(), ClosedAt: tx.Now(), Ticket: tk.ID, Package: string(pkg)})
	}); err != nil {
		t.Fatal(err)
	}
	cfg := brakeman.DefaultConfig(f.e.root)
	cnd, ops := cfg, cfg
	cnd.Scope.Projects, ops.Scope.Projects = []string{"CND"}, []string{"OPS"}
	r := ref(t, "run:"+id)
	got, err := f.resolve(t, cnd, cnd, []brakeman.Item{panel(r)})
	if err != nil || got[0].Now.State != ledger.RunFinished {
		t.Fatalf("a ledger-only run under its launch project: %v %+v", err, got)
	}
	_, err = f.resolve(t, ops, ops, []brakeman.Item{panel(r)})
	if why := badOf(t, err)[r.String()]; !strings.HasPrefix(why, "outside the request's scope") {
		t.Errorf("a ledger-only run under another project: %q", why)
	}
}

// A store the server could not open (the ledger, Dispatch's) is an
// error: the record may be there, which is not its absence.
func TestAnUnopenedStoreIsAnError(t *testing.T) {
	e := newTestEnv(t, func(cfg *config.Config) {
		cfg.DataDir = filepath.Join(t.TempDir(), "data")
		for _, p := range []string{filepath.Join(cfg.DataDir, "dispatch", dispatch.File), filepath.Join(cfg.DataDir, "runs", ledger.File)} {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".elsewhere", p); err != nil {
				t.Fatal(err)
			}
		}
	})
	if e.srv.dispatch != nil || e.srv.ledger != nil {
		t.Fatal("a store opened on a symbolic link")
	}
	cfg := brakeman.DefaultConfig(e.root)
	var bad *badRefsError
	for _, s := range []string{"run:team-0000aaaa", "ticket:CND-1", "op:admin/op-1"} {
		r := ref(t, s)
		_, err := e.srv.resolveBriefRefs(t.Context(), nil, brakeman.Request{ID: "br-1", Config: brakeman.SnapshotOf(cfg)},
			brakeman.Brief{Schema: brakeman.BriefSchema, Title: "t", Summary: "s", Items: []brakeman.Item{panel(r)}}, brakeman.ReadLog{}, cfg)
		var unopened *errRefStore
		if !errors.As(err, &unopened) || errors.As(err, &bad) {
			t.Errorf("%s: %v", s, err)
		}
	}
}
