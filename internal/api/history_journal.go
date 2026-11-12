package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/signal"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// The session journal (docs/plans/history.md, Program item HS1, PR-1):
// a record of every server session, kept by internal/journal under the
// data directory's history/. The server opens it with its data directory
// (a switchyard keeps none) and keeps each recorded session's record in
// step with the session; a hosted session's is in history_hosted.go.
//
// # The record follows the session (reconcile)
//
// A session's record is reconciled from the session's state, never
// assembled from the order of its callbacks. One step, reconcile, reads
// what is true of the session and settles it into its record through one
// journal write (journal.Settle); run twice, or after any other write, it
// changes nothing more. The sources of truth are two: the session object
// (its Info and its process's ProcessDone), which stays readable after it
// has left the registry, and the registry's membership (registry.Get
// answers this very object). Each recorded session has one recorder, in
// recCur under its id, from its Begin (the admission rule); a recorder no
// longer there reconciles nothing and keeps nothing.
//
// reconcile runs at exactly these moments: the registration's completion
// (registered), so what the session did before the registry held it is
// read then; a hint; the registry's removal (recordRemoved), final; and
// the shutdown's final pass (closeSessionRecords), final. A hint is a
// callback that says something may have changed: the session's OnChange
// and the wait for a stopped session's process (awaitProcessEnd). It
// carries no truth: it only runs reconcile. A hint late, repeated, out of
// order or lost is harmless.
//
// The snapshot rule (reconcileLocked): under recMu, membership first, then
// the session's Info, then whether its process is gone. The run is final
// when membership was read absent, or in the final pass; a final run lets
// its recorder go. An end is observed when the process is gone: a final
// read's end is what that read shows (the published end, else exited with
// no exit code at the read's time; the process running, lost, for good);
// a non-final read records an end only once the status is published too.
// Membership read before the state is what makes a non-final run safe: a
// removal after it runs its reconcile after this one (recMu), reads the
// absence and reads the state again.
//
// The admission rule: the final pass marks the records closed under recMu
// (recordsClosed) and, in the same hold, settles every recorder that has
// its session object; one with none is left open on disk, for the next
// start to close as lost. After the mark nothing is begun, no recorder is
// put in place, and nothing reaches a record. An abandonment (the registry
// refused the session) marks its recorder leaving and keeps it in recCur
// until the journal's own removal has returned; the final pass waits for it.
//
// The streams (the activity entries and the chat lines) are evidence, not
// state: each is appended while its recorder is held and its record is
// open, and dropped once the end is recorded (journal.Note, journal.Chat).
//
// recMu is never held across a file write, a session call that waits, the
// end commit's git read, or a wait for another goroutine. The lock order
// is recMu, the registry's lock, the session's own lock, the journal's mu.
//
// One fact is still read in an order of events: the end commit
// (recordEndCommit), a git read the journal's writer makes after the end
// is recorded, through the session's folder, which the session closes as
// it leaves the registry. A session removed before the writer gets to it
// has none read (its endCommit empty).
//
// The test seams: recordDeliver takes every hint and stream callback, with
// its session's id and what it carries, to deliver late, in another order,
// or not at all; recordHook is run at each
// stage recordSeam names.

// revisionTimeout bounds a record's git read.
const revisionTimeout = 2 * time.Second

// deliver runs f, a hint or a stream callback of the session id, or hands
// it to the test's seam (recordDeliver) with what it carries: the
// session.ActivityEntry or session.ChatMessage of a stream, nil for a hint.
func (s *Server) deliver(id string, what any, f func()) {
	if d := s.recordDeliver.Load(); d != nil {
		(*d)(id, what, f)
		return
	}
	f()
}

// openSessionRecords opens the journal over st: nil when it cannot be.
func (s *Server) openSessionRecords(st *store.Store) *journal.Journal {
	j, err := journal.Open(st, journal.Options{
		Log:       s.log,
		EndCommit: s.recordEndCommit,
		Denied:    s.recordDenied,
		Removed:   s.dropRecordRepo,
	})
	if err != nil {
		s.log.Error("the session records could not be opened; sessions are not recorded", "err", err)
		return nil
	}
	s.recordsStop = make(chan struct{})
	s.recCur = map[string]*recorder{}
	return j
}

