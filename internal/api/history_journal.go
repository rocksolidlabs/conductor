package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
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
// it with its data directory (a switchyard keeps none) and feeds it from
// the places a session's evidence passes. A recorded session holds its
// record through a recorder, begun with it: a server session's at its
// launch (createLocalSession: Begin, with the launch revision read before
// its process starts), whose hooks wrap the session's own (recordHooks); a
// hosted one's as the hub adds it (OnAdded), whose hooks the session keeps
// (signal.SessionHooks). Every write names the record's instance the
// recorder was begun with (journal.Instance), never an id alone: what a
// session says after its record was forgotten is dropped, whatever session
// holds its id by then, and what it says after it left the registry is
// judged by that instance too. The registry's OnRemove (recordRemoved)
// hands over its last state; the shutdown's last look takes each
// session's end (closeSessionRecords).

// revisionTimeout bounds a record's git read.
const revisionTimeout = 2 * time.Second

// openSessionRecords opens the journal over st: nil when it cannot be.
func (s *Server) openSessionRecords(st *store.Store) *journal.Journal {
	j, err := journal.Open(st, journal.Options{
		Log:       s.log,
		Current:   s.recordCurrent,
		EndCommit: s.recordEndCommit,
		Denied:    s.recordDenied,
	})
	if err != nil {
		s.log.Error("the session records could not be opened; sessions are not recorded", "err", err)
		return nil
	}
	s.recordsStop = make(chan struct{})
	return j
}

// beforeRecordsWait, when set, runs as the shutdown's last look begins to
// wait for a session's end to be told (closeSessionRecords): a test's
// seam.
var beforeRecordsWait func(id string)

// closeSessionRecords closes the journal. At a shutdown (reconcile), the
// sessions stopped, it takes one ordered look at every recorded session
// still listed before the journal closes: a server session that ended is
// waited for until its end has been handed to its hooks (EndTold: its
// status entry, the question it withdrew, its change), as long as ctx
// allows, so what it said as it ended is in its record; then its end is
// taken from the session itself when the server observed it (observedEnd),
// and its metadata read once more. The waits for processes still running
// end first (awaitProcessEnd). The journal's last pass then writes every
// record (journal.Close). A process killed instead of shut down writes
// none of this: what was not yet written is lost, and the next start marks
// a record left open lost (a hosted one host away), never ended.
func (s *Server) closeSessionRecords(ctx context.Context, reconcile bool) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	s.stopRecordsOne.Do(func() { close(s.recordsStop) })
	if reconcile {
		var listed []session.Driver
		s.registry.Each(func(d session.Driver) { listed = append(listed, d) })
		for _, d := range listed {
			v, ok := s.recorders.Load(d)
			if !ok {
				continue
			}
			rc := v.(*recorder)
			if l, local := d.(*session.Local); local && l.Info().Status.Ended() {
				if f := beforeRecordsWait; f != nil {
					f(l.Info().ID)
				}
				select {
				case <-l.EndTold():
				case <-ctx.Done():
				}
			}
			info := d.Info()
			if s.observedEnd(rc, d, info) {
				end := journal.Ending{How: endingOf(info)}
				if info.EndedAt != nil {
					end.At = *info.EndedAt
				}
				j.End(rc.in, end)
			}
			j.Changed(rc.in)
		}
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

// recorder is a recorded session's hold on its record: the instance its
// record was begun with (journal.Instance), and what its hooks need of the
// session without the registry, which may have let it go: whether it is
// hosted, its folder (a server session's, fixed at its start; a hosted
// one's as its host says it now), the session itself once made.
type recorder struct {
	s      *Server
	in     journal.Instance
	hosted bool
	cwd    string
	hs     *signal.HostedSession
	local  atomic.Pointer[session.Local]
	// waiting: awaitProcessEnd waits for its process.
	waiting atomic.Bool
}

// beginServerRecord begins the record of a server session as it is made,
// before the session can keep an entry, and answers its recorder: nil when
// the session is not recorded.
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
	return &recorder{s: s, in: in, cwd: info.Cwd}
}

