package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/machine"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/version"
)

// The host overview, GET /api/hosts (docs/plans/hosts.md, PR-2). Each test
// reads the reply as the page does, strictly: a field the reply type does
// not name fails it.

// hostsOf reads GET /api/hosts as the admin, failing t unless it answers
// 200 with a reply of the type's fields alone.
func (e *testEnv) hostsOf(t *testing.T) HostsReply {
	t.Helper()
	status, body := e.get(t, "/api/hosts")
	if status != http.StatusOK {
		t.Fatalf("GET /api/hosts: %d %s", status, body)
	}
	return decodeHosts(t, body)
}

func decodeHosts(t *testing.T, body []byte) HostsReply {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var r HostsReply
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("the reply %s: %v", body, err)
	}
	// The ledger's note is one of the server's sentences; its pid at most
	// its digits, and only under another holder.
	if !slices.Contains(hostsNotes, r.Ledger.Note) || r.Ledger.Pid < 0 || len(strconv.FormatInt(r.Ledger.Pid, 10)) > hostsPidDigits ||
		r.Ledger.Pid != 0 && r.Ledger.Holder != HolderOther {
		t.Fatalf("the ledger %+v", r.Ledger)
	}
	return r
}

// hostsRunOf is the reply's row of run id, and whether there is one.
func hostsRunOf(r HostsReply, id string) (HostsRun, bool) {
	for _, run := range r.Runs {
		if run.ID == id {
			return run, true
		}
	}
	return HostsRun{}, false
}

func waitingOf(run HostsRun) string {
	if run.Waiting == nil {
		return "null"
	}
	return strconv.Itoa(*run.Waiting)
}

// This server's row: its name, platform, version, here, and its own
// sessions counted, a question shown counted as waiting.
func TestHostsThisServer(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("this-one"), session.Options{})
	newFakeSession(t, e, sessionHex("this-two"), session.Options{})
	name, _ := os.Hostname()
	r := e.hostsOf(t)
	if r.This.Kind != HostKindServer || r.This.Availability != HostHere || r.This.Name != cutBytes(name, hostsNameMax) ||
		r.This.Platform != machine.Platform() || r.This.Version != version.Version {
		t.Fatalf("this server: %+v", r.This)
	}
	if r.This.Sessions != (HostsSessions{Running: 2}) || len(r.Hosts) != 0 || r.Switchyard != nil || r.Other != nil {
		t.Fatalf("the reply: %+v", r)
	}
	if r.Started.IsZero() || r.At.Before(r.Started) {
		t.Fatalf("at %v, started %v", r.At, r.Started)
	}
	fs.ask(t, "Which database?", dbChoices)
	if got := e.hostsOf(t).This.Sessions; got.Running != 2 || got.Waiting != 1 {
		t.Fatalf("a question shown: %+v", got)
	}
	fs.report(t, session.AttentionWorking, "")
	if got := e.hostsOf(t).This.Sessions; got.Waiting != 0 {
		t.Fatalf("the question gone: %+v", got)
	}
}

// I8: a question keys typed in its terminal answered waits for no one,
// while the screen still shows it and after; a new question does.
func TestHostsTerminalAnsweredNotWaiting(t *testing.T) {
	e := newTestEnv(t, nil)
	fs := newFakeSession(t, e, sessionHex("typed"), session.Options{Questions: codexQuestions(t)})
	if err := fs.proc.Print(codexApproval("curl -sI https://example.com")); err != nil {
		t.Fatal(err)
	}
	q := shownDialog(t, fs).ID
	if got := e.hostsOf(t).This.Sessions.Waiting; got != 1 {
		t.Fatalf("the dialog shown: waiting %d", got)
	}
	sub := fs.owner(t, "Nate")
	if err := fs.l.Input(sub, []byte("y")); err != nil {
		t.Fatal(err)
	}
	until(t, "the question read answered in the terminal", func() bool {
		a := fs.l.Info().Attention
		return a.ID == q && a.Answered == session.AnsweredTerminal
	})
	if a := fs.l.Info().Attention; a.State != session.AttentionNeedsInput {
		t.Fatalf("the screen no longer shows it: %+v", a)
	}
	if got := e.hostsOf(t).This.Sessions; got.Waiting != 0 || got.WaitingRef != "" {
		t.Fatalf("answered in the terminal, still shown: %+v", got)
	}
	fs.report(t, session.AttentionWorking, "")
	until(t, "the question gone", func() bool { return fs.l.Info().Attention.ID != q })
	if got := e.hostsOf(t).This.Sessions.Waiting; got != 0 {
		t.Fatalf("the screen cleared: waiting %d", got)
	}
	fs.ask(t, "Which database?", dbChoices)
	if got := e.hostsOf(t).This.Sessions.Waiting; got != 1 {
		t.Fatalf("a new question: waiting %d", got)
	}
}

