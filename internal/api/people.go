package api

import (
	"cmp"
	"context"
	"encoding/base64"
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
// tickets they own and how many name them an assignee, each the total of
// Dispatch's own list (GET /api/dispatch/tickets, owner= and
// assignee=person:), and that list's link, which the person's page reads
// and pages through with the list's cursor. Two lists, not one: every
// condition of Dispatch's filter must hold, so no one list is the union.
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
	// CrewsUnavailable says the saved crews, or some of them, could not
	// be read: the crews listed are those read, and may not be all.
	CrewsUnavailable bool `json:"crewsUnavailable,omitempty"`
	// RecordsUnavailable says the managed runs' records, or some of them,
	// could not be read: the tasks listed are those of the runs the
	// engine holds and the records read, and may not be all.
	RecordsUnavailable bool `json:"recordsUnavailable,omitempty"`
}

// A person's name in a path: the one rule every link to a person's page
// and every read of their work uses (web/app/utils/people.ts, personSegment
// and personFromSegment, the same rule; testdata/person-segments.json the
// cases both are tested against). Any string the people rules allow is a
// name, so a name is written as is only when nothing between the browser
// and this route can read it as something else, and otherwise as a token
// nothing reads:
//
//   - A plain name is written as itself, escaped as a segment (a space as
//     %20, a character past ASCII as its UTF-8 bytes): every character an
//     ASCII letter or digit, '-', '_', '.', ' ', or one past ASCII; the
//     first neither '.' nor ' ', the last not ' '. Unescaped it holds no
//     '/', '\', '%', '?', '#', ';' or '~' and is never "." or "..", so no
//     decoding, second decoding or cleaning of the path (a browser's, the
//     router's, this server's, the embedded app's, a proxy's) changes it
//     or moves it out of its segment.
//   - Any other name is written as "~" and the base64url of its UTF-8, with
//     no padding: letters, digits, '-' and '_' after the "~", which nothing
//     decodes or cleans either.
//
// A segment is read back only in the one form its name is written in, so
// each name has one address and no address is another name's.

// plainPersonName reports whether name is written as itself in a path.
func plainPersonName(name string) bool {
	if name == "" || name[0] == '.' || name[0] == ' ' || name[len(name)-1] == ' ' {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= utf8.RuneSelf, 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == ' ':
		default:
			return false
		}
	}
	return true
}