// recordHooks has the session's own hooks in o feed rc's record after
// them: its activity, its chat and its changes. Nothing changes for a
// session not recorded (rc nil).
func recordHooks(rc *recorder, o *session.Options) {
	if rc == nil {
		return
	}
	change, activity, chat := o.OnChange, o.OnActivity, o.OnChat
	o.OnChange = func(info session.Info) {
		if change != nil {
			change(info)
		}
		rc.changed()
	}
	o.OnActivity = func(id string, e session.ActivityEntry, st session.AttentionState) {
		if activity != nil {
			activity(id, e, st)
		}
		rc.activity(e)
	}
	o.OnChat = func(id string, m session.ChatMessage) {
		if chat != nil {
			chat(id, m)
		}
		rc.chat(m)
	}
}

// made is the session rc's record is for, once made: its hooks and the
// server's look for it by it from then on.
func (rc *recorder) made(l *session.Local) {
	if rc == nil {
		return
	}
	rc.local.Store(l)
	rc.s.recorders.Store(session.Driver(l), rc)
}

// abandon removes the record of a session the registry refused.
func (rc *recorder) abandon() {
	if rc == nil {
		return
	}
	if l := rc.local.Load(); l != nil {
		rc.s.recorders.Delete(session.Driver(l))
	}
	rc.s.recordRepos.Delete(rc.in.ID())
	rc.s.sessionRecords.Abandon(rc.in)
}

// beginHostedRecord is the hub's OnAdded: the record of a hosted session,
// from what its host registered, borrowing nothing of this server's
// machine (no probe, no git read, no Runtime), and the session's own hooks
// that feed it.
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
	in, err := s.sessionRecords.Begin(r)
	if err != nil {
		if !errors.Is(err, journal.ErrNotOwner) {
			s.log.Warn("the hosted session's record was not begun", "session", info.ID, "err", err)
		}
		return signal.SessionHooks{}
	}
	rc := &recorder{s: s, in: in, hosted: true, hs: hs}
	s.recorders.Store(session.Driver(hs), rc)
	return signal.SessionHooks{Activity: rc.activity, Chat: rc.chat, Change: func(session.Info) { rc.changed() }}
}

// changed marks the record's metadata stale: the journal reads the session
// as it is when it gets to it (recordCurrent).
func (rc *recorder) changed() {
	rc.s.sessionRecords.Changed(rc.in)
}

// activity is the record's part of the session's activity entry: a file's
// path made absolute as the touched index keys it, under the session's own
// folder, and a status entry that is the session's end taken as its end,
// once observed: a hosted one's as its host says, a server one's only once
// its process is gone (observedEnd). It never waits.
func (rc *recorder) activity(e session.ActivityEntry) {
	j := rc.s.sessionRecords
	a := journal.Activity{At: e.At, Type: e.Type, By: e.By, ByName: e.ByName, Message: e.Message, URL: e.URL, To: e.To, Tool: e.Tool, Op: e.Op, Path: e.Path}
	if e.Type == session.ActivityFile {
		if rc.hosted {
			a.Path = signal.HostAbs(rc.hs.Info().Cwd, e.Path)
		} else {
			a.Path = session.TouchedAbs(rc.cwd, e.Path)
		}
	}
	j.Note(rc.in, a)
	if e.Type != session.ActivityStatus || !isEnding(e.Message) {
		return
	}
	if !rc.hosted {
		l := rc.local.Load()
		if l == nil || !rc.s.observedEnd(rc, l, l.Info()) {
			return
		}
	}
	j.End(rc.in, journal.Ending{At: e.At, How: e.Message})
}

// chat is the record's part of the session's chat: its own lines, never a
// run's.
func (rc *recorder) chat(m session.ChatMessage) {
	if m.Scope != "" && m.Scope != proto.ChatScopeSession {
		return
	}
	c := journal.ChatLine{ID: m.ID, At: m.At, Kind: m.Kind, Event: m.Event, ByName: m.By.Name, ByRole: string(m.By.Role), Text: m.Text, Ref: m.Ref, To: m.To}
	for _, o := range m.Options {
		c.Choices = append(c.Choices, o.Label)
	}
	rc.s.sessionRecords.Chat(rc.in, c)
}