// I1: a managed run this server holds is listed from its engine, held by
// this server, its waiting count from its members' sessions.
func TestHostsRunsHeldByThis(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	r := f.hostsOf(t)
	run, ok := hostsRunOf(r, f.run)
	if r.Ledger != (HostsLedger{Holder: HolderThis, Note: NoteHeldHere}) || !ok || run.HeldBy != HolderThis || run.Waiting == nil || *run.Waiting != 0 ||
		run.Crew != "routes" || run.Name != "Routes" || run.State != crew.RunRunning || len(r.Runs) != 1 || r.More.Runs != 0 {
		t.Fatalf("the reply: %+v", r)
	}
	// The member's question waits on the run.
	d, _ := f.srv.registry.Get(f.sid)
	d.(*session.Local).SetAttentionFull(session.AttentionNeedsInput, "Go on?", session.SourceAPI, session.KindPrompt, dbChoices)
	if run, _ := hostsRunOf(f.hostsOf(t), f.run); waitingOf(run) != "1" {
		t.Fatalf("a member's question: %+v", run)
	}
	// Stopped, it is no longer open.
	stopEveryRun(f.srv)
	waitUntil(t, "the run gone from the list", func() bool { _, ok := hostsRunOf(f.hostsOf(t), f.run); return !ok })
}

// lockWait sets the ledger's wait for the owner lock for the servers this
// test makes, read as each opens its ledger.
func lockWait(t *testing.T, d time.Duration) {
	t.Helper()
	was := ledger.LockWait
	ledger.LockWait = d
	t.Cleanup(func() { ledger.LockWait = was })
}

// I1: a server ended with a run running; a new server over the same data
// directory recovers it to interrupted, which its engine does not hold:
// listed under this server from the ledger, waiting 0.
func TestHostsRunsRecoveredHeldByThis(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	crashServer(t, f.testEnv, f.run)
	b := f.restart()
	t.Cleanup(func() { stopEveryRun(b.srv) })
	var run HostsRun
	waitUntil(t, "the recovered run listed", func() bool {
		var ok bool
		run, ok = hostsRunOf(b.hostsOf(t), f.run)
		return ok && run.State == crew.RunInterrupted
	})
	r := b.hostsOf(t)
	if r.Ledger.Holder != HolderThis || run.HeldBy != HolderThis || waitingOf(run) != "0" || run.Crew != "routes" || run.Name != "Routes" {
		t.Fatalf("the recovered run: %+v in %+v", run, r)
	}
}