// closeSessionRecords is the final pass, last in the shutdown (final),
// after the sessions' stops and the other stores (the admission rule):
// under recMu it marks the records closed (recordsClosed) and ends the
// process waits (recordsStop), and in the same hold settles final every
// recorder that has its session object and is not leaving, registered or
// not, by the snapshot rule; one with no session object yet is left as
// its Begin wrote it, open on disk, for the next start's recovery to close
// as lost. It then waits, recMu let go, for each recorder that was leaving
// to have gone (its abandonment's delete), lets every recorder still held
// go, and closes the journal, whose last pass writes what was settled.
// Without final (WaitBackground) nothing is settled. A process killed
// instead runs none of this: what was not yet written is lost, and the
// next start marks each record left open lost, never ended.
func (s *Server) closeSessionRecords(final bool) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	s.recMu.Lock()
	s.recordsClosed = true
	s.stopRecordsOne.Do(func() { close(s.recordsStop) })
	var leaving []*recorder
	for _, rc := range s.recCur {
		switch {
		case rc.leaving:
			leaving = append(leaving, rc)
		case final:
			s.reconcileLocked(rc, true)
		}
	}
	s.recMu.Unlock()
	for _, rc := range leaving {
		s.recordSeam("awaitLeaving", rc.in.ID())
		<-rc.gone
	}
	s.recMu.Lock()
	clear(s.recCur)
	s.recMu.Unlock()
	j.Close()
	// What no end commit read let go: one Close gave up on (a record
	// removed before its read let its repository go then, Removed).
	s.recordRepos.Range(func(id, _ any) bool {
		s.dropRecordRepo(id.(string))
		return true
	})
}

// recording says a session that starts now is recorded: the one check,
// at the one place a record is begun or taken up again (beginServerRecord,
// beginHostedRecord); a session begun without a record has no recorder,
// and nothing it does reaches the journal, a record kept of its id from an
// earlier start included.
func (s *Server) recording() bool {
	return s.sessionRecords != nil && s.cfg.HistorySessions()
}

// launchRevision is what a launch's folder is at before its process
// starts, read in the folder opened and checked (dir), the one the process
// starts in, never again at its path: read for a native session only (a
// Runtime elsewhere runs on a tree this server does not observe); a read
// that fails or times out, or whose folder could not be named as opened,
// leaves it empty. repo is the repository it names, as the file system
// knows it (gitcli.OpenLaunchRepo): what the end commit's read must find
// again (recordEndCommit), nil with no repository; its caller closes it
// unless beginServerRecord takes it.
func (s *Server) launchRevision(where ext.Where, cwd string, dir *os.File) (rev gitcli.Revision, repo *gitcli.LaunchRepo) {
	if !s.recording() || where != ext.WhereNative {
		return gitcli.Revision{}, nil
	}
	at, still := gitcli.OpenedPath(dir, cwd)
	ctx, cancel := context.WithTimeout(context.Background(), revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, at)
	if err != nil || !still() {
		return gitcli.Revision{}, nil
	}
	if rev.Common == "" {
		return rev, nil
	}
	if repo = gitcli.OpenLaunchRepo(rev.Common); repo == nil {
		return gitcli.Revision{}, nil
	}
	return rev, repo
}

// dropRecordRepo lets id's launch repository go: its end commit read, or
// none to be read (an abandonment, a lost end, the record removed by
// Forget, ForgetAll or Prune whether or not its delete succeeded, the
// journal's Removed, the journal closed).
func (s *Server) dropRecordRepo(id string) {
	if v, ok := s.recordRepos.LoadAndDelete(id); ok {
		v.(*gitcli.LaunchRepo).Close()
	}
}

