package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A comparison's doors (docs/plans/compare-crew.md, Isolation): what a
// session of a comparison's lane (a candidate's, or its review's) may call
// with its agent token, and what each route it may call gives it. Whether
// a session is a lane's is a fact of the session from its creation (the
// lane the engine marks it with as it launches it, session.CrewRef.Lane),
// read in one place (laneOf); the lane of a member and a task is the
// engine's (crew.Lane); no door here makes one of names or of the crew's
// shape.

// doorRule is what a route gives a session of a comparison's lane.
type doorRule uint8

const (
	// doorRefused: the route takes no lane's token; it answers it 409
	// lane (laneClosed). A route with no entry is so.
	doorRefused doorRule = iota
	// doorOwn: the route reads or acts on the session's own session,
	// task or attempt alone.
	doorOwn
	// doorLane: the route reaches the session's run, and keeps to its
	// lane (the engine's send, laneRun, laneTicket, laneComment).
	doorLane
)

// compareDoors is the rule of every route an agent's token may call, by
// its pattern as the mux has it (r.Pattern), for a session of a
// comparison's lane; any other session is not asked about. A route
// without an entry is refused, so a route a later change adds is closed to
// a comparison's lanes until its entry is written here.
var compareDoors = map[string]doorRule{
	// The session's own tools: report, set_state and ask, the guide, a
	// link for the person.
	"POST /api/sessions/{id}/events":    doorOwn,
	"POST /api/sessions/{id}/attention": doorOwn,
	"GET /api/sessions/{id}/guide":      doorOwn,
	"POST /api/sessions/{id}/open":      doorOwn,
	// A view link to the session: a lane's member mints none.
	"POST /api/sessions/{id}/links/agent": doorRefused,
	// Its attempt: its task file, its result, its blocker, its
	// checkpoint.
	"GET /api/sessions/{id}/task":             doorOwn,
	"POST /api/sessions/{id}/task/result":     doorOwn,
	"POST /api/sessions/{id}/task/blocked":    doorOwn,
	"POST /api/sessions/{id}/task/checkpoint": doorOwn,
	// Its run: messages within the lane (the engine refuses one to
	// another lane, 409 lane), the lane's run status, its ticket as it
	// stood at the launch and no comment.
	"POST /api/sessions/{id}/messages":        doorLane,
	"GET /api/sessions/{id}/inbox":            doorLane,
	"POST /api/sessions/{id}/inbox/ack":       doorLane,
	"GET /api/sessions/{id}/run":              doorLane,
	"GET /api/sessions/{id}/ticket":           doorLane,
	"POST /api/sessions/{id}/ticket/comments": doorLane,
	// A crew formed or grown by an agent: a run's member forms none.
	"POST /api/sessions/{id}/crew":        doorRefused,
	"POST /api/sessions/{id}/run/members": doorRefused,
	// DR1's decision tools: a candidate asks the people watching with
	// ask, which opens no record.
	"POST /api/sessions/{id}/decisions":                doorRefused,
	"GET /api/sessions/{id}/decisions":                 doorRefused,
	"GET /api/sessions/{id}/decisions/{did}":           doorRefused,
	"POST /api/sessions/{id}/decisions/{did}/withdraw": doorRefused,
	// Brakeman's: a turn's request and brief, its reads and its acts.
	"GET /api/sessions/{id}/brakeman/request":                          doorRefused,
	"POST /api/sessions/{id}/brakeman/brief":                           doorRefused,
	"GET /api/brakeman/changes":                                        doorRefused,
	"GET /api/brakeman/status":                                         doorRefused,
	"GET /api/brakeman/screen":                                         doorRefused,
	"GET /api/brakeman/overlaps":                                       doorRefused,
	"GET /api/brakeman/briefs":                                         doorRefused,
	"GET /api/brakeman/history":                                        doorRefused,
	"GET /api/runs":                                                    doorRefused,
	"GET /api/runs/{run}":                                              doorRefused,
	"GET /api/runs/{run}/tasks/{task}":                                 doorRefused,
	"GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps":              doorRefused,
	"GET /api/runs/{run}/tasks/{task}/attempts/{n}/steps/{id}/{k}/log": doorRefused,
	"GET /api/runs/{run}/commands/{command}":                           doorRefused,
	"GET /api/runs/{run}/messages":                                     doorRefused,
	"GET /api/runs/{run}/candidates":                                   doorRefused,
	"GET /api/sessions":                                                doorRefused,
	"GET /api/touched":                                                 doorRefused,
	"GET /api/dispatch/projects":                                       doorRefused,
	"GET /api/dispatch/tickets":                                        doorRefused,
	"GET /api/dispatch/tickets/{key}":                                  doorRefused,
	"GET /api/dispatch/tickets/{key}/history":                          doorRefused,
	"GET /api/dispatch/tickets/{key}/comments":                         doorRefused,
	"GET /api/dispatch/tickets/{key}/plans":                            doorRefused,
	"POST /api/dispatch/tickets/{key}/launch":                          doorRefused,
	"POST /api/dispatch/tickets/{key}/reviews":                         doorRefused,
	"POST /api/dispatch/tickets/{key}/plans":                           doorRefused,
	decisionRoutePattern:                                               doorRefused,
}

