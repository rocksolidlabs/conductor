package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
// a record of every session the server runs or hosts, kept by
// internal/journal under the data directory's history/. The server opens
// it with its data directory (a switchyard keeps none) and keeps each
// recorded session's record in step with the session.
//
// # The record follows the session (reconcile)
//
// A session's record is reconciled from the session's state, never
// assembled from the order of its callbacks. One function, reconcile,
// reads what is true of the session now and settles the difference into
// its record (journal.Settle): its name, its agent's own session, its
// crew, whether it has ended and how and when. Run twice, or after any
// other write, it changes nothing more.
//
// The sources of truth, read under recMu:
//
//   - which registration the record follows: the recorder registered for
//     its id (recCur). A recorder is one registration of a session: a
//     server session's, begun at its launch; a hosted one's, begun as the
//     hub adds it, and again each time its host registers it anew after
//     the hub let it go (the record taken up again, its instance kept).
//     A recorder that is not its id's (one a later registration replaced,
//     one already final) reconciles nothing and keeps nothing.
//   - whether the session is still the server's: the registry's entry for
//     its id is this registration's session (registry.Get).
//   - what the session is: the session object itself (Driver.Info), and a
//     server session's process (ProcessDone): its end is one the server
//     observed once its status has ended and, for a server session, its
//     process is gone (a stop that failed leaves the status stopped while
//     the process runs).
//
// reconcile runs, and the end is recorded, at exactly these moments:
//
//   - registration: once the registration is complete (a server session
//     in the registry, registered; a hosted one added, beginHostedRecord),
//     so whatever the session did before its recorder existed (an exit
//     before the registry held it) is read then.
//   - a hint (below): an observed end found is recorded.
//   - removal: the registry's OnRemove (recordRemoved) runs it at once,
//     before the removal returns. The session is no longer the registry's,
//     so this run is final: the session's observed end, or, without one,
//     host away for a hosted session and lost for a server one (its
//     process's state unknown), and the recorder is let go. Nothing is owed
//     afterwards. Should the hint of a removal be late (a hosted session
//     re-registered meanwhile), the registration it names is no longer
//     its id's and it changes nothing: the end of the record of a
//     registration is read from what is registered now, never from the
//     event.
//   - shutdown: every recorder still registered, final: the session's own
//     observed end, else the server stopped observing it (lost, host away)
//     (closeSessionRecords).
//   - the next start, for a record still open on disk (a process killed):
//     the journal marks it lost or host away as it loads (journal.Open).
//
// A hint is a callback that says something may have changed: a session's
// OnChange (its status, its name, its agent's session, its crew), and the
// wait for a stopped session's process (awaitProcessEnd). It carries no
// truth: it only runs reconcile, which reads the session again. A hint
// late, repeated, out of order or lost is harmless: the next reconcile, at
// the latest the removal's or the shutdown's, reads the same state. A
// write the journal could not make is made again by the writer; reconcile
// carries no retry state.
//
// The streams are not state: the activity entries (a file touched, a tool
// used, a report, a line typed) and the chat lines. Each is appended while
// its registration is its id's and its record is open, and dropped once
// the end is recorded (journal.Note, journal.Chat): what a session says as
// it ends reaches its record before the end is recorded only when it
// comes first. A question still open when the end is recorded is recorded
// as withdrawn by that end (the journal's closeQuestions), as the session's
// own end withdraws it, so its outcome does not hang on a line that comes
// later.
//
// One fact is still read in an order of events, and it is no lifecycle
// fact: the end commit (recordEndCommit), a git read the journal's writer
// makes after the end is recorded, through the session's folder, which the
// session closes as it leaves the registry. A session removed before the
// writer gets to it has none read (the record says so by its empty
// endCommit); a git read under recMu would hold every reconcile behind a
// disk.
//
// The server's recordDeliver, a test's seam, takes every hint and every
// stream callback of a recorded session, to deliver late, in another
// order, or not at all; registration, removal and shutdown run reconcile
// themselves.

// revisionTimeout bounds a record's git read.
const revisionTimeout = 2 * time.Second

// deliver runs f, a hint or a stream callback, or hands it to the test's
// seam (recordDeliver).
func (s *Server) deliver(f func()) {
	if d := s.recordDeliver.Load(); d != nil {
		(*d)(f)
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
	})
	if err != nil {
		s.log.Error("the session records could not be opened; sessions are not recorded", "err", err)
		return nil
	}
	s.recordsStop = make(chan struct{})
	s.recCur = map[string]*recorder{}
	return j
}