// personSegment is the path segment of a person's name, escaped as a URL
// writes it.
func personSegment(name string) string {
	if plainPersonName(name) {
		return url.PathEscape(name)
	}
	return "~" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// personFromSegment is the name a segment carries, the segment unescaped as
// the route's path value has it; false for a segment that is no name's (not
// the one form personSegment writes for what it decodes to).
func personFromSegment(segment string) (string, bool) {
	token, coded := strings.CutPrefix(segment, "~")
	if !coded {
		return segment, plainPersonName(segment)
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	name := string(b)
	// The decoder passes line breaks over: the token must be the one
	// written for the name.
	if err != nil || name == "" || !utf8.Valid(b) || plainPersonName(name) || base64.RawURLEncoding.EncodeToString(b) != token {
		return "", false
	}
	return name, true
}

// handlePerson answers GET /api/people/{name}: a person's work. The
// crews that list them (at most 100, crewsMore past them), the tasks they
// own in the runs the engine holds and the managed runs' records, as each
// run's crew was launched (at most 200, the newest run first and its tasks
// in the crew's order, more past them), and Dispatch's tickets they own or
// are assigned (counts and the list's links; null, with
// dispatchUnavailable, while Dispatch is off or cannot be read). The name
// is the path's segment (personFromSegment), matched exactly, as Dispatch
// matches it; 404 not_found for a segment that is no name's, and for a
// name no source knows (neither store, nor a run's people or its tasks'
// owners) when every source was read, every crew and record included, so
// a person removed from the saved crew after a launch still has a page
// with that run's work on it, and a name only an unread source could know
// is answered with what was read and which list was not read whole
// (crewsUnavailable, recordsUnavailable, dispatchUnavailable), never as
// no one's.
func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	name, ok := personFromSegment(r.PathValue("name"))
	if !noQuery(w, r) {
		return
	}
	// A segment that is no name's, and a name no store could hold
	// (Dispatch's person.name rule), are no one's.
	if !ok || name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > crew.MaxPersonName || strings.ContainsFunc(name, unicode.IsControl) {
		writeError(w, http.StatusNotFound, "not_found", "no such person")
		return
	}
	out := personWork{Name: name, Crews: []personCrew{}, Tasks: []personTask{}, Decisions: []any{}}
	known := false
	// Every read below has its place in the reply: a list is read and holds
	// rows, read and empty, or not read, wholly or for some of its records,
	// and then says so (crewsUnavailable, recordsUnavailable,
	// dispatchUnavailable), never drawn as fewer rows.
	if s.crews != nil {
		crews, unread, err := s.crews.Summaries()
		if err != nil {
			s.log.Warn("the crews could not be read for a person's work", "err", err)
			out.CrewsUnavailable = true
		} else if unread > 0 {
			s.log.Warn("crews could not be read for a person's work", "unread", unread)
			out.CrewsUnavailable = true
		}
		for _, c := range crews {
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
	}
	// The runs the engine holds, then the records of those it does not.
	runs := s.runs.List()
	if s.records != nil {
		held := map[string]bool{}
		for _, run := range runs {
			held[run.ID] = true
		}
		recorded, unread, err := s.records.ListRead("")
		if err != nil {
			s.log.Warn("run records could not be read for a person's work", "err", err)
			out.RecordsUnavailable = true
		} else if unread > 0 {
			s.log.Warn("run records could not be read for a person's work", "unread", unread)
			out.RecordsUnavailable = true
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
	ds := s.ticketReads()
	out.DispatchUnavailable = ds == nil
	if ds != nil {
		// The directory's answer stands when the counts then fail: a
		// person it lists is known whatever the lists say.
		tickets, inDirectory, err := personTicketsOf(r.Context(), ds, name)
		out.Dispatch = inDirectory
		known = known || inDirectory
		if err != nil {
			s.log.Warn("Dispatch could not be read for a person's work", "err", err)
			out.DispatchUnavailable = true
		} else {
			out.Tickets = tickets
			known = known || tickets.Owner > 0 || tickets.Assignee > 0
		}
	}
	if !known && !out.DispatchUnavailable && !out.RecordsUnavailable && !out.CrewsUnavailable {
		writeError(w, http.StatusNotFound, "not_found", "no such person")
		return
	}
	writeDispatch(w, http.StatusOK, out)
}

// ticketDirectory is what a person's work reads of Dispatch's store: its
// directory and its lists.
type ticketDirectory interface {
	People(context.Context) ([]dispatch.Person, error)
	List(context.Context, dispatch.Query) (dispatch.Page, error)
}

// ticketReads is what a person's work reads of Dispatch: its store, or the
// seam a test puts in its place (personDispatch); nil while Dispatch is off.
func (s *Server) ticketReads() ticketDirectory {
	if s.personDispatch != nil {
		return s.personDispatch
	}
	if s.dispatch == nil {
		return nil
	}
	return s.dispatch
}

// personTicketsOf reads what Dispatch holds for name: whether its directory
// has the name, and the counts of its own lists of the tickets the person
// owns and of those they are assigned, with the lists' links. The
// directory's answer is returned with a failed count's error.
func personTicketsOf(ctx context.Context, ds ticketDirectory, name string) (*personTickets, bool, error) {
	people, err := ds.People(ctx)
	if err != nil {
		return nil, false, err
	}
	inDirectory := slices.ContainsFunc(people, func(p dispatch.Person) bool { return p.Name == name })
	assignee := "person:" + name
	owned, err := ds.List(ctx, dispatch.Query{Filter: dispatch.Filter{Owner: name}, Limit: 1})
	if err != nil {
		return nil, inDirectory, err
	}
	assigned, err := ds.List(ctx, dispatch.Query{Filter: dispatch.Filter{Assignees: []string{assignee}}, Limit: 1})
	if err != nil {
		return nil, inDirectory, err
	}
	return &personTickets{
		Owner:        owned.Total,
		Assignee:     assigned.Total,
		OwnerHref:    "/api/dispatch/tickets?" + url.Values{"owner": {name}}.Encode(),
		AssigneeHref: "/api/dispatch/tickets?" + url.Values{"assignee": {assignee}}.Encode(),
	}, inDirectory, nil
}
