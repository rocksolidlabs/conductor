package crew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/session/sessiontest"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A member's binding (docs/plans/capacity.md, PR-4): the engine keeps what
// a launch bound each member to with the run's crew and hands it to every
// launch of the member; it never reads an account, a default or a reading.

// boundAt is a binding to the registered account id, its folder made up.
func boundAt(id string) *session.BoundAccount {
	return &session.BoundAccount{ID: id, Label: "Work", Folder: "/logins/" + id, Source: session.SourceAccount, Login: "fingerprint-of-" + id}
}

// accountsOf is the bindings and models fl's launches of name were given.
func (f *fakeLauncher) accountsOf(name string) []session.BoundAccount {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []session.BoundAccount
	for _, c := range f.calls {
		if c.name == name || strings.HasPrefix(c.name, name+" · ") {
			out = append(out, c.account)
		}
	}
	return out
}

func TestMemberAccountValidate(t *testing.T) {
	for _, m := range []Member{
		immediate("lead", "x"),
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Account: "default"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Account: "a-abcdefghijk2", Model: "claude-opus-4.1:latest"},
	} {
		if err := m.Validate(); err != nil {
			t.Errorf("%+v: %v", m, err)
		}
	}
	for _, m := range []Member{
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Account: "unlisted"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Account: "a-ABCDEFGHIJKL"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Account: "a-short"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Model: "-x"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Model: "a b"},
		{Name: "lead", AgentID: "claude", Start: Start{When: StartImmediately}, Model: strings.Repeat("m", 101)},
	} {
		if err := m.Validate(); err == nil {
			t.Errorf("%+v was taken", m)
		}
	}
	// A crew is saved with no binding, which only a launch makes; a run's
	// crew carries one and still validates.
	c := testCrew(immediate("lead", "x"))
	c.Members[0].Bound = boundAt("a-aaaaaaaaaaaa")
	if err := c.Validate(); err != nil {
		t.Fatalf("a bound crew: %v", err)
	}
	if err := c.ValidateSave(); err == nil {
		t.Fatal("a crew saved with a binding was taken")
	}
}

// A managed run's crew as the ledger keeps it (runs.crew) holds each
// member's binding, and every attempt of the member, its retry after a
// failed check included, is launched under that binding and model.
func TestEveryAttemptKeepsItsMembersBinding(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	build := work("build", "lead")
	build.Checks = []Check{{ID: "has-it", Argv: []string{"sh", "-c", "test -f built.txt"}}}
	c := repoCrew(dir, build)
	b := boundAt("a-aaaaaaaaaaaa")
	c.Members[0].Bound, c.Members[0].Model = b, "opus"
	run := launchManaged(t, e, c)
	row := l.runRowWhen(t, run.ID, "with its crew", func(r ledger.RunRow) bool { return r.Crew != "" })
	var kept Crew
	if err := json.Unmarshal([]byte(row.Crew), &kept); err != nil || kept.Members[0].Bound == nil || *kept.Members[0].Bound != *b || kept.Members[0].Model != "opus" {
		t.Fatalf("runs.crew: %v %s", err, row.Crew)
	}
	a1 := waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
	writeFile(t, filepath.Join(a1.Attempt.Worktree, "other.txt"), "x\n")
	commitAll(t, a1.Attempt.Worktree, "wrong file")
	submit(t, e, a1.Attempt.SessionID, Result{Summary: "tried"})
	waitTaskWhere(t, e, run.ID, "build", "attempt 2 running", func(ts TaskState) bool {
		return ts.Attempt != nil && ts.Attempt.N == 2 && ts.Attempt.State == ledger.AttemptRunning
	})
	if got := fl.accountsOf("lead"); len(got) != 2 || got[0] != *b || got[1] != *b {
		t.Fatalf("the attempts' bindings: %+v", got)
	}
	fl.mu.Lock()
	for _, call := range fl.calls {
		if call.model != "opus" {
			t.Errorf("%s launched with the model %q", call.name, call.model)
		}
	}
	fl.mu.Unlock()
	stopRun(t, e, run.ID)
}

