package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
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
