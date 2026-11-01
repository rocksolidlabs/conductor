package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/local"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// syncCall is one call the fake sync store was given: its op (Saved or
// Deleted), the doc (its data its own copy) and what the fake answered.
type syncCall struct {
	op  string
	d   ext.Doc
	err error
}

// fakeSync is a sync store that keeps every call it is given. gate, when
// set, is asked for each call and the call waits for the channel it
// answers (nil: none) or its context; fail decides the call's answer
// (nil: acknowledged); check, when set, looks at each call as it arrives.
// All three are read under mu.
type fakeSync struct {
	mu      sync.Mutex
	calls   []syncCall
	started bool
	gate    func(op string, d ext.Doc) <-chan struct{}
	fail    func(op string, d ext.Doc) error
	check   func(op string, d ext.Doc)
	changed chan struct{}
}

func newFakeSync() *fakeSync { return &fakeSync{changed: make(chan struct{}, 1)} }

func (*fakeSync) Name() string { return "cloud" }

func (f *fakeSync) Saved(ctx context.Context, d ext.Doc) error   { return f.call(ctx, "saved", d) }
func (f *fakeSync) Deleted(ctx context.Context, d ext.Doc) error { return f.call(ctx, "deleted", d) }
func (*fakeSync) Restore(context.Context, ext.RestoreRequest, io.Writer) error {
	return ext.ErrNothingToRestore
}

func (f *fakeSync) call(ctx context.Context, op string, d ext.Doc) error {
	d.Data = bytes.Clone(d.Data)
	f.mu.Lock()
	gate, fail, check := f.gate, f.fail, f.check
	f.mu.Unlock()
	if check != nil {
		check(op, d)
	}
	if gate != nil {
		if ch := gate(op, d); ch != nil {
			select {
			case <-ch:
			case <-ctx.Done():
				f.add(syncCall{op, d, ctx.Err()})
				return ctx.Err()
			}
		}
	}
	var err error
	if fail != nil {
		err = fail(op, d)
	}
	f.add(syncCall{op, d, err})
	return err
}

func (f *fakeSync) add(c syncCall) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *fakeSync) set(fn func(f *fakeSync)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeSync) all() []syncCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// until waits for cond to hold of the calls so far, and answers them.
func (f *fakeSync) until(t *testing.T, what string, cond func([]syncCall) bool) []syncCall {
	t.Helper()
	end := time.After(testWait)
	for {
		if calls := f.all(); cond(calls) {
			return calls
		}
		select {
		case <-f.changed:
		case <-end:
			t.Fatalf("the sync store was never told %s: %s", what, describeCalls(f.all()))
		}
	}
}

// describeCalls is calls in a line each, for a failure's words.
func describeCalls(calls []syncCall) string {
	var b strings.Builder
	for _, c := range calls {
		fmt.Fprintf(&b, "\n  %s %s %s rev %d at %d of %d (%d bytes) epoch %.6s err %v", c.op, c.d.Store, c.d.Path, c.d.Rev, c.d.Offset, c.d.Size, len(c.d.Data), c.d.Epoch, c.err)
	}
	return b.String()
}

// syncStarter is the fake as a provider that needs Start: its delivery
// begins once StartExtensions has started it.
type syncStarter struct{ *fakeSync }

func (s syncStarter) Start(context.Context, ext.Host) error {
	s.set(func(f *fakeSync) { f.started = true })
	return nil
}

// replayedIn reports whether calls hold the epoch's acknowledged
// `replayed`, the epoch's being the n-th replay's (from 1).
func replayedIn(calls []syncCall, n int) bool {
	epoch := nthEpoch(calls, n)
	if epoch == "" {
		return false
	}
	for _, c := range calls {
		if c.d.Store == syncReplayed && c.d.Epoch == epoch && c.err == nil {
			return true
		}
	}
	return false
}

// nthEpoch is the epoch of the n-th replay the calls begin (from 1), ""
// before it.
func nthEpoch(calls []syncCall, n int) string {
	seen := []string{}
	for _, c := range calls {
		if c.d.Store == syncReplay && c.err == nil && !slices.Contains(seen, c.d.Epoch) {
			seen = append(seen, c.d.Epoch)
		}
	}
	if len(seen) < n {
		return ""
	}
	return seen[n-1]
}

// syncModel is what a store that applies the calls as ext asks holds:
// each document's last whole state (its parts joined by path, rev and
// epoch, a part's failure starting it again), a replay's `replayed`
// dropping what the replay did not name. It orders a document's docs by
// revision as a provider may: within an epoch, a doc (a save's first part,
// or a delete) whose Rev is not newer than the last one applied of that
// document is stale and ignored, its parts with it.
func syncModel(calls []syncCall) map[syncKey][]byte {
	state := map[syncKey][]byte{}
	partial := map[syncKey][]byte{}
	named := map[syncKey]bool{}
	applied := map[syncKey]int64{}
	stale := map[syncKey]bool{}
	epoch := ""
	for _, c := range calls {
		if c.err != nil {
			continue
		}
		k := syncKey{c.d.Store, c.d.Path}
		if c.d.Store != syncReplay && c.d.Store != syncReplayed && (c.op == "deleted" || c.d.Offset == 0) {
			last, ok := applied[k]
			stale[k] = ok && c.d.Rev <= last
		}
		switch {
		case c.d.Store == syncReplay:
			epoch, named, applied = c.d.Epoch, map[syncKey]bool{}, map[syncKey]int64{}
		case c.d.Store == syncReplayed:
			if c.d.Epoch == epoch {
				for k := range state {
					if !named[k] {
						delete(state, k)
					}
				}
			}
		case stale[k]:
		case c.op == "deleted":
			delete(state, k)
			delete(partial, k)
			applied[k] = c.d.Rev
		default:
			buf := partial[k]
			if c.d.Offset == 0 {
				buf = nil
			}
			buf = append(buf, c.d.Data...)
			if int64(len(buf)) == c.d.Size {
				state[k] = buf
				delete(partial, k)
				named[k] = true
				applied[k] = c.d.Rev
			} else {
				partial[k] = buf
			}
		}
	}
	return state
}

// syncClock is the adapter's backoff clock: every wait asked for is handed
// to the test, which fires it.
type syncClock struct{ reqs chan fakeTimer }

func newSyncClock(a *syncAdapter) *syncClock {
	c := &syncClock{reqs: make(chan fakeTimer, 64)}
	a.mu.Lock()
	a.after = func(d time.Duration) <-chan time.Time {
		tm := fakeTimer{d: d, fire: make(chan time.Time, 1)}
		c.reqs <- tm
		return tm.fire
	}
	a.mu.Unlock()
	return c
}

