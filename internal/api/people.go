package api

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// The people Conductor knows (docs/plans/decisions.md, HM1): the union of
// Dispatch's directory and the people of the saved crews, read from both
// and written to neither (decision 21). Neither store bounds its names, so
// the reply has bounds of its own.
const (
	// defaultPeoplePage and maxPeoplePage bound the names of one page.
	defaultPeoplePage = 200
	maxPeoplePage     = 200
	// maxPersonCrews bounds the crews listed with one name; crewsMore says
	// there are more.
	maxPersonCrews = 20
	// maxPeopleQuery bounds q, in characters: a person's name's bound.
	maxPeopleQuery = crew.MaxPersonName
	// maxWorkCrews bounds the crews of a person's page, and maxWorkTasks
	// the tasks it lists; crewsMore and more say there are more.
	maxWorkCrews = 100
	maxWorkTasks = 200
)

// personCrew is a crew a person is in, with the responsibilities they
// hold there.
type personCrew struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Responsibilities []string `json:"responsibilities"`
}

// knownPerson is a name of GET /api/people: whether Dispatch's directory
// has it, and the crews that list it.
type knownPerson struct {
	Name      string       `json:"name"`
	Dispatch  bool         `json:"dispatch"`
	Crews     []personCrew `json:"crews"`
	CrewsMore bool         `json:"crewsMore,omitempty"`
}