// recorder is a recorded server session (the package's reconcile): the
// instance of its record (journal.Instance), its folder (fixed at its
// start), the session object once made (d), whether its registration is
// complete (attached), whether its process is being waited for
// (watching), and whether it is leaving (an abandonment under way, gone
// closed once it has left recCur). d, attached, watching and leaving are
// guarded by the server's recMu.
type recorder struct {
	in       journal.Instance
	cwd      string
	d        *session.Local
	attached bool
	watching bool
	leaving  bool
	gone     chan struct{}
	// h is a hosted session's recorder's session (history_hosted.go), d
	// nil; as is the agent session this registration last settled into the
	// record (the record's is what a capture goes on from), and endCommit
	// what its host sent before its end. Guarded by recMu.
	h         *signal.HostedSession
	as        *session.AgentSession
	endCommit string
}

// recordSeam runs the test's hook at stage (recordHook), with the
// session's id: "afterBegin" (a Begin returned, before its recorder is put
// in place), "beforeMade" (the recorder in place, before session.NewLocal),
// "afterRegistryAdd" (the registry holds the session, before registered),
// "beforeAbandon" (the recorder marked leaving, before journal.Abandon),
// "beforeRemovedReconcile" (OnRemove after the registry's delete, before
// its reconcile), "betweenReads" (a reconcile between its membership
// read and its state read, recMu held) and "awaitLeaving" (the final pass,
// recMu let go, about to wait for a leaving recorder to have gone).
func (s *Server) recordSeam(stage, id string) {
	if h := s.recordHook.Load(); h != nil {
		(*h)(stage, id)
	}
}

// beginServerRecord begins the record of a server session as it is made,
// before the session can keep an entry, and answers its recorder, in
// recCur from now on: nil when the session is not recorded. Nothing is
// begun once the records are closed (closeSessionRecords' mark), and a
// Begin under way as the mark is set puts no recorder in place: its record
// stays open on disk, for the next start's recovery to close as lost (the
// admission rule). It takes repo (launchRevision's), kept until the end
// commit is read, else closed.
func (s *Server) beginServerRecord(info session.Info, launched session.Launched, agentAdapter, agentName string, version string, where ext.Where, rev gitcli.Revision, repo *gitcli.LaunchRepo) *recorder {
	kept := false
	defer func() {
		if !kept {
			repo.Close()
		}
	}()
	if !s.recording() {
		return nil
	}
	s.recMu.Lock()
	closed := s.recordsClosed
	s.recMu.Unlock()
	if closed {
		return nil
	}
	host, _ := os.Hostname()
	r := journal.Record{
		ID:          info.ID,
		Name:        info.Name,
		Kind:        string(info.Kind),
		Agent:       journal.Agent{ID: info.AgentID, Name: agentName, Adapter: agentAdapter, Version: version, Session: agentSessionOf(info.AgentSession)},
		Host:        journal.Host{Name: host, User: serverUser()},
		Where:       string(where),
		Cwd:         info.Cwd,
		Repo:        rev.Top,
		RepoCommon:  rev.Common,
		Branch:      rev.Branch,
		Start:       rev.Commit,
		Launch:      journal.LaunchOf(launched.Args, launched.Env, launched.Yolo, launched.Renamed),
		Crew:        crewOfRef(info.Crew),
		ResumedFrom: info.ResumedFrom,
		StartedAt:   info.CreatedAt,
	}
	// A registered account's binding, by its id and source alone, and the
	// model; nothing of the tool's own login or of a login its entry or
	// its launch sets, which a resume from the record takes as a launch of
	// default gives it then, as it did before bindings.
	if launched.Account.Source == session.SourceAccount {
		r.Launch.Account, r.Launch.Source = launched.Account.ID, launched.Account.Source
	}
	r.Launch.Model = launched.Model
	switch {
	case info.Crew != nil && info.Crew.Task != "":
		// An attempt's start is the engine's own, the ledger's.
		r.Start, r.StartFrom = "", journal.StartFromAttempt
	case r.Start != "":
		r.StartFrom = journal.StartFromLaunch
	}
	// Outside recMu: Begin writes the record.
	in, err := s.sessionRecords.Begin(r)
	if err != nil {
		if !errors.Is(err, journal.ErrNotOwner) && !errors.Is(err, journal.ErrClosed) {
			s.log.Warn("the session's record was not begun", "session", info.ID, "err", err)
		}
		return nil
	}
	s.recordSeam("afterBegin", info.ID)
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.recordsClosed {
		return nil
	}
	if repo != nil && rev.Top != "" {
		s.recordRepos.Store(info.ID, repo)
		kept = true
	}
	rc := &recorder{in: in, cwd: info.Cwd, gone: make(chan struct{})}
	s.recCur[info.ID] = rc
	return rc
}