// I1: two servers over one data directory. The second, its wait for the
// lock given up, lists the first's run from the database under another
// holder with the holder's line, its waiting count null; once the first
// has ended, the next reply says no server holds them and holds nothing of
// the first's line.
func TestHostsRunsHeldByOther(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	lockWait(t, time.Millisecond)
	b := f.restart()
	waitUntil(t, "the second server's wait given up", func() bool { return !b.srv.ledger.Lock().GaveUpAt.IsZero() })
	r := b.hostsOf(t)
	run, ok := hostsRunOf(r, f.run)
	if r.Ledger.Holder != HolderOther || r.Ledger.Note != NoteOtherHolds || r.Ledger.Pid != int64(os.Getpid()) || r.Ledger.Since.IsZero() ||
		!ok || run.HeldBy != HolderOther || run.Waiting != nil {
		t.Fatalf("held by another server: %+v", r)
	}
	since := r.Ledger.Since.Format(time.RFC3339)
	crashServer(t, f.testEnv, f.run)
	status, body := b.get(t, "/api/hosts")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	after := decodeHosts(t, body)
	if after.Ledger.Holder != HolderNone || after.Ledger.Note != NoteStoppedWaiting || after.Ledger.Pid != 0 ||
		!after.Ledger.Since.Equal(b.srv.ledger.Lock().GaveUpAt.UTC()) {
		t.Fatalf("held by none: %+v", after.Ledger)
	}
	for _, word := range []string{`"pid"`, since, NoteOtherHolds} {
		if bytes.Contains(body, []byte(word)) {
			t.Fatalf("the reply after the holder left holds %q: %s", word, body)
		}
	}
	if run, ok := hostsRunOf(after, f.run); !ok || run.HeldBy != HolderNone || run.Waiting != nil {
		t.Fatalf("the run under no holder: %+v", after.Runs)
	}
}

// The first server ended within the second's wait: the second takes the
// lock and says this, and the first's run, its recovery's, is listed under
// it from the ledger though its engine never launched it.
func TestHostsRunsTakenLate(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	lockWait(t, 5*time.Minute)
	b := f.restart()
	t.Cleanup(func() { stopEveryRun(b.srv) })
	if lv := b.srv.ledger.Lock(); lv.Owned || lv.WaitingSince.IsZero() {
		t.Fatalf("the second server's ledger: %+v", lv)
	}
	if got := b.hostsOf(t).Ledger.Holder; got != HolderOther {
		t.Fatalf("while the first holds the lock: %s", got)
	}
	crashServer(t, f.testEnv, f.run)
	var run HostsRun
	waitUntil(t, "the lock taken late and the run recovered", func() bool {
		var ok bool
		r := b.hostsOf(t)
		run, ok = hostsRunOf(r, f.run)
		return r.Ledger.Holder == HolderThis && ok && run.State == crew.RunInterrupted
	})
	if run.HeldBy != HolderThis || waitingOf(run) != "0" {
		t.Fatalf("the run taken late: %+v", run)
	}
}

// I1: one data directory walked through the lock's states, each reply
// read twice with a session's change between, the redraw the test causes:
// each says its own state's words and nothing of another's.
func TestHostsLedgerStates(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	lockWait(t, 5*time.Minute)
	b := f.restart()
	sess := newFakeSession(t, b, sessionHex("redraw"), session.Options{})
	twice := func(what string, check func(HostsLedger) bool) HostsLedger {
		t.Helper()
		var l HostsLedger
		for i := range 2 {
			l = b.hostsOf(t).Ledger
			if !check(l) {
				t.Fatalf("%s (read %d): %+v", what, i+1, l)
			}
			sess.report(t, session.AttentionWorking, fmt.Sprintf("step %d", i))
		}
		return l
	}
	pid := strconv.Itoa(os.Getpid())
	twice("held by another server", func(l HostsLedger) bool {
		return l.Holder == HolderOther && l.Note == NoteOtherHolds && strconv.FormatInt(l.Pid, 10) == pid && !l.Since.IsZero()
	})
	// The probe finds the lock free while this server's wait still runs.
	b.srv.probeLock = func(string) (ledger.ProbeResult, error) { return ledger.ProbeResult{State: ledger.ProbeFree}, nil }
	twice("free, this server still waiting", func(l HostsLedger) bool {
		return l.Holder == HolderNone && l.Note == NoteTaking && l.Pid == 0 && l.Since.Equal(b.srv.ledger.Lock().WaitingSince.UTC())
	})
	// A lock file whose line is not of the ledger's form.
	b.srv.probeLock = func(string) (ledger.ProbeResult, error) {
		return ledger.ProbeResult{State: ledger.ProbeHeld, Line: "pid 12 since yesterday at /home/ada"}, nil
	}
	twice("a line not of its form", func(l HostsLedger) bool {
		return l.Holder == HolderOther && l.Note == NoteOtherUnreadable && l.Pid == 0 && l.Since.IsZero()
	})
	// A probe that fails says the class of its error, never its text.
	secret := "/home/ada/.local/secret-path"
	for _, c := range []struct {
		err   error
		class string
	}{{&fs.PathError{Op: "open", Path: secret, Err: fs.ErrPermission}, NoteUnreadPermission}, {&fs.PathError{Op: "open", Path: secret, Err: fs.ErrNotExist}, NoteUnreadMissing}, {errors.New(secret), NoteUnreadOther}} {
		b.srv.probeLock = func(string) (ledger.ProbeResult, error) { return ledger.ProbeResult{}, c.err }
		twice("not known: "+c.class, func(l HostsLedger) bool {
			return l.Holder == HolderUnknown && l.Note == c.class && l.Since.IsZero()
		})
	}
	b.srv.probeLock = func(string) (ledger.ProbeResult, error) { return ledger.ProbeResult{State: ledger.ProbeNoLocks}, nil }
	twice("no locks", func(l HostsLedger) bool {
		return l.Holder == HolderNone && l.Note == NoteNoLocks
	})
	b.srv.probeLock = nil
	// The holder gone: this server takes the lock.
	crashServer(t, f.testEnv, f.run)
	t.Cleanup(func() { stopEveryRun(b.srv) })
	waitUntil(t, "the lock taken", func() bool { return b.srv.ledger.Lock().Owned })
	twice("held here", func(l HostsLedger) bool { return l == HostsLedger{Holder: HolderThis, Note: NoteHeldHere} })
}

