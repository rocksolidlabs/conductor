package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// An attempt's ticket (docs/plans/dispatch.md, PR-12): the session of a
// run's member reads the Dispatch ticket its run is linked to and
// comments on it, with its own token (CONDUCTOR_NOTIFY_TOKEN, sent over
// HTTP: the address-based path every task tool takes) or the admin's,
// through taskSession; conductor task ticket and the MCP tools
// ticket_read and ticket_comment are their clients. A run belongs to one
// live ticket at most (run_linked on a local write, Reconcile after a
// merge), so the routes have one target: the live ticket whose live run
// link names the session's run. A deleted ticket's link holds no run (a
// reference to a tombstone is inactive), and with no live ticket the
// routes answer 404 no_ticket. An agent reads and comments, nothing more
// (Owner decisions, 13): its comment is a comment.add under the actor
// agent:<member>, the session's member whichever token asked, with
// receipts of its own apart from the admin's and no display name, so it
// reads as the member's and never as a person's.

// agentActor is the actor a member's operations are kept under:
// agent:<member>, at most 46 bytes (a member's name is at most 40
// characters of [a-z0-9._-]), within dispatch.MaxActor.
func agentActor(member string) string { return "agent:" + member }

// ticketReads bounds how often a session's ticket is looked for: its
// lookup (RunTicket) and its read (Ticket) are two read transactions, and
// a link removed or moved, or the ticket deleted, between them sends the
// lookup round again; past this the route answers that no ticket holds
// the run, which a link that moves on every look nearly is.
const ticketReads = 3

// betweenTicketReads, when set, runs between a session's ticket's lookup
// and its read: a seam for a test, which sets it outside the package's
// parallel tests, to move the link or delete the ticket in the gap.
var betweenTicketReads atomic.Pointer[func()]

// sessionCommentBody is a session's comment: the operation's id, which
// the client mints, and the comment's body, Markdown of 1 to
// dispatch.MaxCommentBody bytes. It names no revision and no display
// name: the comment is the member's.
type sessionCommentBody struct {
	OpID string `json:"opId"`
	Body string `json:"body"`
}

// sessionTicketReply is what a session reads of its run's ticket: the
// ticket as GET /api/dispatch/tickets/{key} reads it, beside the rows of
// its live labels, by name, and its live milestone's row (nil for none),
// which the project's routes give the admin alone.
type sessionTicketReply struct {
	Ticket    dispatch.TicketView `json:"ticket"`
	Labels    []dispatch.Label    `json:"labels"`
	Milestone *dispatch.Milestone `json:"milestone"`
}

// sessionRun is the run and the member of the session with the id: ok is
// false, and the route has answered 404 no_ticket, for a session that is
// no member of a run the engine holds.
func (s *Server) sessionRun(w http.ResponseWriter, sessionID string) (run, member string, ok bool) {
	run, member, ok = s.runs.MemberOf(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "no_ticket", "this session is no member of a run, so no ticket is linked to it")
	}
	return run, member, ok
}

// noTicket answers 404 no_ticket for a run no live ticket's live run link
// names.
func noTicket(w http.ResponseWriter, run string) {
	writeError(w, http.StatusNotFound, "no_ticket", "no ticket is linked to this session's run "+run)
}

// linkedTicket reads, whole (dispatch.TicketView), the live ticket whose
// live run link names the run: found is false when there is none, a
// deleted ticket's link holding none. The ticket answered is live and
// holds the run as its own read shows it: a read that shows it deleted or
// no longer holding the run sends the lookup round again, at most
// ticketReads times.
func linkedTicket(ctx context.Context, ds *dispatch.Store, run string) (dispatch.TicketView, bool, error) {
	for range ticketReads {
		tk, found, err := ds.RunTicket(ctx, run)
		if err != nil || !found || !tk.DeletedAt.IsZero() {
			return dispatch.TicketView{}, false, err
		}
		if f := betweenTicketReads.Load(); f != nil {
			(*f)()
		}
		v, err := ds.Ticket(ctx, tk.Key)
		if err != nil {
			return dispatch.TicketView{}, false, err
		}
		if v.DeletedAt.IsZero() && slices.ContainsFunc(v.Links, func(l dispatch.Link) bool { return l.Kind == dispatch.LinkRun && l.Ref == run }) {
			return v, true, nil
		}
	}
	return dispatch.TicketView{}, false, nil
}