// laneFact is what a session is to a comparison's doors (laneOf).
type laneFact uint8

const (
	// noLane: a session of no run, or of a run without a compare block:
	// every route answers it as it answers any session.
	noLane laneFact = iota
	// inLane: a session of a comparison's lane the engine holds: the
	// lane's doors (compareDoors) and views.
	inLane
	// laneUnknown: a comparison's session whose lane cannot be told now
	// (the engine no longer holds its run, or cannot read its lanes): a
	// lane with no door open and no view.
	laneUnknown
)

// laneOf is what the session d is to a comparison's doors, read from its
// own crew reference, which the engine marks with the session's lane as
// it launches it, before the session can be reached; with the lane for
// inLane.
func (s *Server) laneOf(d session.Driver) (crew.Lane, laneFact) {
	ref := d.Info().Crew
	if ref == nil || ref.Lane == nil {
		return crew.Lane{}, noLane
	}
	if s.runs != nil {
		if l, ok := s.runs.LaneOf(*ref); ok {
			return l, inLane
		}
	}
	return crew.Lane{}, laneUnknown
}

// laneServes reports whether the route with the pattern serves a session
// that is st: every route a session of no lane; a lane's, a route
// compareDoors does not refuse; one whose lane cannot be told, none.
func laneServes(st laneFact, pattern string) bool {
	switch st {
	case noLane:
		return true
	case inLane:
		return compareDoors[pattern] != doorRefused
	}
	return false
}

// laneClosedWords is the one sentence every route closed to a
// comparison's lane answers its session with (409 lane).
const laneClosedWords = "a member of a comparison does not use this"

// laneClosed is the refusal of a route closed to a comparison's lane, or
// of a lane's view that cannot be built: 409 lane.
func laneClosed() *apiError { return newAPIError(http.StatusConflict, "lane", laneClosedWords) }

// writeLaneClosed answers laneClosed.
func writeLaneClosed(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "lane", laneClosedWords)
}

// laneRunView is what run_status answers a session of a comparison's
// lane: the run's id, name and goal, its own lane's members and tasks with
// their latest attempts, each with its own state, and the compare block
// with its own candidate's entry alone. Nothing else of the run is in it:
// no other lane's member or task, no state or outcome worked out over the
// whole run, no log, no chat.
type laneRunView struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Goal    string             `json:"goal"`
	Members []crew.MemberState `json:"members"`
	Tasks   []crew.TaskState   `json:"tasks"`
	Compare crew.Compare       `json:"compare"`
}

// laneRun is the run as the lane l sees it (laneRunView).
func laneRun(run crew.Run, l crew.Lane) laneRunView {
	v := laneRunView{ID: run.ID, Name: run.Name, Goal: run.Goal, Members: []crew.MemberState{}, Tasks: []crew.TaskState{}, Compare: l.Compare()}
	for _, m := range run.Members {
		if l.OwnMember(m.Name) {
			v.Members = append(v.Members, m)
		}
	}
	for _, t := range run.Tasks {
		if l.OwnTask(t.ID) {
			v.Tasks = append(v.Tasks, t)
		}
	}
	return v
}

// laneTicketReply is what ticket_read answers a session of a comparison's
// lane: the ticket as it stood at its run's launch, with people's
// comments since (laneTicketView), and the launch without what changed
// since.
type laneTicketReply struct {
	Ticket laneTicketView `json:"ticket"`
	Launch laneLaunch     `json:"launch"`
}