// recordCurrent is the journal's Current: the session of the instance as
// the registry lists it now, ended only once its end is observed
// (observedEnd); none once the registry lists another session under its id
// or none.
func (s *Server) recordCurrent(in journal.Instance) (journal.Meta, bool) {
	d, ok := s.registry.Get(in.ID())
	if !ok {
		return journal.Meta{}, false
	}
	v, ok := s.recorders.Load(d)
	if !ok || v.(*recorder).in != in {
		return journal.Meta{}, false
	}
	rc := v.(*recorder)
	info := d.Info()
	m := journal.Meta{Name: info.Name, AgentSession: agentSessionOf(info.AgentSession), Crew: crewOfRef(info.Crew)}
	if s.observedEnd(rc, d, info) {
		m.Ended, m.Ending = true, endingOf(info)
		if info.EndedAt != nil {
			m.EndedAt = *info.EndedAt
		}
	}
	return m, true
}

// observedEnd says the session's end is one the server observed: its
// status ended and, for a server session, its process gone (ProcessDone).
// A server session's status reads stopped once a stop was asked, whatever
// the process did (Local.Stop marks it so when the process's stop failed
// too): while that process runs its record is not ended, and its end is
// recorded once the process is gone (awaitProcessEnd), or never, the
// record then left open for the next start to mark lost.
func (s *Server) observedEnd(rc *recorder, d session.Driver, info session.Info) bool {
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
	s.awaitProcessEnd(rc, l)
	return false
}

// awaitProcessEnd records the end of a server session whose status ended
// before its process did, once the process is gone; once a session, and
// never after the journal's close begins (recordsStop).
func (s *Server) awaitProcessEnd(rc *recorder, l *session.Local) {
	if !rc.waiting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		select {
		case <-l.ProcessDone():
		case <-s.recordsStop:
			return
		}
		j := s.sessionRecords
		j.End(rc.in, journal.Ending{How: endingOf(l.Info())})
		j.Changed(rc.in)
	}()
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

// recordRemoved is the registry's OnRemove for the journal: what the
// session was as it left is handed over for the metadata still to be read
// (Gone), then a session that left with an end observed keeps it (the
// Info's, when the journal has none yet), a hosted one let go without one
// (its host away past the grace) is marked host away, which proves nothing
// of its process, and a server session whose process still ran is marked
// lost until it is seen gone (awaitProcessEnd). Its hooks still to come
// keep its recorder, and its record's instance judges them.
func (s *Server) recordRemoved(id string, d session.Driver) {
	v, ok := s.recorders.LoadAndDelete(d)
	if !ok || s.sessionRecords == nil {
		return
	}
	rc := v.(*recorder)
	j := s.sessionRecords
	info := d.Info()
	j.Gone(rc.in, journal.Meta{Name: info.Name, AgentSession: agentSessionOf(info.AgentSession), Crew: crewOfRef(info.Crew)})
	switch {
	case s.observedEnd(rc, d, info):
		end := journal.Ending{How: endingOf(info)}
		if info.EndedAt != nil {
			end.At = *info.EndedAt
		}
		j.End(rc.in, end)
	case info.Kind == session.KindHosted:
		j.End(rc.in, journal.Ending{How: journal.EndingHostAway, Lost: true})
	default:
		j.End(rc.in, journal.Ending{How: journal.EndingLost, Lost: true})
	}
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

// handleForgetHistorySessions answers DELETE /api/history/sessions: every
// record neither live nor pending forgotten, those kept counted.
func (s *Server) handleForgetHistorySessions(w http.ResponseWriter, r *http.Request) {
	if s.historyUnavailable(w) {
		return
	}
	forgotten, kept, err := s.sessionRecords.ForgetAll()
	switch {
	case errors.Is(err, journal.ErrNotOwner):
		writeError(w, http.StatusConflict, "history_read_only", "another server owns the session records")
		return
	case err != nil:
		s.log.Warn("the session records could not all be forgotten", "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the records could not all be removed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": forgotten, "kept": kept})
}
