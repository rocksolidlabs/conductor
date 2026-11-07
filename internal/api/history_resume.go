package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Resume from a record (docs/plans/history.md, "Resume from a record",
// PR-5): POST /api/history/sessions/{id}/resume takes up the agent's own
// conversation of a session from its record, after a restart as much as
// while the session is still listed, as an operation with a durable
// receipt in the record (journal.Resume). Its order:
//
//  1. under the journal's lock for the record (journal.ReserveResume), the
//     command id looked up: its receipt answers at once, whatever happened
//     to its launch (answerResumeReceipt); else, the record at its
//     journal.MaxResumes receipts, 409 resume_limit with nothing reserved;
//     else a receipt reserved pending, with the new session's id allocated
//     now, and written before anything else;
//  2. the record judged by the resume routes' rules and its own
//     (recordResume, the judgement the history's offer says too), then
//     the launch's variables (env_not_kept unless withoutEnv) and a lost
//     record (lost_unconfirmed unless confirmLost): a refusal settles the
//     receipt failed with its code;
//  3. the session launched as the session route launches one
//     (resumeLaunch), with the allocated id and the record's Runtime: a
//     listed session from its own Launched, a record from its launch;
//  4. the receipt settled started with the new session's id: 201.
//
// A stop between 1 and 4 leaves the receipt pending on disk; the next
// start's recovery settles it by what the data directory shows
// (journal.Recover). Nothing resumes a session on its own: only this
// route, which a person sends.

// maxHistoryResumeBody bounds the route's body: a command id and two
// flags.
const maxHistoryResumeBody = 1 << 10

// historyResumeBody is POST /api/history/sessions/{id}/resume's body: the
// command id (crew.CommandIDPattern), and the person's second yes to
// resume without the launch's variables (withoutEnv) or a session whose
// process's state is not known (confirmLost).
type historyResumeBody struct {
	CommandID   string `json:"commandId"`
	WithoutEnv  bool   `json:"withoutEnv,omitempty"`
	ConfirmLost bool   `json:"confirmLost,omitempty"`
}

// recordPlan is what a resume from a record takes up (recordResume): the
// resume routes' plan (the agent, its conversation, the folder), and of
// the record, whether it is lost (its process's state not known), the
// session the registry still lists for it, ended, and, for a refusal, the
// run whose page resumes it instead (run) or that a check could not be
// made now (unknown).
type recordPlan struct {
	resumePlan
	lost    bool
	listed  *session.Local
	run     string
	unknown bool
	// offer is the offer's words for a refusal whose route's words say
	// more (the member route a crew member's names).
	offer string
}

// recordResume judges a resume from the record rec by the resume routes'
// rules (resumeCheck) and the record's own, in this order: never a hosted
// session (400 hosted_session: its machine resumes it); never a Brakeman
// turn (409 brakeman_run), a managed attempt (409 managed_run: its run
// continues) or a crew member (409 crew_member: its run's member route
// resumes it), a member known by the record's crew, the engine's runs or
// a run record naming the session (memberOf; 503 member_unknown when the
// run records cannot all be read); never a launch whose arguments were
// not kept (409 args_not_kept); only in a Runtime this server has (409
// runtime_gone); never a session still running, nor one ended whose
// process is not (409 still_running: a stop that failed); never from a
// record's folder that may be cut (409 cwd_not_kept,
// journal.CwdMayBeCut); then resumeCheck's (400 invalid_agent, 409
// already_resumed, 400 invalid_cwd);
// and only a conversation there is to take up (409 not_resumable: no
// session recipe, or no turn yet). It is the record route's judgement and
// the history's offer (resumeOffered), so the two never differ.
func (s *Server) recordResume(rec journal.Record) (recordPlan, *apiError) {
	var p recordPlan
	if rec.Host.Hosted {
		return p, newAPIError(http.StatusBadRequest, "hosted_session", fmt.Sprintf("resume it on %s: `conductor host` resumes it there", cmpOr(rec.Host.Name, "its host")))
	}
	run, name, member, err := s.memberOf(rec)
	if err != nil {
		p.unknown = true
		return p, newAPIError(http.StatusServiceUnavailable, "member_unknown", "whether it was a crew member's session could not be read; try again")
	}
	if member {
		switch {
		case rec.Crew != nil && rec.Crew.Brakeman != "":
			return p, newAPIError(http.StatusConflict, "brakeman_run", "a Brakeman turn is never resumed: ask Brakeman again")
		case rec.Crew != nil && rec.Crew.Task != "":
			p.run = run
			return p, newAPIError(http.StatusConflict, "managed_run", "a managed run continues from its run page")
		}
		p.run, p.offer = run, "resume it as the run's member, from its run"
		return p, newAPIError(http.StatusConflict, "crew_member", fmt.Sprintf("%s: POST /api/runs/%s/members/%s/resume", p.offer, run, cmpOr(name, "{name}")))
	}
	if rec.Launch == nil || !rec.Launch.ArgsKept {
		return p, newAPIError(http.StatusConflict, "args_not_kept", "its arguments were too long to keep")
	}
	if rec.Where != "" {
		if _, ok := s.runtimeAt(ext.Where(rec.Where)); !ok {
			return p, newAPIError(http.StatusConflict, "runtime_gone", fmt.Sprintf("its Runtime %s is not on this server", rec.Where))
		}
	}
	var as *session.AgentSession
	if rec.Agent.Session != nil {
		as = &session.AgentSession{ID: rec.Agent.Session.ID, Source: rec.Agent.Session.Source, Resumable: rec.Agent.Session.Resumable}
	}
	agentID, cwd := rec.Agent.ID, rec.Cwd
	if d, ok := s.registry.Get(rec.ID); ok {
		info := d.Info()
		if !info.Status.Ended() {
			return p, newAPIError(http.StatusConflict, "still_running", "the session is still running")
		}
		if l, ok := d.(*session.Local); ok {
			// Ended is not gone: a stop that failed marks the session
			// stopped with its process still running, which would then
			// hold the conversation beside the resumed one.
			select {
			case <-l.ProcessDone():
			default:
				return p, newAPIError(http.StatusConflict, "still_running", "the session's process is still running: its stop did not end it; stop it again")
			}
			// Listed: what it was launched with, and its conversation as
			// the session knows it now.
			p.listed = l
			launched := l.Launched()
			agentID, cwd, as = launched.AgentID, launched.Cwd, info.AgentSession
		}
	}
	if p.listed == nil && journal.CwdMayBeCut(cwd) {
		// The record's folder may be a cut of the session's, its parent
		// or another: never one to start the conversation in.
		return p, newAPIError(http.StatusConflict, "cwd_not_kept", fmt.Sprintf("its working directory was too long for its record to keep whole (its bound is %d bytes): start a session there anew", journal.MaxCwd))
	}
	plan, aerr := s.resumeCheck(agentID, as, cwd)
	p.resumePlan = plan
	if aerr != nil {
		return p, aerr
	}
	if plan.id == "" {
		if plan.agent.Session.Empty() {
			return p, newAPIError(http.StatusConflict, "not_resumable", plan.agent.Name+" has no session recipe: nothing of its conversation can be resumed")
		}
		return p, newAPIError(http.StatusConflict, "not_resumable", "nothing to resume yet: the agent had no turn")
	}
	p.lost = rec.Ending == journal.EndingLost || (!rec.LostAt.IsZero() && rec.EndedAt.IsZero())
	return p, nil
}

// lostWords is what a lost record's resume asks the person to read before
// their second yes.
func lostWords(rec journal.Record) string {
	return fmt.Sprintf("the server stopped while this session ran on %s; its process may still be running", cmpOr(rec.Where, string(ext.WhereNative)))
}

// envNamesOf is the variables a launch had, as its record names them, and
// how many it had in all.
func envNamesOf(l *journal.Launch) ([]string, int) {
	if l == nil {
		return nil, 0
	}
	n := max(l.EnvCount, len(l.EnvNames))
	if n == 0 && l.EnvSHA256 != "" {
		n = 1
	}
	return l.EnvNames, n
}

// envWords names the variables a launch had: their names, and how many
// more past those its record names.
func envWords(names []string, n int) string {
	switch {
	case len(names) == 0:
		return fmt.Sprintf("%d variables", n)
	case n > len(names):
		return fmt.Sprintf("%s and %d more", strings.Join(names, ", "), n-len(names))
	}
	return strings.Join(names, ", ")
}

// resumeStops runs the test's seam at stage: true says the server stops
// there (resumeHook).
func (s *Server) resumeStops(stage, id string) bool {
	if h := s.resumeHook.Load(); h != nil {
		return (*h)(stage, id)
	}
	return false
}

// handleResumeHistorySession answers POST /api/history/sessions/{id}/resume:
// 201 {session, resumed, notice?, receipt} for a session started; the
// command id's receipt again for one sent before (answerResumeReceipt); a
// refusal's code with {error, receipt}, its receipt settled failed.
func (s *Server) handleResumeHistorySession(w http.ResponseWriter, r *http.Request) {
	var body historyResumeBody
	if err := decodeJSONLimit(w, r, &body, maxHistoryResumeBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.historyUnavailable(w) {
		return
	}
	if !crew.CommandIDPattern.MatchString(body.CommandID) {
		writeError(w, http.StatusBadRequest, "invalid_request", crew.ErrBadCommandID.Error())
		return
	}
	id := r.PathValue("id")
	rec, receipt, fresh, err := s.sessionRecords.ReserveResume(id, journal.Resume{CommandID: body.CommandID, Actor: actorOf(r), Session: session.NewID()})
	switch {
	case errors.Is(err, journal.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such record")
		return
	case errors.Is(err, journal.ErrResumeLimit):
		writeError(w, http.StatusConflict, "resume_limit", fmt.Sprintf("this record has been resumed %d times; resume from the newest session's own record", journal.MaxResumes))
		return
	case errors.Is(err, journal.ErrNotOwner):
		writeError(w, http.StatusConflict, "history_read_only", "another server owns the session records")
		return
	case errors.Is(err, journal.ErrNewer):
		writeError(w, http.StatusConflict, "record_newer", "the record is of a later version of Conductor, which resumes from it")
		return
	case errors.Is(err, journal.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "history_closed", "the server is stopping")
		return
	case err != nil:
		s.log.Warn("a resume's receipt could not be written", "record", id, "err", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "the record could not be written")
		return
	}
	if !fresh {
		s.answerResumeReceipt(w, receipt)
		return
	}
	if s.resumeStops("reserved", id) {
		writeError(w, http.StatusServiceUnavailable, "history_closed", "the server is stopping")
		return
	}
	out, aerr := s.resumeFromRecord(context.WithoutCancel(r.Context()), rec, body, receipt.Session)
	if aerr != nil {
		receipt.State, receipt.Code, receipt.Error = journal.ResumeFailed, aerr.Code, aerr.Message
		// The receipt as the record keeps it, its words cut to their
		// bound: the same id sent again answers the same.
		receipt = s.settleResume(id, receipt)
		writeJSON(w, aerr.status, map[string]any{"error": apiError{Code: receipt.Code, Message: receipt.Error}, "receipt": receipt})
		return
	}
	if s.resumeStops("launched", id) {
		writeError(w, http.StatusServiceUnavailable, "history_closed", "the server is stopping")
		return
	}
	receipt.State = journal.ResumeStarted
	out["receipt"] = s.settleResume(id, receipt)
	writeJSON(w, http.StatusCreated, out)
}

// resumeFromRecord is steps 2 and 3 of a resume from rec: the judgement,
// the variables and a lost record's second yes, then the launch under the
// allocated id. It answers the reply's fields, or the refusal.
func (s *Server) resumeFromRecord(ctx context.Context, rec journal.Record, body historyResumeBody, allocated string) (map[string]any, *apiError) {
	p, aerr := s.recordResume(rec)
	if aerr != nil {
		return nil, aerr
	}
	sp := resumeSpec{agentID: rec.Agent.ID, name: rec.Name, cwd: p.cwd, where: ext.Where(rec.Where), resume: p.id, from: rec.ID, id: allocated}
	notice := ""
	if p.listed != nil {
		l := p.listed.Launched()
		sp.agentID, sp.name, sp.args, sp.env, sp.yolo, sp.renamed = l.AgentID, l.Name, l.Args, l.Env, l.Yolo, l.Renamed
	} else {
		sp.args, sp.yolo, sp.renamed = slices.Clone(rec.Launch.Args), rec.Launch.Yolo, rec.Launch.Renamed
		if names, n := envNamesOf(rec.Launch); n > 0 {
			if !body.WithoutEnv {
				return nil, newAPIError(http.StatusConflict, "env_not_kept", "its launch had variables, whose values no record keeps; resume without them (withoutEnv): "+envWords(names, n))
			}
			notice = "resumed without its launch's variables: " + envWords(names, n)
		}
	}
	if p.lost && !body.ConfirmLost {
		return nil, newAPIError(http.StatusConflict, "lost_unconfirmed", lostWords(rec)+"; resume it all the same (confirmLost) once you have looked")
	}
	next, aerr := s.resumeLaunch(ctx, sp)
	if aerr != nil {
		return nil, aerr
	}
	out := resumeReply(next.Info(), true, p.agent)
	if notice != "" {
		out["notice"] = notice
	}
	return out, nil
}

// settleResume settles the receipt in the record id as it says and
// answers it as the journal stored it (journal.SettleResume), every field
// cut to its bound, which is what the same command id answers again: a
// write that fails is logged, the receipt settled in memory for the
// journal's writer, or, the journal closed, left pending on disk for the
// next start's recovery.
func (s *Server) settleResume(id string, receipt journal.Resume) journal.Resume {
	stored, err := s.sessionRecords.SettleResume(id, receipt.CommandID, receipt)
	if err != nil {
		s.log.Warn("a resume's receipt was not written as it settled", "record", id, "state", receipt.State, "err", err)
	}
	return stored
}

// answerResumeReceipt answers a command id sent again with its receipt,
// starting nothing: started, 200 {receipt, session?} (the session while
// the registry lists it); failed, its refusal's code (resumeRefusal) with
// {error, receipt}; uncertain, 409 resume_uncertain with its words;
// pending, 409 resume_pending.
func (s *Server) answerResumeReceipt(w http.ResponseWriter, receipt journal.Resume) {
	switch receipt.State {
	case journal.ResumeStarted:
		out := map[string]any{"receipt": receipt}
		if d, ok := s.registry.Get(receipt.Session); ok && receipt.Session != "" {
			out["session"] = d.Info()
		}
		writeJSON(w, http.StatusOK, out)
		return
	case journal.ResumeFailed:
		writeJSON(w, resumeRefusal(receipt.Code), map[string]any{"error": apiError{Code: receipt.Code, Message: receipt.Error}, "receipt": receipt})
		return
	case journal.ResumeUncertain:
		writeJSON(w, http.StatusConflict, map[string]any{"error": apiError{Code: "resume_uncertain", Message: receipt.Error}, "receipt": receipt})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": apiError{Code: "resume_pending", Message: "this resume is being started; ask again"}, "receipt": receipt})
}

// resumeRefusal is the status a failed receipt's code is answered with
// again: the status its route first answered.
func resumeRefusal(code string) int {
	switch code {
	case "hosted_session", "invalid_agent", "invalid_cwd", "invalid_request", "args_not_allowed":
		return http.StatusBadRequest
	case "member_unknown":
		return http.StatusServiceUnavailable
	case "start_failed":
		return http.StatusBadGateway
	}
	return http.StatusConflict
}