// A repair continuation copies the crew as launched: the repair attempt
// of a member on a registered account is launched under its binding, the
// first attempt's.
func TestRepairKeepsAccount(t *testing.T) {
	dir := newRepo(t)
	e, fl, l := managedEngine(t)
	c := repoCrew(dir, work("build", "lead"))
	b := boundAt("a-aaaaaaaaaaaa")
	c.Members[0].Bound = b
	run := launchManaged(t, e, c)
	commitResult(t, e, waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning), "built.txt", "built\n", "built")
	c1 := waitTask(t, e, run.ID, "build", ledger.TaskSucceeded, 1).Result.Commit
	runFinished(t, e, l, run.ID, ledger.OutcomeSucceeded)
	next, err := continueWithRepair(e, run.ID, "rv.rv-0021.1", "build", 1, c1, "", personsRequest("rv-0021"))
	if err != nil {
		t.Fatal(err)
	}
	attemptN(t, e, next.ID, "build", 2)
	if got := fl.accountsOf("lead"); len(got) != 2 || got[0] != *b || got[1] != *b {
		t.Fatalf("the attempts' bindings: %+v", got)
	}
	stopRun(t, e, next.ID)
}

// A managed run any member of which is bound to a login of this server's
// (a registered account, an entry's pinned folder, Brakeman's unlisted
// folder, a launch's own login variables) is offered to no Runtime: the
// offered one is asked nothing and the run runs natively. A run whose
// members all take the tool's own login is offered as before.
func TestBoundRunNotOffered(t *testing.T) {
	for _, source := range []string{session.SourceAccount, session.SourcePinned, session.SourceUnlisted, session.SourceLaunchEnv, session.SourceOwn} {
		t.Run(source, func(t *testing.T) {
			e, _, _ := managedEngine(t)
			fake := &sessiontest.FakeRuntime{TakesRun: func(ext.ProcessRef) bool { return false }}
			offer(t, e, fake)
			c := repoCrew(newRepo(t), work("build", "lead"))
			c.Members[0].Bound = &session.BoundAccount{ID: "default", Source: source, Login: "fp"}
			run := launchManaged(t, e, c)
			waitAttempt(t, e, run.ID, "build", ledger.AttemptRunning)
			want := []string(nil)
			if source == session.SourceOwn {
				want = []string{"Takes"}
			}
			if got := methods(fake.Calls()); !reflect.DeepEqual(got, want) {
				t.Fatalf("the Runtime was asked %v, want %v", got, want)
			}
			if r := runOf(t, e, run.ID); r.taken || r.rt.Where() != ext.WhereNative {
				t.Fatalf("the run's Runtime: %q, taken %v", r.rt.Where(), r.taken)
			}
			stopRun(t, e, run.ID)
		})
	}
}