// closeSessionRecords closes the journal. At a shutdown (final), the
// sessions stopped and the hosts let go, every registration still
// recorded is reconciled a last time (the shutdown's moment): its
// session's observed end, else lost (host away) as the server stops
// observing it. The waits for processes still running end first
// (awaitProcessEnd). The journal's last pass then writes every record
// (journal.Close). A process killed instead of shut down writes none of
// this: what was not yet written is lost, and the next start marks a
// record left open lost (a hosted one host away), never ended.
func (s *Server) closeSessionRecords(final bool) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	s.stopRecordsOne.Do(func() { close(s.recordsStop) })
	if final {
		s.recMu.Lock()
		for _, rc := range s.recCur {
			s.reconcileLocked(rc, true)
		}
		s.recMu.Unlock()
	}
	j.Close()
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
// knows it (its common git directory's identity): what the end commit's
// read must find again (recordEndCommit), nil with no repository.
func (s *Server) launchRevision(where ext.Where, cwd string, dir *os.File) (rev gitcli.Revision, repo os.FileInfo) {
	if !s.recording() || where != ext.WhereNative {
		return gitcli.Revision{}, nil
	}
	at, still := openedPath(dir, cwd)
	ctx, cancel := context.WithTimeout(context.Background(), revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, at)
	if err != nil || !still() {
		return gitcli.Revision{}, nil
	}
	if rev.Common == "" {
		return rev, nil
	}
	if repo, err = os.Stat(rev.Common); err != nil {
		return gitcli.Revision{}, nil
	}
	return rev, repo
}

// openedPath is a path that names the directory dir holds open, for a git
// read in it: its descriptor's (/proc/<pid>/fd/<n>, where the system has
// one, as the process's own start uses, internal/pty), which nothing done
// to cwd moves; else cwd, which still says whether cwd named it as the
// read ended. It never names another directory than dir's: none (still
// false) when dir cannot be read.
func openedPath(dir *os.File, cwd string) (string, func() bool) {
	if dir == nil {
		return cwd, func() bool { return false }
	}
	want, err := dir.Stat()
	if err != nil {
		return cwd, func() bool { return false }
	}
	byHandle := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.FormatUint(uint64(dir.Fd()), 10)
	if fi, err := os.Stat(byHandle); err == nil && os.SameFile(fi, want) {
		return byHandle, func() bool { return true }
	}
	return cwd, func() bool {
		fi, err := os.Stat(cwd)
		return err == nil && os.SameFile(fi, want)
	}
}

// recorder is one registration of a recorded session (the package's
// reconcile): the instance of its record (journal.Instance), whether it is
// hosted, its folder (a server session's, fixed at its start), the session
// (d, once made; hs for a hosted one) and whether its registration is
// complete (attached). d, attached and watching are guarded by the
// server's recMu.
type recorder struct {
	in       journal.Instance
	hosted   bool
	cwd      string
	hs       *signal.HostedSession
	d        session.Driver
	attached bool
	// watching: a wait for its stopped session's process is under way
	// (awaitProcessEnd).
	watching bool
}

// beginServerRecord begins the record of a server session as it is made,
// before the session can keep an entry, and answers its recorder,
// registered for its id: nil when the session is not recorded. Its
// registration completes once the registry holds the session (registered).
func (s *Server) beginServerRecord(info session.Info, launched session.Launched, agentAdapter, agentName string, version string, where ext.Where, rev gitcli.Revision, repo os.FileInfo) *recorder {
	if !s.recording() {
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
	switch {
	case info.Crew != nil && info.Crew.Task != "":
		// An attempt's start is the engine's own, the ledger's.
		r.Start, r.StartFrom = "", journal.StartFromAttempt
	case r.Start != "":
		r.StartFrom = journal.StartFromLaunch
	}
	// Outside recMu: a server session's Begin writes its record.
	in, err := s.sessionRecords.Begin(r)
	if err != nil {
		if !errors.Is(err, journal.ErrNotOwner) {
			s.log.Warn("the session's record was not begun", "session", info.ID, "err", err)
		}
		return nil
	}
	if repo != nil && rev.Top != "" {
		s.recordRepos.Store(info.ID, repo)
	}
	rc := &recorder{in: in, cwd: info.Cwd}
	s.recMu.Lock()
	s.recCur[info.ID] = rc
	s.recMu.Unlock()
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
		s.deliver(func() { s.reconcile(rc) })
	}
	o.OnActivity = func(id string, e session.ActivityEntry, st session.AttentionState) {
		if activity != nil {
			activity(id, e, st)
		}
		s.deliver(func() { s.recordActivity(rc, e) })
	}
	o.OnChat = func(id string, m session.ChatMessage) {
		if chat != nil {
			chat(id, m)
		}
		s.deliver(func() { s.recordChat(rc, m) })
	}
}