// recordHooks has the session's own hooks in o feed rc's record after
// them: its changes as hints (reconcile), its activity and its chat as
// streams. Nothing changes for a session not recorded (rc nil).
func (s *Server) recordHooks(rc *recorder, o *session.Options) {
	if rc == nil {
		return
	}
	change, activity, chat := o.OnChange, o.OnActivity, o.OnChat
	o.OnChange = func(info session.Info) {
		if change != nil {
			change(info)
		}
		s.deliver(rc.in.ID(), nil, func() { s.reconcile(rc) })
	}
	o.OnActivity = func(id string, e session.ActivityEntry, st session.AttentionState) {
		if activity != nil {
			activity(id, e, st)
		}
		s.deliver(rc.in.ID(), e, func() { s.recordActivity(rc, e) })
	}
	o.OnChat = func(id string, m session.ChatMessage) {
		if chat != nil {
			chat(id, m)
		}
		s.deliver(rc.in.ID(), m, func() { s.recordChat(rc, m) })
	}
}

// held says rc is a recorder the records still follow: in recCur, not
// leaving, the records not closed. The caller holds recMu.
func (s *Server) held(rc *recorder) bool {
	return s.recCur[rc.in.ID()] == rc && !rc.leaving && !s.recordsClosed
}

// made is the server session rc's record is for, once made: what
// reconcile reads from then on. A recorder the final pass let go takes
// none.
func (s *Server) made(rc *recorder, l *session.Local) {
	if rc == nil {
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.held(rc) {
		rc.d = l
	}
}

// registered completes rc's registration, its session now in the
// registry, and reconciles its record once (the registration's moment):
// what the session did before the registry held it is read now.
func (s *Server) registered(rc *recorder) {
	if rc == nil {
		return
	}
	s.recordSeam("afterRegistryAdd", rc.in.ID())
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if !s.held(rc) || rc.d == nil {
		return
	}
	rc.attached = true
	s.reconcileLocked(rc, false)
}

// abandon removes the record of a session the registry refused: its
// recorder marked leaving and kept in recCur, visible, while the journal's
// own removal runs outside recMu (journal.Abandon, which returns once the
// delete is made or has failed); then the recorder leaves recCur and says
// so (gone), which the final pass may wait on. A recorder the final pass
// took first is not removed: its record stays as that pass settled it.
func (s *Server) abandon(rc *recorder) {
	if rc == nil {
		return
	}
	id := rc.in.ID()
	s.recMu.Lock()
	if s.recCur[id] != rc {
		s.recMu.Unlock()
		return
	}
	rc.leaving = true
	s.recMu.Unlock()
	s.recordSeam("beforeAbandon", id)
	s.dropRecordRepo(id)
	s.sessionRecords.Abandon(rc.in)
	s.recMu.Lock()
	if s.recCur[id] == rc {
		delete(s.recCur, id)
	}
	close(rc.gone)
	s.recMu.Unlock()
}

// reconcile is a hint's: rc's record settled from its session as it is
// now (the package's reconcile).
func (s *Server) reconcile(rc *recorder) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.reconcileLocked(rc, false)
}

