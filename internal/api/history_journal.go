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
// it with its data directory (a switchyard keeps none) and feeds it from
// the places a session's evidence passes: a server session's launch
// (createLocalSession: Begin, with the launch revision read before its
// process starts), a hosted session's add (the hub's OnAdded), every
// session's changes (localChange and the hub's OnChange, through Changed
// and Current), its activity (a sink on the event hub), its chat (the
// hub's chat) and its removal (the registry's OnRemove).

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
	return j
}

// recording says a session that starts now is recorded.
func (s *Server) recording() bool {
	return s.sessionRecords != nil && s.cfg.HistorySessions()
}

// launchRevision is what a launch's folder is at before its process
// starts: read for a native session only (a Runtime elsewhere runs on a
// tree this server does not observe); a read that fails or times out
// leaves it empty.
func (s *Server) launchRevision(where ext.Where, cwd string) gitcli.Revision {
	if !s.recording() || where != ext.WhereNative {
		return gitcli.Revision{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, cwd)
	if err != nil {
		return gitcli.Revision{}
	}
	return rev
}

// beginServerRecord begins the record of a server session as it is made,
// before the session can keep an entry.
func (s *Server) beginServerRecord(info session.Info, launched session.Launched, agentAdapter, agentName string, version string, where ext.Where, rev gitcli.Revision) {
	if !s.recording() {
		return
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
	if err := s.sessionRecords.Begin(r); err != nil && !errors.Is(err, journal.ErrNotOwner) {
		s.log.Warn("the session's record was not begun", "session", info.ID, "err", err)
	}
}

// beginHostedRecord is the hub's OnAdded: the record of a hosted session,
// from what its host registered, borrowing nothing of this server's
// machine (no probe, no git read, no Runtime).
func (s *Server) beginHostedRecord(info session.Info, owner string) {
	if !s.recording() {
		return
	}
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
	if err := s.sessionRecords.Begin(r); err != nil && !errors.Is(err, journal.ErrNotOwner) {
		s.log.Warn("the hosted session's record was not begun", "session", info.ID, "err", err)
	}
}

// recordChanged marks a session's record stale: the journal reads the
// session as it is when it gets to it (recordCurrent).
func (s *Server) recordChanged(id string) {
	if j := s.sessionRecords; j != nil {
		j.Changed(id)
	}
}

// recordCurrent is the journal's Current: the session as the registry
// lists it now.
func (s *Server) recordCurrent(id string) (journal.Meta, bool) {
	d, ok := s.registry.Get(id)
	if !ok {
		return journal.Meta{}, false
	}
	info := d.Info()
	m := journal.Meta{Name: info.Name, AgentSession: agentSessionOf(info.AgentSession), Crew: crewOfRef(info.Crew)}
	if info.Status.Ended() {
		m.Ended, m.Ending = true, endingOf(info)
		if info.EndedAt != nil {
			m.EndedAt = *info.EndedAt
		}
	}
	return m, true
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
func (s *Server) recordEndCommit(r journal.Record) string {
	if r.Host.Hosted || r.Where != string(ext.WhereNative) || r.Repo == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), revisionTimeout)
	defer cancel()
	rev, err := s.readRevision(ctx, r.Cwd)
	if err != nil {
		return ""
	}
	return rev.Commit
}

// recordDenied is the journal's Denied: a server session's deny list, as
// /api/touched applies it; a session no longer listed keeps nothing.
func (s *Server) recordDenied(id, path string) bool {
	d, ok := s.registry.Get(id)
	if !ok {
		return true
	}
	switch drv := d.(type) {
	case *session.Local:
		return drv.Denied(path)
	case *signal.HostedSession:
		return false
	}
	return true
}

// recordActivity is the event hub's sink for the journal: every activity
// entry of every session, server and hosted alike, a file's path made
// absolute as the touched index keys it, and a status entry that is the
// session's end taken as its end. It never waits.
func (s *Server) recordActivity(id string, e session.ActivityEntry, _ session.AttentionState) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	a := journal.Activity{At: e.At, Type: e.Type, By: e.By, ByName: e.ByName, Message: e.Message, URL: e.URL, To: e.To, Tool: e.Tool, Op: e.Op, Path: e.Path}
	if e.Type == session.ActivityFile {
		d, ok := s.registry.Get(id)
		if !ok {
			return
		}
		switch d.(type) {
		case *session.Local:
			a.Path = session.TouchedAbs(d.Info().Cwd, e.Path)
		case *signal.HostedSession:
			a.Path = signal.HostAbs(d.Info().Cwd, e.Path)
		default:
			return
		}
	}
	j.Note(id, a)
	if e.Type == session.ActivityStatus && isEnding(e.Message) {
		j.End(id, journal.Ending{At: e.At, How: e.Message})
	}
}

// recordChat is the hub's chat for the journal: a session's own chat
// lines, never a run's.
func (s *Server) recordChat(id string, m session.ChatMessage) {
	j := s.sessionRecords
	if j == nil || id == "" || (m.Scope != "" && m.Scope != proto.ChatScopeSession) {
		return
	}
	c := journal.ChatLine{ID: m.ID, At: m.At, Kind: m.Kind, Event: m.Event, ByName: m.By.Name, ByRole: string(m.By.Role), Text: m.Text, Ref: m.Ref, To: m.To}
	for _, o := range m.Options {
		c.Choices = append(c.Choices, o.Label)
	}
	j.Chat(id, c)
}

// recordRemoved is the registry's OnRemove for the journal: a session
// that left with an end keeps it (the Info's, when the journal has none
// yet), a hosted one let go without one (its host away past the grace) is
// marked host away, which proves nothing of its process.
func (s *Server) recordRemoved(id string, d session.Driver) {
	j := s.sessionRecords
	if j == nil {
		return
	}
	info := d.Info()
	switch {
	case info.Status.Ended():
		end := journal.Ending{How: endingOf(info)}
		if info.EndedAt != nil {
			end.At = *info.EndedAt
		}
		j.End(id, end)
	case info.Kind == session.KindHosted:
		j.End(id, journal.Ending{How: journal.EndingHostAway, Lost: true})
	default:
		j.End(id, journal.Ending{How: journal.EndingLost, Lost: true})
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