// A legacy run's record keeps every member's binding beside the run, in
// its storage form alone: the run read back (and as every reply carries
// it) has none of it; a new Records over the same folder reads it back
// whole; and a resume from the record launches each member under it, a
// member a record from before bindings names under none (the launcher
// then takes the tool's own login).
func TestLegacyRecordKeepsBindings(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rs.Close)
	b := boundAt("a-aaaaaaaaaaaa")
	pinned := &session.BoundAccount{ID: "default", Folder: "/logins/pinned", Source: session.SourcePinned, Login: "fingerprint-pinned"}
	members := []Member{immediate("lead", "x"), immediate("pin", "y"), immediate("old", "z")}
	members[0].Bound, members[1].Bound = b, pinned
	rec := Run{ID: "api-sweep-0000beef", CrewID: "api-sweep", Name: "API sweep", Cwd: "/work", State: RunStopped, Isolation: IsolationNone}
	for _, m := range members {
		st := newMember(m).state
		st.Status = MemberEnded
		rec.Members = append(rec.Members, st)
	}
	if err := rs.Save(rec); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runs", rec.ID+".json"))
	if err != nil || !strings.Contains(string(raw), `"bound"`) || !strings.Contains(string(raw), b.Folder) || !strings.Contains(string(raw), pinned.Login) {
		t.Fatalf("the record on disk: %v\n%s", err, raw)
	}
	again, err := NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := again.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("read back: %v %v", ok, err)
	}
	if got.Members[0].bound == nil || *got.Members[0].bound != *b || got.Members[1].bound == nil || *got.Members[1].bound != *pinned || got.Members[2].bound != nil {
		t.Fatalf("the bindings read back: %+v", got.Members)
	}
	reply, _ := json.Marshal(got)
	for _, leak := range []string{b.Folder, b.Login, b.Label, pinned.Folder, pinned.Login, `"bound"`} {
		if strings.Contains(string(reply), leak) {
			t.Fatalf("the run as a reply carries %q: %s", leak, reply)
		}
	}
	if !strings.Contains(string(reply), `"account":{"id":"a-aaaaaaaaaaaa","source":"account"}`) {
		t.Fatalf("the member's account in the reply: %s", reply)
	}
	list, err := again.List("api-sweep")
	if err != nil || len(list) != 1 || list[0].Members[0].bound == nil {
		t.Fatalf("listed: %+v %v", list, err)
	}

	e, fl := newEngine(t)
	if _, err := e.ResumeRecord(t.Context(), got); err != nil {
		t.Fatal(err)
	}
	waitLaunched(t, fl, 3)
	for name, want := range map[string]session.BoundAccount{"lead": *b, "pin": *pinned, "old": {}} {
		if got := fl.accountsOf(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s resumed under %+v, want %+v", name, got, want)
		}
	}
}

// A run is kept with the tool's own login as no binding, as a run from
// before bindings is: its member's state names no account and its legacy
// record keeps no binding for it; the ledger's crew holds none either,
// while every other binding there is whole.
func TestOwnLoginKeptAsNoBinding(t *testing.T) {
	own := immediate("own", "x")
	own.Bound = &session.BoundAccount{ID: "default", Source: session.SourceOwn, Login: "fingerprint-own"}
	named := immediate("named", "y")
	named.Bound = boundAt("a-aaaaaaaaaaaa")
	if st := memberStateOf(own); st.Account != nil || st.bound != nil {
		t.Fatalf("the own member's state: %+v", st)
	}
	s := storedOf(Run{Members: []MemberState{memberStateOf(own), memberStateOf(named)}})
	if _, ok := s.Bound["own"]; ok || s.Bound["named"] != *named.Bound {
		t.Fatalf("the record's bindings: %+v", s.Bound)
	}
	kept := crewAsLaunched(testCrew(own, named))
	var c Crew
	if err := json.Unmarshal([]byte(kept), &c); err != nil || c.Members[0].Bound != nil || c.Members[1].Bound == nil || *c.Members[1].Bound != *named.Bound {
		t.Fatalf("the ledger's crew: %v %s", err, kept)
	}
}

// A run rebuilt from its ledger row alone (its record missing) has its
// members as a launch makes them: the account, the model and the whole
// binding of each, but the tool's own login's.
func TestRecoveredRunKeepsItsMembersBindings(t *testing.T) {
	named := immediate("named", "x")
	named.Bound, named.Model = boundAt("a-aaaaaaaaaaaa"), "opus"
	plain := immediate("plain", "y")
	c := testCrew(named, plain)
	rec := Recovered(Run{}, Loaded{Run: ledger.RunRow{ID: "api-sweep-0000beef", CrewID: c.ID, Crew: crewAsLaunched(c), State: ledger.RunInterrupted}})
	if len(rec.Members) != 2 {
		t.Fatalf("the members: %+v", rec.Members)
	}
	m := rec.Members[0]
	if m.Account == nil || *m.Account != (MemberAccount{ID: "a-aaaaaaaaaaaa", Source: session.SourceAccount}) || m.Model != "opus" || m.bound == nil || *m.bound != *named.Bound {
		t.Fatalf("the named member: %+v", m)
	}
	if p := rec.Members[1]; p.Account != nil || p.bound != nil {
		t.Fatalf("the plain member: %+v", p)
	}
	if s := storedOf(rec); s.Bound["named"] != *named.Bound {
		t.Fatalf("the record written of it: %+v", s.Bound)
	}
}