// next is the next wait the adapter asks for, which must be of d; the
// test fires it.
func (c *syncClock) next(t *testing.T, d time.Duration) fakeTimer {
	t.Helper()
	select {
	case tm := <-c.reqs:
		if tm.d != d {
			t.Fatalf("the adapter waits %s, want %s", tm.d, d)
		}
		return tm
	case <-time.After(testWait):
		t.Fatalf("the adapter asked for no wait of %s within %s", d, testWait)
	}
	return fakeTimer{}
}

// syncDataDir is a data directory holding a new Dispatch store.
func syncDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	seedDispatch(t, dir)
	return dir
}

// syncEnv is a test server over dir whose sync store is p, its delivery
// begun only once the test starts the providers when p is a Starter.
func syncEnv(t *testing.T, dir string, p ext.ContinuityStore, extra ...catalog.Agent) *testEnv {
	t.Helper()
	providers := local.Fill(ext.Providers{Continuity: p, All: []ext.Entry{{Kind: ext.KindSync, Name: p.Name(), Value: p}}})
	return newTestEnvAgents(t, func(c *config.Config) { c.DataDir = dir }, slog.New(slog.NewTextHandler(io.Discard, nil)), extra, WithExtensions(providers))
}

// stopEnv ends e's server as a shutdown would, its ledger's lock and its
// stores let go, so another server may open the directory.
func stopEnv(t *testing.T, e *testEnv) {
	t.Helper()
	e.http.Close()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	e.srv.WaitBackground(ctx)
}

// syncCrew is a crew document as the data directory holds one.
type syncCrew struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	N    int    `json:"n,omitempty"`
}

// keysOf is the documents the calls name, in the order of their first
// acknowledged call of the epoch (every one when epoch is "").
func keysOf(calls []syncCall, epoch string) []syncKey {
	var out []syncKey
	for _, c := range calls {
		k := syncKey{c.d.Store, c.d.Path}
		if c.err != nil || (epoch != "" && c.d.Epoch != epoch) || slices.Contains(out, k) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// callsOf is the calls of the document k.
func callsOf(calls []syncCall, k syncKey) []syncCall {
	var out []syncCall
	for _, c := range calls {
		if (syncKey{c.d.Store, c.d.Path}) == k {
			out = append(out, c)
		}
	}
	return out
}

// newLedgerRun makes a managed run's row through a transition of ld.
func newLedgerRun(t *testing.T, ld *ledger.Ledger, id, state string) {
	t.Helper()
	if _, err := ld.Transition(context.Background(), id, func(tx *ledger.Tx) error {
		return tx.CreateRun(ledger.RunRow{ID: id, CrewID: "team", Crew: `{"id":"team","name":"Team","tasks":[{"id":"build","member":"lead","kind":"work"}]}`, State: state, CreatedAt: tx.Now()})
	}); err != nil {
		t.Fatal(err)
	}
}

// What reaches the sync store is the allowlist: the catalog, the crews,
// the runs' records, the runs' pointers and Dispatch's commits; never the
// workbench token, links/, tls/, hooks/, ext/ or a plain file, and no byte
// of any of them.
func TestSyncAllowlist(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	st := e.srv.store
	const secret = "s3cret-never-synced"
	if err := st.WriteFile("workbench-token", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"links", "tls", "hooks", "ext"} {
		sub, err := st.Sub(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := sub.Save("x.json", map[string]string{"secret": secret}); err != nil {
			t.Fatal(err)
		}
	}
	extOwn, _ := st.Sub("ext")
	extOwn, _ = extOwn.Sub("cloud")
	if err := extOwn.Save("state.json", map[string]string{"secret": secret}); err != nil {
		t.Fatal(err)
	}
	runsDir, _ := st.Sub("runs")
	if err := runsDir.WriteFile("plain", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(catalogFile, catalog.Overlay{Agents: []catalog.Agent{{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}, Env: map[string]string{"K": secret}}}}); err != nil {
		t.Fatal(err)
	}
	crews, _ := st.Sub("crews")
	if err := crews.Save("todo-app.json", syncCrew{ID: "todo-app", Name: "Todo"}); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.records.Save(crew.Run{ID: "todo-app-0000beef", CrewID: "todo-app", Name: "Todo", StartedAt: time.Now().UTC(), State: crew.RunFinished}); err != nil {
		t.Fatal(err)
	}
	newLedgerRun(t, e.srv.ledger, "team-00000001", ledger.RunRunning)
	rc, err := e.srv.dispatch.CreateProject(t.Context(), dispatch.Op{Actor: adminActor, ID: "sync-p1", By: "Ada"}, dispatch.NewProject{Key: "SYN", Name: "Sync"})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("project: %+v %v", rc, err)
	}
	// The last of them: once it is told, everything before it was.
	if err := crews.Save("zz-last.json", syncCrew{ID: "zz-last", Name: "Last"}); err != nil {
		t.Fatal(err)
	}
	calls := f.until(t, "the last crew", func(c []syncCall) bool {
		return slices.Contains(keysOf(c, ""), syncKey{syncData, "crews/zz-last.json"})
	})
	keys := keysOf(calls, "")
	for _, want := range []syncKey{
		{syncData, "catalog.json"}, {syncData, "crews/todo-app.json"}, {syncData, "runs/todo-app-0000beef.json"},
		{syncLedger, "team-00000001"}, {syncDispatch, "project/" + rc.ID},
	} {
		if !slices.Contains(keys, want) {
			t.Fatalf("%v never reached the sync store: %v", want, keys)
		}
	}
	for _, c := range calls {
		if c.d.Store == syncData && !syncDataDoc.MatchString(c.d.Path) {
			t.Fatalf("%s reached the sync store", c.d.Path)
		}
		if bytes.Contains(c.d.Data, []byte(secret)) {
			t.Fatalf("%s %s carried the secret", c.d.Store, c.d.Path)
		}
	}
}

// Each doc is handed over after its local commit: as it arrives, its bytes
// are a version of the file already committed, the ledger's row has its
// revision, Dispatch the receipt of the operation that changed the entity,
// and a deleted document's delete has landed. The test goes on saving as
// docs arrive, so a doc is compared with the versions committed by then,
// never with the file as a later save left it.
func TestSyncAfterLocalCommit(t *testing.T) {
	f := syncStarter{newFakeSync()}
	dir := syncDataDir(t)
	e := syncEnv(t, dir, f)
	var mu sync.Mutex
	var problems []string
	note := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	// committed holds each version of a document the test's saves had
	// committed, read back once its save returned; deleted the documents
	// whose delete had. A save's commit comes before its return, so a doc
	// told of a version whose save has not returned is the file as it is
	// when told, which the check reads too.
	committed := map[string][][]byte{}
	deleted := map[string]bool{}
	landed := func(path string) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case errors.Is(err, os.ErrNotExist):
			deleted[path] = true
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s read back: %v", path, err))
		default:
			committed[path] = append(committed[path], b)
		}
	}
	srv := e.srv
	f.set(func(f *fakeSync) {
		f.check = func(op string, d ext.Doc) {
			ctx := context.Background()
			switch d.Store {
			case syncData:
				if d.Path == catalogFile {
					return
				}
				b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.Path)))
				mu.Lock()
				was, gone := committed[d.Path], deleted[d.Path]
				mu.Unlock()
				switch {
				case op == "deleted" && !gone && !errors.Is(err, os.ErrNotExist):
					note("%s told deleted before its delete landed", d.Path)
				case op == "saved" && !bytes.Equal(b, d.Data) && !slices.ContainsFunc(was, func(v []byte) bool { return bytes.Equal(v, d.Data) }):
					note("%s told %q, no version committed (the file holds %q, %v)", d.Path, d.Data, b, err)
				}
			case syncLedger:
				row, ok, err := srv.ledger.Run(ctx, d.Path)
				if err != nil || !ok || row.Revision < d.Rev {
					note("run %s told at revision %d while the ledger has %+v %v %v", d.Path, d.Rev, row.Revision, ok, err)
				}
			case syncDispatch:
				if d.Path == syncLogPath {
					return
				}
				var ref syncRef
				_ = json.Unmarshal(d.Data, &ref)
				if ref.Kind != "project" {
					return
				}
				if _, found, err := srv.dispatch.Receipt(ctx, adminActor, "sync-p1"); err != nil || !found {
					note("project %s told before its receipt: %v %v", d.Path, found, err)
				}
				if _, err := srv.dispatch.Project(ctx, "SYN"); err != nil {
					note("project %s told before its row: %v", d.Path, err)
				}
			}
		}
	})
	srv.StartExtensions(t.Context())
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	crews, _ := srv.store.Sub("crews")
	for i := range 3 {
		if err := crews.Save("todo-app.json", syncCrew{ID: "todo-app", Name: "Todo", N: i}); err != nil {
			t.Fatal(err)
		}
		landed("crews/todo-app.json")
	}
	newLedgerRun(t, srv.ledger, "team-00000001", ledger.RunRunning)
	if _, err := srv.ledger.Transition(t.Context(), "team-00000001", func(tx *ledger.Tx) error { return tx.Event("x", nil) }); err != nil {
		t.Fatal(err)
	}
	run, ok, err := srv.ledger.Run(t.Context(), "team-00000001")
	if err != nil || !ok {
		t.Fatalf("the run: %v %v", ok, err)
	}
	if rc, err := srv.dispatch.CreateProject(t.Context(), dispatch.Op{Actor: adminActor, ID: "sync-p1", By: "Ada"}, dispatch.NewProject{Key: "SYN", Name: "Sync"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("project: %+v %v", rc, err)
	}
	if err := crews.Delete("todo-app.json"); err != nil {
		t.Fatal(err)
	}
	landed("crews/todo-app.json")
	// The three are told in no fixed order (a pending entry is updated in
	// place, so the crew's delete can go ahead of the run and the project),
	// and a call is kept only after its check ran: wait for each
	// acknowledged before reading what the checks found.
	f.until(t, "the crew deleted, the run at its revision and the project", func(calls []syncCall) bool {
		var gone, pointer, project bool
		for _, c := range calls {
			if c.err != nil {
				continue
			}
			switch {
			case c.d.Store == syncData && c.d.Path == "crews/todo-app.json" && c.op == "deleted":
				gone = true
			case c.d.Store == syncLedger && c.d.Path == "team-00000001" && c.d.Rev >= run.Revision:
				pointer = true
			case c.d.Store == syncDispatch && c.d.Path != syncLogPath:
				var ref syncRef
				project = project || (json.Unmarshal(c.d.Data, &ref) == nil && ref.Kind == "project")
			}
		}
		return gone && pointer && project
	})
	mu.Lock()
	defer mu.Unlock()
	if len(problems) > 0 {
		t.Fatalf("told before the commit: %v", problems)
	}
}