// A database a newer schema wrote, and one that cannot be written: held
// here, read-only, each with its own sentence; neither reply holds the
// data directory's path, nor any word of the ledger's own reason.
func TestHostsHeldHereReadOnly(t *testing.T) {
	for _, c := range []struct {
		name, note string
		spoil      func(t *testing.T, path string)
	}{
		{"newer", NoteNewerOrDamaged, func(t *testing.T, path string) {
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("UPDATE schema_version SET version = ?", ledger.Version+1); err != nil {
				t.Fatal(err)
			}
		}},
		{"unwritable", NoteCannotWrite, func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("root writes a file whatever its mode")
			}
			if err := os.Chmod(path, 0o400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o600) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDecisionFixture(t, nil, nil)
			crashServer(t, f.testEnv, f.run)
			path := filepath.Join(f.srv.store.Dir(), "runs", ledger.File)
			c.spoil(t, path)
			b := f.restart()
			status, body := b.get(t, "/api/hosts")
			if status != http.StatusOK {
				t.Fatalf("%d %s", status, body)
			}
			r := decodeHosts(t, body)
			run, ok := hostsRunOf(r, f.run)
			if r.Ledger.Holder != HolderThisReadOnly || r.Ledger.Note != c.note || !ok || run.Waiting != nil || run.HeldBy != HolderThisReadOnly {
				t.Fatalf("read-only here: %+v (the ledger says %+v)", r.Ledger, b.srv.ledger.Lock())
			}
			_, why := b.srv.ledger.ReadOnly()
			for _, word := range []string{filepath.Base(filepath.Dir(f.srv.store.Dir())), f.srv.store.Dir(), ledger.File, why} {
				if word != "" && bytes.Contains(body, []byte(word)) {
					t.Fatalf("the reply holds %q: %s", word, body)
				}
			}
		})
	}
}

// A filesystem with no locks: held by no server, saying why.
func TestHostsLockless(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.probeLock = func(string) (ledger.ProbeResult, error) { return ledger.ProbeResult{State: ledger.ProbeNoLocks}, nil }
	crashServer(t, e)
	if l := e.hostsOf(t).Ledger; l.Holder != HolderNone || l.Note != NoteNoLocks {
		t.Fatalf("no locks: %+v", l)
	}
}

// fakePublication is a publication whose connection is up or down.
type fakePublication struct {
	PublishedSession
	up bool
}

func (p fakePublication) Connected() bool { return p.up }

// publisherStub is a publisher the host overview reads the URL of; it
// publishes nothing.
type publisherStub struct{ Publisher }