// made is the server session rc's record is for, once made: what
// reconcile reads from then on.
func (s *Server) made(rc *recorder, l *session.Local) {
	if rc == nil {
		return
	}
	s.recMu.Lock()
	rc.d = l
	s.recMu.Unlock()
}

// registered completes rc's registration, its session now in the
// registry, and reconciles its record once (the registration's moment).
func (s *Server) registered(rc *recorder) {
	if rc == nil {
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	rc.attached = true
	s.reconcileLocked(rc, false)
}

// abandon removes the record of a session the registry refused.
func (s *Server) abandon(rc *recorder) {
	if rc == nil {
		return
	}
	s.recMu.Lock()
	if s.recCur[rc.in.ID()] == rc {
		delete(s.recCur, rc.in.ID())
	}
	s.recMu.Unlock()
	s.recordRepos.Delete(rc.in.ID())
	s.sessionRecords.Abandon(rc.in)
}

// beginHostedRecord is the hub's OnAdded: the record of a hosted session,
// from what its host registered, borrowing nothing of this server's
// machine (no probe, no git read, no Runtime), its registration complete
// and reconciled at once, and the session's own hooks that feed it. A
// session its host registers again once the hub let it go is a new
// registration of the same record (journal.Begin takes it up again): it
// replaces the earlier one for its id, whose hints and streams change
// nothing from then on.
func (s *Server) beginHostedRecord(hs *signal.HostedSession, owner string) signal.SessionHooks {
	if !s.recording() {
		return signal.SessionHooks{}
	}
	info := hs.Info()
	r := journal.Record{
		ID:        info.ID,
		Name:      info.Name,
		Kind:      string(info.Kind),
		Agent:     journal.Agent{ID: info.AgentID},
		Host:      journal.Host{Name: info.HostName, User: info.HostUser, Owner: owner, Hosted: true},
		Cwd:       info.Cwd,
		Branch:    info.Branch,
		StartedAt: info.CreatedAt,
	}
	// Under recMu, the Begin (which never waits on a disk for a hosted
	// record) and the registration together: a reconcile of the earlier
	// registration comes wholly before or wholly after.
	s.recMu.Lock()
	defer s.recMu.Unlock()
	in, err := s.sessionRecords.Begin(r)
	if err != nil {
		if !errors.Is(err, journal.ErrNotOwner) {
			s.log.Warn("the hosted session's record was not begun", "session", info.ID, "err", err)
		}
		return signal.SessionHooks{}
	}
	rc := &recorder{in: in, hosted: true, hs: hs, d: hs, attached: true}
	s.recCur[info.ID] = rc
	s.reconcileLocked(rc, false)
	return signal.SessionHooks{
		Activity: func(e session.ActivityEntry) { s.deliver(func() { s.recordActivity(rc, e) }) },
		Chat:     func(m session.ChatMessage) { s.deliver(func() { s.recordChat(rc, m) }) },
		Change:   func(session.Info) { s.deliver(func() { s.reconcile(rc) }) },
	}
}

// reconcile is a hint's: rc's record settled from its session as it is
// now (the package's reconcile).
func (s *Server) reconcile(rc *recorder) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.reconcileLocked(rc, false)
}

// reconcileLocked reads what is true of rc's session now and settles it
// into its record: nothing for a registration that is not its id's or not
// yet complete. The run is final when the session is no longer the
// registry's (removal) or closing is set (shutdown): the session's
// observed end, else lost (host away for a hosted session), and rc let go.
// A server session whose status ended while its process runs has its
// process waited for (awaitProcessEnd), a hint. The caller holds recMu.
func (s *Server) reconcileLocked(rc *recorder, closing bool) {
	id := rc.in.ID()
	if s.recCur[id] != rc || !rc.attached {
		return
	}
	d := rc.d
	info := d.Info()
	cur, _ := s.registry.Get(id)
	final := closing || cur != d
	st := journal.State{Name: info.Name, AgentSession: agentSessionOf(info.AgentSession), Crew: crewOfRef(info.Crew), Final: final}
	l, local := d.(*session.Local)
	switch {
	case observedEnd(d, info):
		end := journal.Ending{How: endingOf(info)}
		if info.EndedAt != nil {
			end.At = *info.EndedAt
		}
		st.End = &end
	case final && rc.hosted:
		st.End = &journal.Ending{How: journal.EndingHostAway, Lost: true}
	case final:
		st.End = &journal.Ending{How: journal.EndingLost, Lost: true}
	case local && info.Status.Ended() && !rc.watching:
		rc.watching = true
		go s.awaitProcessEnd(rc, l)
	}
	s.sessionRecords.Settle(rc.in, st)
	if final {
		delete(s.recCur, id)
	}
}