// reconcileLocked reads what is true of rc's session and settles it into
// its record, by the snapshot rule: nothing for a recorder not held or
// leaving; outside the final pass (finalPass false) nothing for one whose
// registration is not complete, and in it nothing for one with no session
// object. Then membership first (the registry's entry for the id is rc's
// session), then the session's Info (one snapshot under its lock), then
// whether its process is gone. The run is final when membership was read
// absent or in the final pass, and a final run lets rc go. An end is
// observed when the process is gone: with the status published, that end;
// at a final read before it is published, exited with no exit code at the
// read's time. A final read with the process running records lost. A
// non-final read records an end only once the status is published too,
// and waits for a process whose status ended while it runs
// (awaitProcessEnd). The caller holds recMu.
func (s *Server) reconcileLocked(rc *recorder, finalPass bool) {
	if rc.h != nil {
		s.reconcileHostedLocked(rc, finalPass)
		return
	}
	id := rc.in.ID()
	if s.recCur[id] != rc || rc.leaving {
		return
	}
	if finalPass {
		if rc.d == nil {
			return
		}
	} else if s.recordsClosed || !rc.attached || rc.d == nil {
		return
	}
	d := rc.d
	cur, ok := s.registry.Get(id)
	member := ok && cur == session.Driver(d)
	s.recordSeam("betweenReads", id)
	info := d.Info()
	gone := false
	select {
	case <-d.ProcessDone():
		gone = true
	default:
	}
	final := finalPass || !member
	st := journal.State{Name: info.Name, AgentSession: agentSessionOf(info.AgentSession), Crew: crewOfRef(info.Crew), Final: final}
	switch {
	case gone && info.Status.Ended():
		end := journal.Ending{How: endingOf(info)}
		if info.EndedAt != nil {
			end.At = *info.EndedAt
		}
		st.End = &end
	case gone && final:
		st.End = &journal.Ending{How: string(session.StatusExited), At: time.Now()}
	case final:
		st.End = &journal.Ending{How: journal.EndingLost, Lost: true}
		s.dropRecordRepo(id) // a lost end has no end commit
	case info.Status.Ended() && !rc.watching:
		rc.watching = true
		go s.awaitProcessEnd(rc, d)
	}
	s.sessionRecords.Settle(rc.in, st)
	if final {
		delete(s.recCur, id)
	}
}

// recordRemoved is the registry's OnRemove for the journal: the removal's
// moment. The recorder of the session that left, when it still holds that
// session, is reconciled at once; it reads the absence, so the run is
// final (reconcileLocked).
func (s *Server) recordRemoved(id string, d session.Driver) {
	if s.sessionRecords == nil {
		return
	}
	s.recordSeam("beforeRemovedReconcile", id)
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if rc := s.recCur[id]; rc != nil && ((rc.d != nil && session.Driver(rc.d) == d) || (rc.h != nil && session.Driver(rc.h) == d)) {
		s.reconcileLocked(rc, false)
	}
}

// awaitProcessEnd is the hint of a server session whose status ended
// before its process did: once the process is gone, rc is reconciled;
// never after the final pass's mark (recordsStop).
func (s *Server) awaitProcessEnd(rc *recorder, l *session.Local) {
	select {
	case <-l.ProcessDone():
	case <-s.recordsStop:
		return
	}
	s.deliver(rc.in.ID(), nil, func() { s.reconcile(rc) })
}