func (publisherStub) Server() string { return "https://yard.example.test/base" }

// A server publishing to a switchyard: its URL, the sessions published
// and those whose connection is up.
func TestHostsSwitchyardPublishing(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Rendezvous.Server = "https://yard.example.test/base" })
	e.srv.pubMu.Lock()
	e.srv.publisher = publisherStub{}
	e.srv.published = map[string]PublishedSession{"a": fakePublication{up: true}, "b": fakePublication{up: false}, "c": fakePublication{up: true}}
	e.srv.pubMu.Unlock()
	t.Cleanup(func() {
		e.srv.pubMu.Lock()
		e.srv.published, e.srv.publisher = map[string]PublishedSession{}, nil
		e.srv.pubMu.Unlock()
	})
	sw := e.hostsOf(t).Switchyard
	if sw == nil || *sw != (HostsSwitchyard{URL: "https://yard.example.test/base", Publishing: 3, Connected: 2}) {
		t.Fatalf("the switchyard: %+v", sw)
	}
}

// A switchyard serves no workbench page: no host overview.
func TestHostsNotOnASwitchyard(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	if status, body := e.get(t, "/api/hosts"); status != http.StatusNotFound || !strings.Contains(string(body), `"not_found"`) {
		t.Fatalf("on a switchyard: %d %s", status, body)
	}
}

// The route takes no query.
func TestHostsRefusesAQuery(t *testing.T) {
	e := newTestEnv(t, nil)
	if status, body := e.get(t, "/api/hosts?all=1"); status != http.StatusBadRequest || !strings.Contains(string(body), `"invalid_request"`) {
		t.Fatalf("a query: %d %s", status, body)
	}
}

// Admin only: no token, a host token and a share link are refused.
func TestHostsAdminOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	id := e.createSession("cat")
	_, out := e.do("POST", "/api/sessions/"+id+"/links", adminToken, map[string]any{"role": "view"})
	link, _ := out["token"].(string)
	if link == "" {
		t.Fatalf("the link: %v", out)
	}
	for name, token := range map[string]string{"no token": "", "a host token": "test-host-token", "a share link": link} {
		if resp, _ := e.do("GET", "/api/hosts", token, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
	}
}

// The route writes nothing: no file made or changed under the data
// directory. The history's index is left out of the comparison: its
// indexer writes it on a goroutine of its own, whatever a route does, and
// the route reads nothing of it.
func TestHostsWritesNothing(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	history := filepath.Join(f.srv.store.Dir(), "history") + string(filepath.Separator)
	state := func() map[string]string {
		out := treeState(t, f.srv.store.Dir())
		for p := range out {
			if strings.HasPrefix(p, history) {
				delete(out, p)
			}
		}
		return out
	}
	before := state()
	for range 3 {
		f.hostsOf(t)
	}
	after := state()
	for p, v := range after {
		if before[p] != v {
			t.Fatalf("%s changed: %q, was %q", p, v, before[p])
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Fatalf("%s went", p)
		}
	}
}

// treeState is every file under dir with its size and time.
func treeState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%v %d %d", d.IsDir(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Under another holder the host summary reads nothing of the ledger: it
// reports runs only when this server holds them. The page's read does.
func TestHostSummaryReadsNoLedgerItDoesNotHold(t *testing.T) {
	f := newDecisionFixture(t, nil, nil)
	lockWait(t, time.Millisecond)
	b := f.restart()
	waitUntil(t, "the second server's wait given up", func() bool { return !b.srv.ledger.Lock().GaveUpAt.IsZero() })
	var reads atomic.Int64
	b.srv.hostsLedgerRead = func() { reads.Add(1) }
	if _, ok := b.srv.hostSummaryBytes(t.Context()); !ok {
		t.Fatal("no summary")
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("the summary read the ledger %d times", n)
	}
	if r := b.hostsOf(t); r.Ledger.Holder != HolderOther || reads.Load() != 1 {
		t.Fatalf("the page's read: %+v, %d reads", r.Ledger, reads.Load())
	}
}
