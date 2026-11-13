package api

import (
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A comparison's doors (docs/plans/compare-crew.md, Isolation): what a
// session of a comparison's lane (a candidate's, or its review's) may call
// with its agent token, and what each route it may call gives it. The lane
// of a session, a member and a task is the engine's (crew.Engine.LaneOf);
// no door here makes one of names or of the crew's shape.

// doorRule is what a route gives a session of a comparison's lane.
type doorRule uint8

const (
	// doorRefused: the route takes no lane's token; it answers it as a
	// token it does not take (its 401). A route with no entry is so.
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
	// view link, a link for the person.
	"POST /api/sessions/{id}/events":      doorOwn,
	"POST /api/sessions/{id}/attention":   doorOwn,
	"GET /api/sessions/{id}/guide":        doorOwn,
	"POST /api/sessions/{id}/links/agent": doorOwn,
	"POST /api/sessions/{id}/open":        doorOwn,
	// Its attempt: its task file, its result, its blocker, its
	// checkpoint.
	"GET /api/sessions/{id}/task":             doorOwn,
	"POST /api/sessions/{id}/task/result":     doorOwn,
	"POST /api/sessions/{id}/task/blocked":    doorOwn,
	"POST /api/sessions/{id}/task/checkpoint": doorOwn,
	// Its run: messages within the lane (the engine refuses one to
	// another lane, 409 lane), the lane's run status, its ticket without
	// the other lanes and no comment.
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

// laneDoorOpen reports whether the route r was routed by is open to the
// session with the id: every route for a session of no comparison's lane,
// and for one of a lane only a route compareDoors does not refuse.
func (s *Server) laneDoorOpen(r *http.Request, sessionID string) bool {
	if s.runs == nil {
		return true
	}
	if _, inLane := s.runs.LaneOf(sessionID); !inLane {
		return true
	}
	return compareDoors[r.Pattern] != doorRefused
}

// laneRunView is what run_status answers a session of a comparison's
// lane: the run's id, name, goal, state and outcome, its own lane's
// members and tasks with their latest attempts, and the compare block
// with its own candidate's entry alone. Nothing else of the run is in it:
// no other lane's member or task, no log, no chat.
type laneRunView struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Goal    string             `json:"goal"`
	State   string             `json:"state"`
	Outcome string             `json:"outcome,omitempty"`
	Members []crew.MemberState `json:"members"`
	Tasks   []crew.TaskState   `json:"tasks"`
	Compare crew.Compare       `json:"compare"`
}

// laneRun is the run as the lane l sees it (laneRunView).
func laneRun(run crew.Run, l crew.Lane) laneRunView {
	v := laneRunView{ID: run.ID, Name: run.Name, Goal: run.Goal, State: run.State, Outcome: run.Outcome,
		Members: []crew.MemberState{}, Tasks: []crew.TaskState{}, Compare: l.Compare()}
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

// laneTicket is the ticket v as the lane l reads it: without the comments
// another lane's member of l's run made (the author agent:<member>,
// whichever run made it, as a comment names no run), and without the
// links that name another lane's task as their candidate or its branch,
// with those links' reads.
func laneTicket(v dispatch.TicketView, l crew.Lane) dispatch.TicketView {
	v.Comments = slices.DeleteFunc(slices.Clone(v.Comments), func(c dispatch.Comment) bool {
		member, agent := strings.CutPrefix(c.Author, "agent:")
		return agent && l.OtherMember(member)
	})
	tasks := l.OtherTasks()
	branches := branchesOf(tasks)
	reads := maps.Clone(v.LinkReads)
	v.Links = slices.DeleteFunc(slices.Clone(v.Links), func(k dispatch.Link) bool {
		c, err := dispatch.ParseCandidate(k.Candidate)
		hide := err == nil && slices.Contains(tasks, c.Task)
		for _, s := range []string{k.Ref, k.URL, k.Note} {
			if plain, err := url.PathUnescape(s); err == nil {
				s += " " + plain
			}
			hide = hide || (branches != nil && branches.MatchString(s))
		}
		if hide {
			delete(reads, k.ID)
		}
		return hide
	})
	v.LinkReads = reads
	return v
}

// branchesOf matches the attempt branch of any of the tasks
// (crew/<run>/<task>.<n>), nil for none.
func branchesOf(tasks []string) *regexp.Regexp {
	if len(tasks) == 0 {
		return nil
	}
	quoted := make([]string, len(tasks))
	for i, t := range tasks {
		quoted[i] = regexp.QuoteMeta(t)
	}
	return regexp.MustCompile(`crew/[^/\s]+/(?:` + strings.Join(quoted, "|") + `)\.[0-9]`)
}

// laneComment answers 409 lane for a session of a comparison's lane, whose
// ticket takes no comment from it before a selection, before anything is
// read or written, and reports whether it did.
func (s *Server) laneComment(w http.ResponseWriter, d session.Driver) bool {
	if _, inLane := s.runs.LaneOf(d.Info().ID); !inLane {
		return false
	}
	writeError(w, http.StatusConflict, "lane", "a candidate of a comparison does not comment on its ticket before the selection: say it in your result's summary")
	return true
}