// A record that names a member's account and keeps no binding for it is
// refused at its resume (ErrLoginNotKept): nothing is launched.
func TestRecordWithoutItsBindingIsNotResumed(t *testing.T) {
	st := newMember(immediate("lead", "x")).state
	st.Status, st.Account = MemberEnded, &MemberAccount{ID: "a-aaaaaaaaaaaa", Source: session.SourceAccount}
	rec := Run{ID: "api-sweep-0000beef", CrewID: "api-sweep", Name: "API sweep", Cwd: "/work", State: RunStopped, Isolation: IsolationNone, Members: []MemberState{st}}
	e, fl := newEngine(t)
	if _, err := e.ResumeRecord(t.Context(), rec); !errors.Is(err, ErrLoginNotKept) {
		t.Fatalf("the resume: %v", err)
	}
	if n := len(fl.launched()); n != 0 {
		t.Fatalf("%d sessions launched", n)
	}
}

// waitLaunched waits for fl to have launched n sessions: the launcher's
// own record of its launches is what it waits on, and the test's timeout
// bounds it.
func waitLaunched(t *testing.T, fl *fakeLauncher, n int) {
	t.Helper()
	for len(fl.launched()) < n {
		time.Sleep(10 * time.Millisecond)
	}
}

// A crew stored before bindings (no account in its members) reads as
// bound to none: its members are launched under the zero binding, the
// launcher's tool's own login, as they ran.
func TestOldCrewReadsDefault(t *testing.T) {
	var c Crew
	if err := json.Unmarshal([]byte(`{"id":"api-sweep","name":"API sweep","goal":"g","cwd":"/work","where":"server","isolation":"none","members":[{"name":"lead","agentId":"claude","prompt":"x","start":{"when":"immediately"}}]}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Members[0].Bound != nil || c.Members[0].Account != "" {
		t.Fatalf("the member: %+v", c.Members[0])
	}
	e, fl := newEngine(t)
	if _, err := e.Launch(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if got := fl.accountsOf("lead"); len(got) != 1 || got[0] != (session.BoundAccount{}) {
		t.Fatalf("launched under %+v", got)
	}
}

// A legacy member started by hand later is launched under the binding its
// launch (or its add) gave it, and the run as the engine answers it shows
// the binding's id, label and source alone.
func TestLegacyMemberStartedLaterKeepsAccount(t *testing.T) {
	e, fl := newEngine(t)
	b := boundAt("a-bbbbbbbbbbbb")
	later := manual("later", "x")
	later.Bound = b
	run, err := e.Launch(t.Context(), testCrew(immediate("lead", "y"), later))
	if err != nil {
		t.Fatal(err)
	}
	added := manual("added", "z")
	added.Bound = boundAt("a-cccccccccccc")
	if err := e.AddMember(t.Context(), run.ID, added); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"later", "added"} {
		if err := e.StartMember(t.Context(), run.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	if got := fl.accountsOf("later"); len(got) != 1 || got[0] != *b {
		t.Fatalf("later started under %+v", got)
	}
	if got := fl.accountsOf("added"); len(got) != 1 || got[0] != *added.Bound {
		t.Fatalf("added started under %+v", got)
	}
	now, _ := e.Get(run.ID)
	reply, _ := json.Marshal(now)
	if strings.Contains(string(reply), "/logins/") || strings.Contains(string(reply), "fingerprint-of-") || strings.Contains(string(reply), `"bound"`) || strings.Contains(string(reply), `"Work"`) {
		t.Fatalf("the run's reply carries a binding: %s", reply)
	}
	i := slices.IndexFunc(now.Members, func(m MemberState) bool { return m.Name == "later" })
	if a := now.Members[i].Account; a == nil || *a != (MemberAccount{ID: b.ID, Source: b.Source}) {
		t.Fatalf("the member's account: %+v", a)
	}
	stopRun(t, e, run.ID)
}