// recordRemoved is the registry's OnRemove for the journal: the removal's
// moment. The registration of the session that left, when it is still
// its id's, is reconciled at once, final (reconcileLocked).
func (s *Server) recordRemoved(id string, d session.Driver) {
	if s.sessionRecords == nil {
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if rc := s.recCur[id]; rc != nil && rc.d == d {
		s.reconcileLocked(rc, false)
	}
}

// observedEnd says the session's end is one the server observed: its
// status ended and, for a server session, its process gone (ProcessDone).
// A server session's status reads stopped once a stop was asked, whatever
// the process did (Local.Stop marks it so when the process's stop failed
// too): while that process runs its record is not ended.
func observedEnd(d session.Driver, info session.Info) bool {
	if !info.Status.Ended() {
		return false
	}
	l, ok := d.(*session.Local)
	if !ok {
		return true
	}
	select {
	case <-l.ProcessDone():
		return true
	default:
	}
	return false
}

// awaitProcessEnd is the hint of a server session whose status ended
// before its process did: once the process is gone, rc is reconciled;
// never after the journal's close begins (recordsStop).
func (s *Server) awaitProcessEnd(rc *recorder, l *session.Local) {
	select {
	case <-l.ProcessDone():
	case <-s.recordsStop:
		return
	}
	s.deliver(func() { s.reconcile(rc) })
}

// recordActivity is a stream: the record's part of the session's activity
// entry, while rc is its id's registration (journal.Note keeps it while
// the record is open): a file's path made absolute as the touched index
// keys it, under the session's own folder (a hosted one's as its host
// says it now).
func (s *Server) recordActivity(rc *recorder, e session.ActivityEntry) {
	a := journal.Activity{At: e.At, Type: e.Type, By: e.By, ByName: e.ByName, Message: e.Message, URL: e.URL, To: e.To, Tool: e.Tool, Op: e.Op, Path: e.Path}
	if e.Type == session.ActivityFile {
		if rc.hosted {
			a.Path = signal.HostAbs(rc.hs.Info().Cwd, e.Path)
		} else {
			a.Path = session.TouchedAbs(rc.cwd, e.Path)
		}
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if s.recCur[rc.in.ID()] == rc {
		s.sessionRecords.Note(rc.in, a)
	}
}

// recordChat is a stream: the record's part of the session's chat, its
// own lines, never a run's, while rc is its id's registration.
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
	if s.recCur[rc.in.ID()] == rc {
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

// isEnding says a status entry's message is a session's end.
func isEnding(msg string) bool {
	for _, st := range []session.Status{session.StatusExited, session.StatusStopped} {
		if msg == string(st) || strings.HasPrefix(msg, string(st)+" (exit ") {
			return true
		}
	}
	return false
}

// recordEndCommit is the journal's EndCommit: HEAD of a native server
// session's folder after its end, an observation; nothing for any other.
// The folder is the session's own, the one it was opened as (FolderDir),
// read through its handle where the system names one (openedPath), never
// whatever its path leads to now; and the repository found there must be
// the launch's, by the file system's identity of its common directory
// (launchRevision), never by the text of a path: a folder moved, a
// repository put in its place or made again at the same path lends no
// commit. A session the server no longer holds (its folder closed) has
// none read.
func (s *Server) recordEndCommit(ctx context.Context, r journal.Record) string {
	v, ok := s.recordRepos.LoadAndDelete(r.ID)
	if !ok || r.Host.Hosted || r.Where != string(ext.WhereNative) || r.Repo == "" {
		return ""
	}
	want := v.(os.FileInfo)
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
	at, still := openedPath(dir, l.Info().Cwd)
	ctx, cancel := context.WithTimeout(ctx, revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, at)
	if err != nil || !still() || rev.Common == "" {
		return ""
	}
	if fi, err := os.Stat(rev.Common); err != nil || !os.SameFile(fi, want) {
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