// laneLaunch is the launch a lane's run was made from: its id, the
// ticket's revision then, the plan revision and the package's digest.
type laneLaunch struct {
	ID     string            `json:"id"`
	Rev    int64             `json:"rev"`
	Plan   sessionLaunchPlan `json:"plan"`
	Digest string            `json:"digest"`
}

// laneTicketView is the ticket a lane reads: its identity (id, key,
// number) and the person's brief as the launch's package carries it
// (title, description, acceptance); the links with their reads and the
// attachments it had before the launch; the comments it had before the
// launch, and a person's since (the workbench's, or an identity
// provider's subject). Nothing else: no comment, link, read or attachment
// added since but a person's comment, no status, close state or count of
// what changed, no revision or time of the ticket's last change.
type laneTicketView struct {
	ID          string                              `json:"id"`
	Key         string                              `json:"key"`
	Number      int64                               `json:"number"`
	Title       string                              `json:"title"`
	Description string                              `json:"description"`
	Acceptance  string                              `json:"acceptance"`
	Links       []dispatch.Link                     `json:"links"`
	LinkReads   map[string]dispatch.LinkReadSummary `json:"linkReads"`
	Attachments []dispatch.Attachment               `json:"attachments"`
	Comments    []laneTicketComment                 `json:"comments"`
}

// laneTicketComment is a comment as a lane reads it: who wrote it, what
// and when; no number or revision.
type laneTicketComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	By        string    `json:"by"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	EditedAt  time.Time `json:"editedAt,omitzero"`
}

// laneTicket answers ticket_read for a session of the lane l
// (laneTicketReply), from the store's ticket and what it held at the
// run's launch (dispatch.AtLaunch); 409 lane when that cannot be told: a
// run no ticket launched, or a launch the store does not hold.
func (s *Server) laneTicket(w http.ResponseWriter, r *http.Request, ds *dispatch.Store, l crew.Lane) {
	rt := s.runPackage(l.Run)
	if rt == nil {
		writeLaneClosed(w)
		return
	}
	ctx := r.Context()
	v, found, err := packageTicket(ctx, ds, rt)
	if err == nil && !found {
		launchTicketGone(w, l.Run, rt.Key)
		return
	}
	var at dispatch.AtLaunch
	if err == nil {
		at, err = ds.AtLaunch(ctx, rt.Launch)
	}
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		writeLaneClosed(w)
		return
	case err != nil:
		s.dispatchError(w, "ticket", err)
		return
	}
	out := laneTicketView{ID: v.ID, Key: v.Key, Number: v.Number, Title: at.Ticket.Title, Description: at.Ticket.Description, Acceptance: at.Ticket.Acceptance,
		Links: []dispatch.Link{}, LinkReads: map[string]dispatch.LinkReadSummary{}, Attachments: []dispatch.Attachment{}, Comments: []laneTicketComment{}}
	for _, k := range v.Links {
		if at.Before[k.ID] && k.DeletedAt.IsZero() {
			out.Links = append(out.Links, k)
			if rd, ok := v.LinkReads[k.ID]; ok {
				out.LinkReads[k.ID] = rd
			}
		}
	}
	for _, a := range v.Attachments {
		if at.Before[a.ID] && a.DeletedAt.IsZero() {
			out.Attachments = append(out.Attachments, a)
		}
	}
	for _, c := range v.Comments {
		person := c.Author == adminActor || strings.HasPrefix(c.Author, ext.SubjectActor)
		if c.DeletedAt.IsZero() && (at.Before[c.ID] || person) {
			out.Comments = append(out.Comments, laneTicketComment{ID: c.ID, Author: c.Author, By: c.By, Body: c.Body, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt})
		}
	}
	writeDispatch(w, http.StatusOK, laneTicketReply{Ticket: out, Launch: laneLaunch{ID: rt.Launch, Rev: rt.Rev, Plan: sessionLaunchPlan{ID: rt.Plan, N: rt.PlanN}, Digest: rt.Digest}})
}

// laneComment answers 409 lane for a session of a comparison's lane, whose
// ticket takes no comment from it before a selection, before anything is
// read or written, and reports whether it did.
func (s *Server) laneComment(w http.ResponseWriter, d session.Driver) bool {
	if _, st := s.laneOf(d); st == noLane {
		return false
	}
	writeError(w, http.StatusConflict, "lane", "a candidate of a comparison does not comment on its ticket before the selection: say it in your result's summary")
	return true
}