// A data directory made before the server starts is told whole when the
// sync store's Start succeeds: the replay marker, each document once, the
// run pointers, Dispatch's log at its head, then `replayed`, all of one
// epoch; nothing reaches the store before its Start.
func TestSyncReplayAtStart(t *testing.T) {
	dir := syncDataDir(t)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(catalogFile, catalog.Overlay{Agents: []catalog.Agent{{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}}}}); err != nil {
		t.Fatal(err)
	}
	crews, _ := st.Sub("crews")
	for _, id := range []string{"alpha", "beta"} {
		if err := crews.Save(id+".json", syncCrew{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := crew.NewRecords(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Save(crew.Run{ID: "alpha-0000beef", CrewID: "alpha", Name: "Alpha", StartedAt: time.Now().UTC(), State: crew.RunFinished}); err != nil {
		t.Fatal(err)
	}
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	newLedgerRun(t, ld, "team-00000001", ledger.RunFinished)
	newLedgerRun(t, ld, "team-00000002", ledger.RunFinished)
	ld.Close()
	ds, err := dispatch.Open(filepath.Join(dir, "dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	if rc, err := ds.CreateProject(t.Context(), dispatch.Op{Actor: adminActor, ID: "p1", By: "Ada"}, dispatch.NewProject{Key: "PRE", Name: "Pre"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("project: %+v %v", rc, err)
	}
	ds.Close()

	f := syncStarter{newFakeSync()}
	e := syncEnv(t, dir, f)
	if calls := f.all(); len(calls) != 0 {
		t.Fatalf("told before its Start: %s", describeCalls(calls))
	}
	f.set(func(f *fakeSync) {
		f.check = func(string, ext.Doc) {
			if !f.started {
				t.Error("told before its Start")
			}
		}
	})
	e.srv.StartExtensions(t.Context())
	calls := f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	if calls[0].d.Store != syncReplay || calls[len(calls)-1].d.Store != syncReplayed {
		t.Fatalf("the replay is not framed by its markers: %s", describeCalls(calls))
	}
	epoch := calls[0].d.Epoch
	if len(epoch) != 32 {
		t.Fatalf("epoch %q", epoch)
	}
	seen := map[syncKey]int{}
	for _, c := range calls {
		if c.err != nil || c.d.Epoch != epoch {
			t.Fatalf("a call of the replay: %s", describeCalls([]syncCall{c}))
		}
		seen[syncKey{c.d.Store, c.d.Path}]++
	}
	// Every allowlisted document the directory holds now (the server's own
	// start wrote the ledger runs' records), each run, Dispatch's log.
	want := []syncKey{{syncReplay, ""}, {syncReplayed, ""}, {syncLedger, "team-00000001"}, {syncLedger, "team-00000002"}, {syncDispatch, syncLogPath}}
	for _, sub := range []string{"", "crews", "runs"} {
		docs, _ := st.ListDocs(sub)
		for _, d := range docs {
			p := d.Name
			if sub != "" {
				p = sub + "/" + d.Name
			}
			if syncDataDoc.MatchString(p) {
				want = append(want, syncKey{syncData, p})
			}
		}
	}
	for _, k := range want {
		if seen[k] != 1 {
			t.Fatalf("%v told %d times in the replay, want once: %s", k, seen[k], describeCalls(calls))
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("the replay told %d documents, want %d: %s", len(seen), len(want), describeCalls(calls))
	}
	for _, k := range []syncKey{{syncData, "crews/alpha.json"}, {syncData, "runs/alpha-0000beef.json"}, {syncData, "runs/team-00000001.json"}} {
		if seen[k] != 1 {
			t.Fatalf("%v missing from the replay", k)
		}
	}
	head, err := e.srv.dispatch.Head(t.Context())
	if err != nil || head == 0 {
		t.Fatalf("Head %d %v", head, err)
	}
	for _, c := range calls {
		if c.d.Store == syncDispatch && c.d.Rev != head {
			t.Fatalf("Dispatch's log told at %d, its head %d", c.d.Rev, head)
		}
	}
}

// The delivery held shut while more documents change than the pending set
// takes, one of them once more and quiet after: once opened, a new epoch's
// replay holds every document, that one with its last bytes.
func TestSyncOverflowReplays(t *testing.T) {
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, syncDataDir(t), f)
	const limit = 8
	e.srv.syncs.mu.Lock()
	e.srv.syncs.limit = limit
	e.srv.syncs.mu.Unlock()
	open, held := make(chan struct{}), make(chan struct{})
	first := ""
	f.set(func(f *fakeSync) {
		f.gate = func(_ string, d ext.Doc) <-chan struct{} {
			if d.Store == syncReplay && first == "" {
				first = d.Epoch
				close(held)
				return open
			}
			return nil
		}
	})
	e.srv.StartExtensions(t.Context())
	// Held at the first replay's marker.
	waitClosed(t, held, "the first replay's marker")
	crews, _ := e.srv.store.Sub("crews")
	for i := range limit + 3 {
		if err := crews.Save(fmt.Sprintf("c%d.json", i), syncCrew{ID: fmt.Sprintf("c%d", i), Name: "first"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := crews.Save("c3.json", syncCrew{ID: "c3", Name: "last"}); err != nil {
		t.Fatal(err)
	}
	close(open)
	calls := f.until(t, "a second epoch's replay", func(c []syncCall) bool { return replayedIn(c, 2) })
	second := nthEpoch(calls, 2)
	if second == first {
		t.Fatal("the overflow's replay kept the epoch")
	}
	model := syncModel(calls)
	for i := range limit + 3 {
		k := syncKey{syncData, fmt.Sprintf("crews/c%d.json", i)}
		b, ok := model[k]
		if !ok {
			t.Fatalf("%v missing after the overflow's replay: %s", k, describeCalls(calls))
		}
		var c syncCrew
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: "last", false: "first"}[i == 3]; c.Name != want {
			t.Fatalf("%v holds %q, want %q", k, c.Name, want)
		}
		if n := len(callsOf(calls, k)); n == 0 {
			t.Fatalf("%v never told", k)
		}
	}
	if n, _ := e.srv.syncs.pending(); n > limit+3 {
		t.Fatalf("%d pending after the overflow", n)
	}
}

// A document saved 100 times while delivery is held is delivered once, in
// its last state.
func TestSyncPendingOnce(t *testing.T) {
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, syncDataDir(t), f)
	open, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.set(func(f *fakeSync) {
		f.gate = func(_ string, d ext.Doc) <-chan struct{} {
			if d.Store == syncReplay {
				once.Do(func() { close(held) })
				return open
			}
			return nil
		}
	})
	e.srv.StartExtensions(t.Context())
	waitClosed(t, held, "the replay's marker")
	crews, _ := e.srv.store.Sub("crews")
	for i := range 100 {
		if err := crews.Save("busy.json", syncCrew{ID: "busy", Name: "busy", N: i}); err != nil {
			t.Fatal(err)
		}
	}
	close(open)
	k := syncKey{syncData, "crews/busy.json"}
	calls := f.until(t, "the busy crew", func(c []syncCall) bool { return len(callsOf(c, k)) > 0 && replayedIn(c, 1) })
	// The drain is quiet once it is told: wait for the set to empty.
	waitPending(t, e.srv.syncs, 0)
	got := callsOf(f.all(), k)
	if len(got) != 1 {
		t.Fatalf("told %d times, want once: %s", len(got), describeCalls(calls))
	}
	var c syncCrew
	if err := json.Unmarshal(got[0].d.Data, &c); err != nil || c.N != 99 {
		t.Fatalf("told %s (%v), want its last state", got[0].d.Data, err)
	}
}

// waitClosed waits for ch to be closed: the step it marks was reached.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testWait):
		t.Fatalf("%s was never reached", what)
	}
}

// waitPending waits until a's set holds n entries.
func waitPending(t *testing.T, a *syncAdapter, n int) {
	t.Helper()
	end := time.Now().Add(testWait)
	for {
		got, _ := a.pending()
		if got == n {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d pending, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// bigRecord saves a run record of more than three times ext.MaxDocData
// through the server's records and answers its path and bytes.
func bigRecord(t *testing.T, srv *Server, id string) (string, []byte) {
	t.Helper()
	run := crew.Run{ID: id, CrewID: "big", Name: "Big", Goal: strings.Repeat("g", 3*ext.MaxDocData+ext.MaxDocData/2), StartedAt: time.Now().UTC(), State: crew.RunFinished}
	if err := srv.records.Save(run); err != nil {
		t.Fatal(err)
	}
	path := "runs/" + id + ".json"
	b, err := os.ReadFile(filepath.Join(srv.store.Dir(), filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) <= 3*ext.MaxDocData || len(b) > 4*ext.MaxDocData {
		t.Fatalf("the record is %d bytes, not four parts", len(b))
	}
	return path, b
}

// checkParts checks the acknowledged delivery of the parts in calls (one
// document's, in order) holds four parts of one rev and epoch, at their
// offsets, whose bytes join to want.
func checkParts(t *testing.T, calls []syncCall, want []byte) {
	t.Helper()
	if len(calls) != 4 {
		t.Fatalf("%d parts, want 4: %s", len(calls), describeCalls(calls))
	}
	var joined []byte
	for i, c := range calls {
		if c.err != nil || c.op != "saved" || c.d.Offset != int64(i*ext.MaxDocData) || c.d.Size != int64(len(want)) ||
			c.d.Rev != calls[0].d.Rev || c.d.Epoch != calls[0].d.Epoch || len(c.d.Data) > ext.MaxDocData {
			t.Fatalf("part %d: %s", i, describeCalls([]syncCall{c}))
		}
		joined = append(joined, c.d.Data...)
	}
	if !bytes.Equal(joined, want) {
		t.Fatal("the parts do not join to the document")
	}
}

// A run record of 4 MiB is delivered in four parts of one path, rev and
// epoch whose bytes join to the file.
func TestSyncLargeDocInParts(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	path, want := bigRecord(t, e.srv, "big-0000beef")
	k := syncKey{syncData, path}
	calls := f.until(t, "the record's last part", func(c []syncCall) bool { return len(callsOf(c, k)) == 4 })
	checkParts(t, callsOf(calls, k), want)
}

// After the replay, a failed Saved of a document never written again, and
// the failed third part of a 4 MiB record, are both delivered again whole
// once the test's clock passes the backoff, with no further local write;
// nothing behind them goes first.
func TestSyncRetriesAFailedDelivery(t *testing.T) {
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, syncDataDir(t), f)
	clock := newSyncClock(e.srv.syncs)
	e.srv.StartExtensions(t.Context())
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	var failedOnce, partOnce bool
	f.set(func(f *fakeSync) {
		f.fail = func(_ string, d ext.Doc) error {
			if d.Path == "crews/once.json" && !failedOnce {
				failedOnce = true
				return errors.New("the service is down")
			}
			if strings.HasPrefix(d.Path, "runs/big-") && d.Offset == 2*ext.MaxDocData && !partOnce {
				partOnce = true
				return errors.New("the service is down")
			}
			return nil
		}
	})
	crews, _ := e.srv.store.Sub("crews")
	if err := crews.Save("once.json", syncCrew{ID: "once", Name: "once"}); err != nil {
		t.Fatal(err)
	}
	clock.next(t, syncRetryFirst).fire <- time.Now()
	path, want := bigRecord(t, e.srv, "big-0000beef")
	// Reset by the success: the record's failure waits the first backoff.
	clock.next(t, syncRetryFirst).fire <- time.Now()
	k := syncKey{syncData, path}
	calls := f.until(t, "the record again, whole", func(c []syncCall) bool {
		n := 0
		for _, c := range callsOf(c, k) {
			if c.err == nil {
				n++
			}
		}
		return n == 6 // two parts, then four
	})
	once := callsOf(calls, syncKey{syncData, "crews/once.json"})
	if len(once) != 2 || once[0].err == nil || once[1].err != nil || !bytes.Equal(once[0].d.Data, once[1].d.Data) {
		t.Fatalf("the crew: %s", describeCalls(once))
	}
	big := callsOf(calls, k)
	if len(big) != 7 || big[2].err == nil || big[3].d.Offset != 0 {
		t.Fatalf("the record: %s", describeCalls(big))
	}
	checkParts(t, big[3:], want)
	// Nothing behind the crew went before it was acknowledged.
	if idx := slices.IndexFunc(calls, func(c syncCall) bool { return c.d.Path == path }); idx < slices.IndexFunc(calls, func(c syncCall) bool { return c.d.Path == "crews/once.json" && c.err == nil }) {
		t.Fatalf("the record went before the crew was acknowledged: %s", describeCalls(calls))
	}
}

// The start's replay with one document failing: `replayed` is not told
// until that document is acknowledged.
func TestSyncReplayedWaitsForEveryDoc(t *testing.T) {
	dir := syncDataDir(t)
	st, _ := store.Open(dir)
	crews, _ := st.Sub("crews")
	for _, id := range []string{"alpha", "beta"} {
		if err := crews.Save(id+".json", syncCrew{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, dir, f)
	clock := newSyncClock(e.srv.syncs)
	failed := false
	f.set(func(f *fakeSync) {
		f.fail = func(_ string, d ext.Doc) error {
			if d.Path == "crews/alpha.json" && !failed {
				failed = true
				return errors.New("the service is down")
			}
			return nil
		}
	})
	e.srv.StartExtensions(t.Context())
	timer := clock.next(t, syncRetryFirst)
	if calls := f.all(); slices.ContainsFunc(calls, func(c syncCall) bool { return c.d.Store == syncReplayed }) {
		t.Fatalf("replayed told with a document unacknowledged: %s", describeCalls(calls))
	}
	timer.fire <- time.Now()
	calls := f.until(t, "replayed", func(c []syncCall) bool { return replayedIn(c, 1) })
	ok := slices.IndexFunc(calls, func(c syncCall) bool { return c.d.Path == "crews/alpha.json" && c.err == nil })
	done := slices.IndexFunc(calls, func(c syncCall) bool { return c.d.Store == syncReplayed })
	if ok < 0 || done < ok {
		t.Fatalf("replayed before the document was acknowledged: %s", describeCalls(calls))
	}
}

// Two server lifetimes against one store that keeps its cursor: the second
// begins with a new epoch's replay, a record deleted while no server ran
// absent from it (and from the store once it is replayed), and a save of
// the second whose rev equals one of the first's is told apart by its
// epoch.
func TestSyncEpochAcrossRestarts(t *testing.T) {
	dir := syncDataDir(t)
	f := newFakeSync()
	e1 := syncEnv(t, dir, f)
	f.until(t, "the first replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	crews, _ := e1.srv.store.Sub("crews")
	for _, id := range []string{"kept", "gone"} {
		if err := crews.Save(id+".json", syncCrew{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	calls := f.until(t, "the first lifetime's saves", func(c []syncCall) bool {
		return len(callsOf(c, syncKey{syncData, "crews/gone.json"})) > 0
	})
	first := nthEpoch(calls, 1)
	revs := map[int64]bool{}
	for _, c := range calls {
		if c.d.Store == syncData && c.d.Epoch == first {
			revs[c.d.Rev] = true
		}
	}
	stopEnv(t, e1)
	if err := os.Remove(filepath.Join(dir, "crews", "gone.json")); err != nil {
		t.Fatal(err)
	}
	e2 := syncEnv(t, dir, f)
	calls = f.until(t, "the second replay", func(c []syncCall) bool { return replayedIn(c, 2) })
	second := nthEpoch(calls, 2)
	if second == first || second == "" {
		t.Fatalf("epochs %q then %q", first, second)
	}
	for _, c := range calls {
		if c.d.Epoch == second && c.d.Path == "crews/gone.json" {
			t.Fatal("the deleted crew was in the second replay")
		}
	}
	if _, ok := syncModel(calls)[syncKey{syncData, "crews/gone.json"}]; ok {
		t.Fatal("the store kept the crew deleted while no server ran")
	}
	if _, ok := syncModel(calls)[syncKey{syncData, "crews/kept.json"}]; !ok {
		t.Fatal("the store lost the crew kept")
	}
	// Saves in the second lifetime until one's rev is one the first
	// lifetime told: the same number, another epoch.
	crews2, _ := e2.srv.store.Sub("crews")
	for i := range 200 {
		if err := crews2.Save("kept.json", syncCrew{ID: "kept", Name: "kept", N: i}); err != nil {
			t.Fatal(err)
		}
		if revs[e2.srv.store.Seq()] {
			break
		}
	}
	f.until(t, "a save of the second lifetime at a rev of the first", func(c []syncCall) bool {
		for _, c := range c {
			if c.d.Store == syncData && c.d.Epoch == second && revs[c.d.Rev] && c.d.Path == "crews/kept.json" {
				return true
			}
		}
		return false
	})
}

// With no sync store registered the free default keeps nothing: every doc
// is acknowledged, the set drains, and nothing is written for it in the
// data directory.
func TestSyncDefaultKeepsNothing(t *testing.T) {
	e := newTestEnv(t, nil)
	if n := e.srv.exts.sync.Name(); n != local.Name {
		t.Fatalf("the sync store is %q, want the free default", n)
	}
	crews, _ := e.srv.store.Sub("crews")
	if err := crews.Save("todo-app.json", syncCrew{ID: "todo-app", Name: "Todo"}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, e.srv.syncs, 0)
	if _, err := os.Stat(filepath.Join(e.srv.store.Dir(), "ext")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the free default wrote ext/: %v", err)
	}
	var w bytes.Buffer
	if err := e.srv.exts.sync.Restore(t.Context(), ext.RestoreRequest{}, &w); !errors.Is(err, ext.ErrNothingToRestore) || w.Len() != 0 {
		t.Fatalf("Restore: %v, %d bytes", err, w.Len())
	}
}

// The catalog reaches the sync store masked: every env value of every
// agent, its yolo recipe's too, catalog.RedactedValue, the rest as saved;
// a catalog.json that does not decode goes with no data.
func TestSyncCatalogMasksEnv(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	const secret, yoloSecret = "sk-live-0123456789", "yolo-0123456789"
	ov := catalog.Overlay{Agents: []catalog.Agent{
		{ID: "cat", Name: "cat", Command: []string{"/bin/cat"}, Env: map[string]string{"API_KEY": secret, "MODE": "fast"}, Yolo: &catalog.Yolo{Args: []string{"--yes"}, Env: map[string]string{"Y": yoloSecret}}},
		{ID: "plain", Name: "plain", Command: []string{"/bin/sh"}},
	}, Hidden: []string{"exit"}}
	if err := e.srv.store.Save(catalogFile, ov); err != nil {
		t.Fatal(err)
	}
	k := syncKey{syncData, catalogFile}
	calls := f.until(t, "the catalog", func(c []syncCall) bool { return len(callsOf(c, k)) > 0 })
	d := callsOf(calls, k)[0].d
	if bytes.Contains(d.Data, []byte(secret)) || bytes.Contains(d.Data, []byte(yoloSecret)) || bytes.Contains(d.Data, []byte("fast")) {
		t.Fatalf("an env value reached the sync store: %s", d.Data)
	}
	var got catalog.Overlay
	if err := store.DecodeStrict(d.Data, &got); err != nil {
		t.Fatal(err)
	}
	a := got.Agents[0]
	if len(got.Agents) != 2 || a.Env["API_KEY"] != catalog.RedactedValue || a.Env["MODE"] != catalog.RedactedValue || a.Yolo.Env["Y"] != catalog.RedactedValue ||
		!slices.Equal(a.Yolo.Args, []string{"--yes"}) || !slices.Equal(got.Hidden, []string{"exit"}) || got.Agents[1].Env != nil {
		t.Fatalf("the masked catalog: %s", d.Data)
	}
	if d.Size != int64(len(d.Data)) {
		t.Fatalf("size %d of %d bytes", d.Size, len(d.Data))
	}
	// One that does not decode (a field no overlay has) goes with no data.
	if err := e.srv.store.Save(catalogFile, map[string]string{"bogus": secret}); err != nil {
		t.Fatal(err)
	}
	calls = f.until(t, "the undecodable catalog", func(c []syncCall) bool { return len(callsOf(c, k)) > 1 })
	if d := callsOf(calls, k)[1].d; d.Data != nil || d.Size != 0 || d.Rev == 0 {
		t.Fatalf("the undecodable catalog: %s", describeCalls(callsOf(calls, k)))
	}
}

// A restart with an unfinished managed run and a catalog to mask: the
// recovery's transition, the record it rewrote and the masked catalog
// reach the sync store, after its Start.
func TestSyncStartupCommits(t *testing.T) {
	dir := syncDataDir(t)
	ld, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	newLedgerRun(t, ld, "team-00000001", ledger.RunRunning)
	ld.Close()
	// An override an earlier version saved, its env value equal to the
	// configured agent's in full: the start masks it in the file.
	const secret = "configured-0123456789"
	keyed := catalog.Agent{ID: "keyed", Name: "keyed", Command: []string{"/bin/cat"}, Env: map[string]string{"TOKEN": secret}}
	over := keyed
	over.Name = "Keyed"
	if err := os.WriteFile(filepath.Join(dir, catalogFile), mustEncode(t, catalog.Overlay{Agents: []catalog.Agent{over}}), 0o600); err != nil {
		t.Fatal(err)
	}
	f := syncStarter{newFakeSync()}
	e := syncEnv(t, dir, f, keyed)
	if b, _ := os.ReadFile(filepath.Join(dir, catalogFile)); bytes.Contains(b, []byte(secret)) {
		t.Fatal("the start did not mask the catalog")
	}
	if calls := f.all(); len(calls) != 0 {
		t.Fatalf("told before its Start: %s", describeCalls(calls))
	}
	e.srv.StartExtensions(t.Context())
	calls := f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	model := syncModel(calls)
	var p syncPointer
	if err := json.Unmarshal(model[syncKey{syncLedger, "team-00000001"}], &p); err != nil || p.State != ledger.RunInterrupted || p.Revision != 2 {
		t.Fatalf("the recovered run: %+v %v", p, err)
	}
	var rec crew.Run
	if err := json.Unmarshal(model[syncKey{syncData, "runs/team-00000001.json"}], &rec); err != nil || rec.Managed == nil || rec.Managed.Revision != 2 {
		t.Fatalf("the rewritten record: %+v %v", rec.Managed, err)
	}
	var ov catalog.Overlay
	if err := json.Unmarshal(model[syncKey{syncData, catalogFile}], &ov); err != nil || len(ov.Agents) != 1 || ov.Agents[0].Env["TOKEN"] != catalog.RedactedValue {
		t.Fatalf("the masked catalog: %+v %v", ov, err)
	}
}

// mustEncode is v as the store writes it.
func mustEncode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := store.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A document committed again while it is delivered is pending again
// behind what became pending meanwhile: a document written faster than it
// is delivered holds up nothing behind it, and its last state follows.
func TestSyncBusyDocumentHoldsNothingBehind(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	open, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.set(func(f *fakeSync) {
		f.gate = func(_ string, d ext.Doc) <-chan struct{} {
			var ch <-chan struct{}
			if d.Path == "crews/hot.json" {
				once.Do(func() { close(held); ch = open })
			}
			return ch
		}
	})
	crews, _ := e.srv.store.Sub("crews")
	if err := crews.Save("hot.json", syncCrew{ID: "hot", Name: "hot", N: 1}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, held, "the hot crew's delivery")
	if err := crews.Save("hot.json", syncCrew{ID: "hot", Name: "hot", N: 2}); err != nil {
		t.Fatal(err)
	}
	if err := crews.Save("next.json", syncCrew{ID: "next", Name: "next"}); err != nil {
		t.Fatal(err)
	}
	close(open)
	hot := syncKey{syncData, "crews/hot.json"}
	calls := f.until(t, "the hot crew again", func(c []syncCall) bool { return len(callsOf(c, hot)) == 2 })
	next, again, seen := -1, -1, 0
	for i, c := range calls {
		switch c.d.Path {
		case "crews/next.json":
			next = i
		case "crews/hot.json":
			if seen++; seen == 2 {
				again = i
			}
		}
	}
	if next < 0 || again < next {
		t.Fatalf("the hot crew's second delivery went before the crew behind it: %s", describeCalls(calls))
	}
	var c syncCrew
	if err := json.Unmarshal(syncModel(calls)[hot], &c); err != nil || c.N != 2 {
		t.Fatalf("the hot crew's last state: %+v %v", c, err)
	}
}

// A document committed as a replay begins, between the reset of the set
// and its listing, is delivered behind the replay's marker, in the new
// epoch, so the store keeps it once `replayed` is told (Codex round 1 on
// #553).
func TestSyncCommitAsAReplayBegins(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	a := e.srv.syncs
	crews, _ := e.srv.store.Sub("crews")
	var once sync.Once
	a.mu.Lock()
	a.afterReset = func() {
		once.Do(func() {
			if err := crews.Save("edge.json", syncCrew{ID: "edge", Name: "edge"}); err != nil {
				t.Error(err)
			}
		})
	}
	a.due = true
	a.signal()
	a.mu.Unlock()
	calls := f.until(t, "the second replay", func(c []syncCall) bool { return replayedIn(c, 2) })
	second := nthEpoch(calls, 2)
	for _, c := range calls {
		if c.d.Epoch == second {
			if c.d.Store != syncReplay {
				t.Fatalf("the new epoch begins with %s %s, not its marker: %s", c.d.Store, c.d.Path, describeCalls(calls))
			}
			break
		}
	}
	if _, ok := syncModel(calls)[syncKey{syncData, "crews/edge.json"}]; !ok {
		t.Fatalf("the store dropped the crew committed as the replay began: %s", describeCalls(calls))
	}
}

// A restart against a ledger whose file is there but will not open: its
// runs are not taken as gone. The replay is not complete, so `replayed` is
// not told and the store keeps the run pointers it holds; the replay is
// tried again after the backoff (Codex round 1 on #553).
func TestSyncUnreadableLedgerIsNotEmpty(t *testing.T) {
	dir := syncDataDir(t)
	f := newFakeSync()
	e1 := syncEnv(t, dir, f)
	newLedgerRun(t, e1.srv.ledger, "team-00000001", ledger.RunFinished)
	run := syncKey{syncLedger, "team-00000001"}
	f.until(t, "the run's pointer", func(c []syncCall) bool {
		_, ok := syncModel(c)[run]
		return replayedIn(c, 1) && ok
	})
	stopEnv(t, e1)
	file, err := os.OpenFile(filepath.Join(dir, "runs", ledger.File), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte(strings.Repeat("\xff", 100)), 0); err != nil {
		t.Fatal(err)
	}
	file.Close()
	e2 := syncEnv(t, dir, syncStarter{f})
	if _, err := e2.srv.ledger.Pointers(t.Context(), "", 1); !errors.Is(err, ledger.ErrUnavailable) || e2.srv.ledger.NoDatabase() {
		t.Fatalf("the damaged ledger reads %v, NoDatabase %v", err, e2.srv.ledger.NoDatabase())
	}
	clock := newSyncClock(e2.srv.syncs)
	e2.srv.StartExtensions(t.Context())
	// The replay's listing failed: the backoff is asked for, twice.
	clock.next(t, syncRetryFirst).fire <- time.Now()
	clock.next(t, 2*syncRetryFirst)
	calls := f.all()
	if nthEpoch(calls, 2) != "" {
		t.Fatalf("a replay with the ledger unread was told: %s", describeCalls(calls))
	}
	if _, ok := syncModel(calls)[run]; !ok {
		t.Fatalf("the store lost the run's pointer: %s", describeCalls(calls))
	}
}

// A ledger whose owner lock another server held at the start, taken once
// that one lets go: what that server wrote meanwhile, which no hook here
// saw, is told by a replay in a new epoch.
func TestSyncReplaysWhenTheLedgerIsOwnedLate(t *testing.T) {
	was := ledger.LockWait
	ledger.LockWait = testWait
	t.Cleanup(func() { ledger.LockWait = was })
	dir := syncDataDir(t)
	other, err := ledger.Open(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	newLedgerRun(t, other, "team-00000001", ledger.RunFinished)
	f := newFakeSync()
	e := syncEnv(t, dir, f)
	if ro, _ := e.srv.ledger.ReadOnly(); !ro {
		t.Fatal("the server took the owner lock another ledger holds")
	}
	calls := f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	if _, ok := syncModel(calls)[syncKey{syncLedger, "team-00000001"}]; !ok {
		t.Fatalf("the start's replay missed the run it could read: %s", describeCalls(calls))
	}
	// The server it waits for writes on, then lets go.
	newLedgerRun(t, other, "team-00000002", ledger.RunFinished)
	other.Close()
	f.until(t, "a replay once the lock is taken", func(c []syncCall) bool {
		_, ok := syncModel(c)[syncKey{syncLedger, "team-00000002"}]
		return replayedIn(c, 2) && ok
	})
	if ro, why := e.srv.ledger.ReadOnly(); ro {
		t.Fatalf("the ledger is read-only after the replay: %s", why)
	}
}

// A pruned run's delete is newer than every pointer the store was told of
// it: a store that orders a run's docs by revision (syncModel does) drops
// the run, never keeping it as the newer state (Codex round 2 on #553).
func TestSyncPrunedRunDeletedAtANewerRevision(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	ld := e.srv.ledger
	ctx := t.Context()
	const runID = "team-00000001"
	run := syncKey{syncLedger, runID}
	newLedgerRun(t, ld, runID, ledger.RunRunning)
	if _, err := ld.Transition(ctx, runID, func(tx *ledger.Tx) error {
		r, _, err := tx.Run()
		if err != nil {
			return err
		}
		r.State, r.Outcome, r.ClosedAt = ledger.RunFinished, ledger.OutcomeSucceeded, tx.Now()
		return tx.PutRun(r)
	}); err != nil {
		t.Fatal(err)
	}
	f.until(t, "the closed run's pointer", func(c []syncCall) bool {
		var p syncPointer
		return replayedIn(c, 1) && json.Unmarshal(syncModel(c)[run], &p) == nil && p.State == ledger.RunFinished
	})
	if err := ld.Prune(ctx, runID); err != nil {
		t.Fatal(err)
	}
	calls := f.until(t, "the pruned run's delete", func(c []syncCall) bool {
		for _, c := range callsOf(c, run) {
			if c.op == "deleted" && c.err == nil {
				return true
			}
		}
		return false
	})
	var saved, deleted int64
	for _, c := range callsOf(calls, run) {
		switch {
		case c.err != nil:
		case c.op == "saved":
			saved = max(saved, c.d.Rev)
		case deleted == 0:
			deleted = c.d.Rev
		}
	}
	if saved < 2 || deleted <= saved {
		t.Fatalf("the run saved at revision %d, deleted at %d: %s", saved, deleted, describeCalls(calls))
	}
	if _, ok := syncModel(calls)[run]; ok {
		t.Fatalf("an ordering store kept the pruned run: %s", describeCalls(calls))
	}
}

// A restart where the ledger's or Dispatch's database cannot be opened at
// all (its file a symbolic link): the server runs without it, which is not
// a store with nothing in it. The replay delivers what it could list, but
// no `replayed` ends it, so the sync store keeps the run pointers or the
// Dispatch log it holds (Codex round 2 on #553).
func TestSyncUnopenedStoreKeepsWhatTheSyncStoreHolds(t *testing.T) {
	for _, tc := range []struct {
		name, dir, file string
		kept            syncKey
	}{
		{"ledger", "runs", ledger.File, syncKey{syncLedger, "team-00000001"}},
		{"dispatch", "dispatch", dispatch.File, syncKey{syncDispatch, syncLogPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := syncDataDir(t)
			f := newFakeSync()
			e1 := syncEnv(t, dir, f)
			newLedgerRun(t, e1.srv.ledger, "team-00000001", ledger.RunFinished)
			f.until(t, "the first epoch", func(c []syncCall) bool {
				_, ok := syncModel(c)[tc.kept]
				return replayedIn(c, 1) && ok
			})
			stopEnv(t, e1)
			files, err := filepath.Glob(filepath.Join(dir, tc.dir, tc.file+"*"))
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range files {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(dir, tc.dir, "elsewhere.sqlite"), filepath.Join(dir, tc.dir, tc.file)); err != nil {
				t.Fatal(err)
			}
			e2 := syncEnv(t, dir, syncStarter{f})
			if (tc.name == "ledger" && e2.srv.ledger != nil) || (tc.name == "dispatch" && e2.srv.dispatch != nil) {
				t.Fatalf("the %s opened on a symbolic link", tc.name)
			}
			crews, err := e2.srv.store.Sub("crews")
			if err != nil {
				t.Fatal(err)
			}
			if err := crews.Save("after.json", syncCrew{ID: "after", Name: "After"}); err != nil {
				t.Fatal(err)
			}
			e2.srv.StartExtensions(t.Context())
			// The second epoch's replay, and the crew saved before it, both
			// acknowledged; the set then empty, so nothing of the replay waits.
			f.until(t, "the second epoch's crew", func(c []syncCall) bool {
				second := nthEpoch(c, 2)
				for _, c := range callsOf(c, syncKey{syncData, "crews/after.json"}) {
					if second != "" && c.d.Epoch == second && c.err == nil {
						return true
					}
				}
				return false
			})
			waitPending(t, e2.srv.syncs, 0)
			calls := f.all()
			if replayedIn(calls, 2) {
				t.Fatalf("a replay without the %s ended as complete: %s", tc.name, describeCalls(calls))
			}
			if _, ok := syncModel(calls)[tc.kept]; !ok {
				t.Fatalf("the sync store lost %v: %s", tc.kept, describeCalls(calls))
			}
		})
	}
}