// handlePeople answers GET /api/people?q=&after=&limit=: the names
// Conductor knows, each once, ordered by name as Dispatch orders its
// directory (byte for byte), in pages of at most limit (1 to 200, 200 when
// absent): {people, next?, dispatchUnavailable?}. next is the page's last
// name when more follow, which the next page passes as after; q (at most
// 60 characters) keeps the names that contain it, ignoring case. Each
// person's crews are at most 20, with crewsMore past them. A Dispatch that
// is off or cannot be read leaves its names out and says so
// (dispatchUnavailable); the crews' people are listed all the same.
func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	q, err := readQuery(r, "q", "after", "limit")
	var query, after string
	var limit int
	if err == nil {
		query, err = q.one("q")
	}
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	switch {
	case err != nil:
	case utf8.RuneCountInString(query) > maxPeopleQuery:
		err = fmt.Errorf("q is at most %d characters", maxPeopleQuery)
	case utf8.RuneCountInString(after) > crew.MaxPersonName:
		err = fmt.Errorf("after is a name of at most %d characters", crew.MaxPersonName)
	case limit < 0 || limit > maxPeoplePage || (limit == 0 && len(q["limit"]) > 0):
		// 0 is the count of an absent limit: one given is 1 to the page.
		err = fmt.Errorf("limit is 1 to %d", maxPeoplePage)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if limit == 0 {
		limit = defaultPeoplePage
	}
	people := map[string]*knownPerson{}
	know := func(name string) *knownPerson {
		p := people[name]
		if p == nil {
			p = &knownPerson{Name: name, Crews: []personCrew{}}
			people[name] = p
		}
		return p
	}
	unavailable := s.dispatch == nil
	if ds := s.dispatch; ds != nil {
		names, err := ds.People(r.Context())
		if err != nil {
			s.log.Warn("Dispatch's people could not be read for the people Conductor knows", "err", err)
			unavailable = true
		}
		for _, n := range names {
			know(n.Name).Dispatch = true
		}
	}
	if s.crews != nil {
		for offset := 0; ; offset += maxCrewPage {
			page, total, err := s.crews.List(offset, maxCrewPage)
			if err != nil {
				s.log.Error("crews list failed", "err", err)
				writeError(w, http.StatusInternalServerError, "store_failed", "could not read the crews")
				return
			}
			for _, c := range page {
				for _, p := range c.People {
					kp := know(p.Name)
					if len(kp.Crews) == maxPersonCrews {
						kp.CrewsMore = true
						continue
					}
					kp.Crews = append(kp.Crews, personCrew{ID: c.ID, Name: c.Name, Responsibilities: append([]string{}, p.Responsibilities...)})
				}
			}
			if offset+len(page) >= total || len(page) == 0 {
				break
			}
		}
	}
	lower := strings.ToLower(query)
	var names []string
	for name := range people {
		if name > after && (query == "" || strings.Contains(strings.ToLower(name), lower)) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := map[string]any{}
	if len(names) > limit {
		names = names[:limit]
		out["next"] = names[limit-1]
	}
	list := make([]knownPerson, 0, len(names))
	for _, name := range names {
		list = append(list, *people[name])
	}
	out["people"] = list
	if unavailable {
		out["dispatchUnavailable"] = true
	}
	writeDispatch(w, http.StatusOK, out)
}

// personTask is a task a person owns in a run (crew.TaskOwner, as the
// crew was launched): the run, its label, the task, its state, the member
// (the agent) that does it, and how the person came to own it.
type personTask struct {
	Run       string `json:"run"`
	RunLabel  string `json:"runLabel"`
	Task      string `json:"task"`
	State     string `json:"state"`
	Member    string `json:"member"`
	OwnerFrom string `json:"ownerFrom"`
}

// personTickets is what Dispatch holds for a person: how many live
// tickets they own and how many name them an assignee, each Dispatch's own
// list (GET /api/dispatch/tickets, owner= and assignee=person:), and that
// list's link, by which the person's page reads them.
type personTickets struct {
	Owner        int    `json:"owner"`
	Assignee     int    `json:"assignee"`
	OwnerHref    string `json:"ownerHref"`
	AssigneeHref string `json:"assigneeHref"`
}

// personWork is GET /api/people/{name}.
type personWork struct {
	Name      string         `json:"name"`
	Dispatch  bool           `json:"dispatch"`
	Crews     []personCrew   `json:"crews"`
	CrewsMore bool           `json:"crewsMore,omitempty"`
	Tasks     []personTask   `json:"tasks"`
	More      bool           `json:"more,omitempty"`
	Tickets   *personTickets `json:"tickets"`
	// Decisions are the decisions for the person: none until whom a
	// decision is for exists (docs/plans/decisions.md, HM1, PR-12).
	Decisions           []any `json:"decisions"`
	DispatchUnavailable bool  `json:"dispatchUnavailable,omitempty"`
}

// handlePerson answers GET /api/people/{name}: a person's work. The
// crews that list them (at most 100, crewsMore past them), the tasks they
// own in the runs the engine holds and the managed runs' records, as each
// run's crew was launched (at most 200, the newest run first and its tasks
// in the crew's order, more past them), and Dispatch's tickets they own or
// are assigned (counts and the list's links; null, with
// dispatchUnavailable, while Dispatch is off or cannot be read). The name
// is matched exactly, as Dispatch matches it; 404 not_found only for a
// name no source knows (neither store, nor a run's people or its tasks'
// owners), so a person removed from the saved crew after a launch still
// has a page with that run's work on it.
func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !noQuery(w, r) {
		return
	}
	// A name no store could hold is no one's: Dispatch's person.name rule.
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > crew.MaxPersonName || strings.ContainsFunc(name, unicode.IsControl) {
		writeError(w, http.StatusNotFound, "not_found", "no such person")
		return
	}
	out := personWork{Name: name, Crews: []personCrew{}, Tasks: []personTask{}, Decisions: []any{}}
	known := false
	if s.crews != nil {
		for offset := 0; ; offset += maxCrewPage {
			page, total, err := s.crews.List(offset, maxCrewPage)
			if err != nil {
				s.log.Error("crews list failed", "err", err)
				writeError(w, http.StatusInternalServerError, "store_failed", "could not read the crews")
				return
			}
			for _, c := range page {
				for _, p := range c.People {
					if p.Name != name {
						continue
					}
					known = true
					if len(out.Crews) == maxWorkCrews {
						out.CrewsMore = true
						continue
					}
					out.Crews = append(out.Crews, personCrew{ID: c.ID, Name: c.Name, Responsibilities: append([]string{}, p.Responsibilities...)})
				}
			}
			if offset+len(page) >= total || len(page) == 0 {
				break
			}
		}
	}
	// The runs the engine holds, then the records of those it does not.
	runs := s.runs.List()
	if s.records != nil {
		held := map[string]bool{}
		for _, run := range runs {
			held[run.ID] = true
		}
		recorded, err := s.records.List("")
		if err != nil {
			s.log.Warn("run records", "err", err)
		}
		for _, run := range recorded {
			if !held[run.ID] {
				runs = append(runs, run)
			}
		}
	}
	slices.SortStableFunc(runs, func(a, b crew.Run) int {
		return cmp.Or(b.StartedAt.Compare(a.StartedAt), strings.Compare(b.ID, a.ID))
	})
	for _, run := range runs {
		if slices.ContainsFunc(run.People, func(p crew.Person) bool { return p.Name == name }) {
			known = true
		}
		for _, t := range run.Tasks {
			if t.Owner != name {
				continue
			}
			known = true
			if len(out.Tasks) == maxWorkTasks {
				out.More = true
				continue
			}
			out.Tasks = append(out.Tasks, personTask{Run: run.ID, RunLabel: cmp.Or(run.Label, run.Name), Task: t.ID, State: t.State, Member: t.Member, OwnerFrom: t.OwnerFrom})
		}
	}
	out.DispatchUnavailable = s.dispatch == nil
	if ds := s.dispatch; ds != nil {
		tickets, inDirectory, err := s.personTickets(r, ds, name)
		if err != nil {
			s.log.Warn("Dispatch could not be read for a person's work", "err", err)
			out.DispatchUnavailable = true
		} else {
			out.Tickets, out.Dispatch = tickets, inDirectory
			known = known || inDirectory || tickets.Owner > 0 || tickets.Assignee > 0
		}
	}
	if !known {
		writeError(w, http.StatusNotFound, "not_found", "no such person")
		return
	}
	writeDispatch(w, http.StatusOK, out)
}

// personTickets reads what Dispatch holds for name: whether its directory
// has the name, and the counts of its own lists of the tickets the person
// owns and of those they are assigned, with the lists' links.
func (s *Server) personTickets(r *http.Request, ds *dispatch.Store, name string) (*personTickets, bool, error) {
	people, err := ds.People(r.Context())
	if err != nil {
		return nil, false, err
	}
	inDirectory := slices.ContainsFunc(people, func(p dispatch.Person) bool { return p.Name == name })
	assignee := "person:" + name
	owned, err := ds.List(r.Context(), dispatch.Query{Filter: dispatch.Filter{Owner: name}, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	assigned, err := ds.List(r.Context(), dispatch.Query{Filter: dispatch.Filter{Assignees: []string{assignee}}, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	return &personTickets{
		Owner:        owned.Total,
		Assignee:     assigned.Total,
		OwnerHref:    "/api/dispatch/tickets?" + url.Values{"owner": {name}}.Encode(),
		AssigneeHref: "/api/dispatch/tickets?" + url.Values{"assignee": {assignee}}.Encode(),
	}, inDirectory, nil
}