// handleSessionTicket answers the ticket the session's run is linked to:
// 200 {ticket, labels, milestone}, the ticket in full as the admin's read
// gives it (its last 50 comments among it), labels the rows of its live
// labels by name and milestone its live milestone's row or null; 404
// no_ticket for a session in no run or a run no live ticket holds; 400
// invalid_request for any parameter. The reply is bounded as the ticket's
// own read is, under MaxTicketReply, which dispatch_test.go measures
// through this route.
func (s *Server) handleSessionTicket(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	ds, ok := s.dispatchStore(w)
	if !ok || !noQuery(w, r) {
		return
	}
	run, _, ok := s.sessionRun(w, d.Info().ID)
	if !ok {
		return
	}
	v, found, err := linkedTicket(r.Context(), ds, run)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	if !found {
		noTicket(w, run)
		return
	}
	project, _, _ := dispatch.ParseKey(v.Key)
	pv, err := ds.Project(r.Context(), project)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	out := sessionTicketReply{Ticket: v, Labels: []dispatch.Label{}}
	for _, l := range pv.Labels {
		if slices.Contains(v.Labels, l.ID) {
			out.Labels = append(out.Labels, l)
		}
	}
	for _, m := range pv.Milestones {
		if m.ID == v.MilestoneID {
			out.Milestone = &m
		}
	}
	writeDispatch(w, http.StatusOK, out)
}

// beforeSessionComment, when set, runs once a session's comment has been
// read and its run found, before the store's operation: a seam for a
// test, which sets it outside the package's parallel tests, to hold a
// request there while another with the same opId commits and the run's
// link moves.
var beforeSessionComment atomic.Pointer[func()]

// handleSessionTicketComment comments on the ticket the session's run is
// linked to (comment.add): body {opId, body}, the body Markdown of 1 to
// 16 KiB; the author agent:<member>, the session's member, and the
// operation kept under that actor, so the same opId sent again with the
// same body answers its receipt and changes nothing; 201 {receipt,
// comment}, the comment with its number, as the admin's comment route
// answers; 404 no_ticket; 400 invalid_request for a body that is not
// {opId, body} or an opId that is not one, 413 bound past 64 KiB; the
// store's refusals as the admin's route answers them (409 invalid, bound
// or archived). The ticket is found inside the comment's own transaction
// (dispatch.Store.AddRunComment), after the operation's receipt is looked
// up: a resend, or a request that overlaps the one it repeats, answers
// that operation's receipt whatever the run's link has done since (a
// repeat on the ticket it went to, or op_conflict for other content, a
// refusal's as well), and a new operation goes to the ticket that holds
// the run when it commits.
func (s *Server) handleSessionTicketComment(w http.ResponseWriter, r *http.Request) {
	d, ok := s.taskSession(w, r, false)
	if !ok {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body sessionCommentBody
	if err := decodeJSONLimit(w, r, &body, maxBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !dispatch.OpIDPattern.MatchString(body.OpID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
		return
	}
	run, member, ok := s.sessionRun(w, d.Info().ID)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	if f := beforeSessionComment.Load(); f != nil {
		(*f)()
	}
	op := dispatch.Op{Actor: agentActor(member), ID: body.OpID}
	rc, err := ds.AddRunComment(ctx, op, run, body.Body)
	if errors.Is(err, dispatch.ErrNoRunTicket) {
		noTicket(w, run)
		return
	}
	s.writeOp(w, "comment", rc, err, true, "comment", runCommentAfter(ctx, ds, op))
}

// runCommentAfter is a session's comment as its reply carries it: the
// ticket it went to read from the operation's log (dispatch.Store.OpTicket),
// then the comment (commentAfter). Both are reads after the commit, so a
// failure of either, the operation's deadline spent among them, answers
// the receipt with a null comment (writeOp) and never an error a client
// would send again under a new opId.
func runCommentAfter(ctx context.Context, ds *dispatch.Store, op dispatch.Op) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		if rc.ID == "" {
			return nil, nil
		}
		key, found, err := ds.OpTicket(ctx, op.Actor, op.ID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("dispatch: operation %s is done and its log names no ticket", op.ID)
		}
		return commentAfter(ctx, ds, key, "")(rc)
	}
}