// recordActivity is a stream: the record's part of the session's activity
// entry, while rc is held (journal.Note keeps it while the record is
// open): a file's path made absolute as the touched index keys it, under
// the session's own folder.
func (s *Server) recordActivity(rc *recorder, e session.ActivityEntry) {
	a := journal.Activity{At: e.At, Type: e.Type, By: e.By, ByName: e.ByName, Message: e.Message, URL: e.URL, To: e.To, Tool: e.Tool, Op: e.Op, Path: e.Path}
	if e.Type == session.ActivityFile {
		a.Path = session.TouchedAbs(rc.cwd, e.Path)
		if rc.h != nil {
			a.Path = signal.HostAbs(rc.cwd, e.Path)
		}
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.held(rc) {
		s.sessionRecords.Note(rc.in, a)
	}
}

// recordChat is a stream: the record's part of the session's chat, its
// own lines, never a run's, while rc is held.
func (s *Server) recordChat(rc *recorder, m session.ChatMessage) {
	if m.Scope != "" && m.Scope != proto.ChatScopeSession {
		return
	}
	c := journal.ChatLine{ID: m.ID, At: m.At, Kind: m.Kind, Event: m.Event, ByName: m.By.Name, ByRole: string(m.By.Role), Text: m.Text, Ref: m.Ref, To: m.To}
	for _, o := range m.Options {
		c.Choices = append(c.Choices, o.Label)
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.held(rc) {
		s.sessionRecords.Chat(rc.in, c)
	}
}

// endingOf is how an ended session's Info says it ended, in the words of
// its status entry: "exited (exit 0)", "stopped".
func endingOf(info session.Info) string {
	if info.ExitCode != nil {
		return fmt.Sprintf("%s (exit %d)", info.Status, *info.ExitCode)
	}
	return string(info.Status)
}

// recordEndCommit is the journal's EndCommit: HEAD of a native server
// session's folder after its end, an observation; nothing for any other.
// The folder is the session's own, the one it was opened as (FolderDir),
// read through its handle where the system names one (gitcli.OpenedPath),
// never whatever its path leads to now; and the repository found there
// must be the launch's, by the file system's identity of its common
// directory (launchRevision), held open from the launch where the system
// would give it to a directory made after (gitcli.LaunchRepo), never by
// the text of a path: a folder moved, a
// repository put in its place or made again at the same path lends no
// commit. A session the server no longer holds (its folder closed) has
// none read.
func (s *Server) recordEndCommit(ctx context.Context, r journal.Record) string {
	v, ok := s.recordRepos.LoadAndDelete(r.ID)
	if !ok {
		return ""
	}
	repo := v.(*gitcli.LaunchRepo)
	defer repo.Close()
	if r.Where != string(ext.WhereNative) || r.Repo == "" {
		return ""
	}
	d, ok := s.registry.Get(r.ID)
	l, local := d.(*session.Local)
	if !ok || !local {
		return ""
	}
	dir, err := l.FolderDir()
	if err != nil {
		return ""
	}
	defer dir.Close()
	at, still := gitcli.OpenedPath(dir, l.Info().Cwd)
	ctx, cancel := context.WithTimeout(ctx, revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, at)
	if err != nil || !still() || !repo.Is(rev.Common) {
		return ""
	}
	return rev.Commit
}

// recordDenied is the journal's Denied: the server's deny list, which a
// server session's file reads keep to (fileDeny), as /api/touched applies
// it; asked of a server session's paths alone, its own list being the
// server's, so it holds once the session has left the registry too.
func (s *Server) recordDenied(path string) bool {
	return session.DeniedBy(path, s.fileDeny())
}

func agentSessionOf(as *session.AgentSession) *journal.AgentSession {
	if as == nil {
		return nil
	}
	return &journal.AgentSession{ID: as.ID, Source: as.Source, Resumable: as.Resumable}
}

func crewOfRef(c *session.CrewRef) *journal.Crew {
	if c == nil {
		return nil
	}
	out := &journal.Crew{RunID: c.RunID, CrewID: c.CrewID, Member: c.Member, Task: c.Task, Attempt: c.Attempt}
	if c.Brakeman != nil {
		out.Brakeman = c.Brakeman.Request
	}
	return out
}

// historyUnavailable answers a History route on a server that keeps no
// records: without a data directory, and on a switchyard.
func (s *Server) historyUnavailable(w http.ResponseWriter) bool {
	if s.sessionRecords == nil {
		writeError(w, http.StatusNotFound, "history_unavailable", "this server keeps no history")
		return true
	}
	return false
}

// handleHistorySessions answers GET /api/history/sessions: a page of the
// records' rows, newest first.
func (s *Server) handleHistorySessions(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > journal.MaxPage {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be 1 to 100")
			return
		}
		limit = n
	}
	after := q.Get("after")
	if len(after) > 64 {
		writeError(w, http.StatusBadRequest, "bad_cursor", "not a cursor of this list")
		return
	}
	page, err := s.sessionRecords.List(after, limit)
	if errors.Is(err, journal.ErrBadCursor) {
		writeError(w, http.StatusBadRequest, "bad_cursor", "not a cursor of this list")
		return
	}
	if err != nil {
		s.log.Warn("the session records could not be listed", "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the session records could not be read")
		return
	}
	out := map[string]any{"records": page.Records}
	if page.Next != "" {
		out["next"] = page.Next
	}
	if ro, _ := s.sessionRecords.ReadOnly(); ro {
		out["readOnly"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHistorySession answers GET /api/history/sessions/{id}: the record
// whole, live while the registry lists its session running.
func (s *Server) handleHistorySession(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	rec, newer, err := s.sessionRecords.Get(id)
	if errors.Is(err, journal.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such record")
		return
	}
	if err != nil {
		s.log.Warn("a session's record could not be read", "session", id, "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the record could not be read")
		return
	}
	live := false
	if d, ok := s.registry.Get(id); ok && !d.Info().Status.Ended() {
		live = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": rec, "live": live, "newer": newer})
}

// handleForgetHistorySession answers DELETE /api/history/sessions/{id}.
func (s *Server) handleForgetHistorySession(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	if d, ok := s.registry.Get(id); ok && !d.Info().Status.Ended() {
		writeError(w, http.StatusConflict, "live_session", "the session is still running")
		return
	}
	switch err := s.sessionRecords.Forget(id); {
	case err == nil:
		if !s.dropFromHistory(w, []string{id}, false) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, journal.ErrNotFound):
		// A record already gone from the journal (a Forget again after
		// one the index could not take) still has its rows dropped: the
		// drop does not rest on the journal holding the record.
		if s.history != nil && s.history.owns() && !s.dropFromHistory(w, []string{id}, false) {
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such record")
	case errors.Is(err, journal.ErrLive):
		writeError(w, http.StatusConflict, "live_session", "the session is still running")
	case errors.Is(err, journal.ErrResumePending):
		writeError(w, http.StatusConflict, "resume_pending", "a resume of this record is being started")
	case errors.Is(err, journal.ErrNotOwner):
		writeError(w, http.StatusConflict, "history_read_only", "another server owns the session records")
	default:
		s.log.Warn("a session's record could not be forgotten", "session", id, "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the record could not be removed")
	}
}

// dropFromHistory removes the forgotten records' rows from the history's
// index before a Forget route answers (ids, or every record the journal no
// longer answers for with all): false, with the answer written, when the
// index could not take them. The records are gone from the journal either
// way; the index drops them when it can, before anything reads it.
func (s *Server) dropFromHistory(w http.ResponseWriter, ids []string, all bool) bool {
	if s.history == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyDropTimeout)
	defer cancel()
	if err := s.history.dropSessions(ctx, ids, all); err != nil {
		s.log.Warn("forgotten records could not be dropped from the history's index", "err", err)
		writeError(w, http.StatusServiceUnavailable, "history_index_failed", "the record is removed, but the history's index could not drop it yet: it does before it is read again")
		return false
	}
	return true
}

// historyDropTimeout bounds a Forget's wait for the index's drop.
const historyDropTimeout = 30 * time.Second

// handleForgetHistorySessions answers DELETE /api/history/sessions: every
// record neither live nor pending forgotten, those kept counted.
func (s *Server) handleForgetHistorySessions(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	forgotten, kept, err := s.sessionRecords.ForgetAll()
	if errors.Is(err, journal.ErrNotOwner) {
		writeError(w, http.StatusConflict, "history_read_only", "another server owns the session records")
		return
	}
	// The records removed leave the index before the answer, those a
	// failed delete kept or not.
	if !s.dropFromHistory(w, nil, true) {
		return
	}
	switch {
	case err != nil:
		s.log.Warn("the session records could not all be forgotten", "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the records could not all be removed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": forgotten, "kept": kept})
}
