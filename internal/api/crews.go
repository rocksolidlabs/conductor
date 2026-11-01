package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
)

// maxCrewBody bounds the body of a crew create or update: 2 MiB, twice
// crew.MaxEncoded. A client may indent a crew, or escape more of it than the
// server does, so a crew near the bound on its file still fits; Validate holds
// the crew itself to crew.MaxEncoded as its file.
const maxCrewBody = 2 << 20

// The crew list's page: limit defaults to 100 and takes 1 to 500.
const (
	defaultCrewPage = 100
	maxCrewPage     = 500
)

// crewInput is the body of POST /api/crews and PUT /api/crews/{id}: a crew
// without what the server sets. A client that sends id, createdAt, updatedAt
// or manifestRevision is refused like one that sends any other unknown field.
// Its tags are crew.Crew's, so the operating guide's schema of it
// (crew_create) requires what a crew read back always carries.
type crewInput struct {
	Name               string        `json:"name"`
	Goal               string        `json:"goal"`
	Cwd                string        `json:"cwd"`
	Where              string        `json:"where"`
	Isolation          string        `json:"isolation"`
	OpenAfterLaunch    bool          `json:"openAfterLaunch"`
	ViewLinkTTLSeconds int64         `json:"viewLinkTtlSeconds,omitempty"`
	Yolo               *bool         `json:"yolo,omitempty"`
	Members            []crew.Member `json:"members"`
	// Tasks and Setup make a managed crew (#334): validated with the rest.
	// Manifest is its project manifest, of which Setup is the legacy form: a
	// crew has one or the other. The store counts its changes itself
	// (crew.Crew.ManifestRevision), never from the body.
	Tasks    []crew.Task    `json:"tasks,omitempty"`
	Setup    []crew.Check   `json:"setup,omitempty"`
	Manifest *crew.Manifest `json:"manifest,omitempty"`
}

// handleListCrews lists one page of the saved crews' summaries, ordered by
// name: GET /api/crews?offset=0&limit=100. total counts every crew. Without a
// data directory there are none.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r)
	if !ok {
		return
	}
	offset, limit, err := pageParams(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	list, total := []crew.Summary{}, 0
	if s.crews != nil {
		page, n, err := s.crews.List(offset, limit)
		if err != nil {
			s.log.Error("crews list failed", "err", err)
			writeError(w, http.StatusInternalServerError, "store_failed", "could not read the crews")
			return
		}
		list, total = page, n
	}
	writeJSON(w, http.StatusOK, map[string]any{"crews": list, "total": total})
}

// pageParams reads offset (a whole number from 0, default 0) and limit (1 to
// maxCrewPage, default defaultCrewPage).
func pageParams(q url.Values) (offset, limit int, err error) {
	offset, limit = 0, defaultCrewPage
	if v := q.Get("offset"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n >= 0 {
			offset = n
		} else {
			return 0, 0, errors.New("offset must be a whole number from 0")
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n >= 1 && n <= maxCrewPage {
			limit = n
		} else {
			return 0, 0, fmt.Errorf("limit must be a whole number from 1 to %d", maxCrewPage)
		}
	}
	return offset, limit, nil
}

// handleGetCrew answers one crew in full, for the editor.
func (s *Server) handleGetCrew(w http.ResponseWriter, r *http.Request) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.crews.Get(id)
	if err != nil {
		s.crewStoreError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"crew": c})
}

// handleCreateCrew saves a new crew under an ID the store derives from its name.
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readCrew(w, r)
	if !ok {
		return
	}
	saved, err := s.crews.Create(c)
	if err != nil {
		s.crewStoreError(w, "", err)
		return
	}
	s.log.Info("crew created", "crew", saved.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"crew": saved})
}

// handleUpdateCrew replaces a crew's fields with the body. Its ID and creation
// time never change.
func (s *Server) handleUpdateCrew(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := s.readCrew(w, r)
	if !ok {
		return
	}
	saved, err := s.crews.Update(id, c)
	if err != nil {
		s.crewStoreError(w, id, err)
		return
	}
	s.log.Info("crew updated", "crew", id)
	writeJSON(w, http.StatusOK, map[string]any{"crew": saved})
}

// handleDeleteCrew deletes a crew.
func (s *Server) handleDeleteCrew(w http.ResponseWriter, r *http.Request) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	id := r.PathValue("id")
	found, err := s.crews.Delete(id)
	if err != nil {
		s.crewStoreError(w, id, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such crew")
		return
	}
	s.log.Info("crew deleted", "crew", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleDuplicateCrew saves a copy of a crew under <id>-copy. A crew with an
// agent the catalog no longer has is not copied.
func (s *Server) handleDuplicateCrew(w http.ResponseWriter, r *http.Request) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	id := r.PathValue("id")
	src, err := s.crews.Get(id)
	if err != nil {
		s.crewStoreError(w, id, err)
		return
	}
	if err := src.CheckAgents(s.Catalog()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return
	}
	saved, err := s.crews.Duplicate(id)
	if err != nil {
		s.crewStoreError(w, id, err)
		return
	}
	s.log.Info("crew duplicated", "crew", id, "copy", saved.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"crew": saved})
}

// readCrew decodes the body of a create or an update into a crew and checks
// it: first its rules, on the name trimmed as the store saves it, then its
// agents against the catalog. So a crew that breaks a rule is refused for it
// (400) before the store looks at the id of an update (404), and an unknown
// agent is 400 whatever else holds. The store checks the crew again as it
// saves it, with its id and times. When readCrew reports false it has
// answered the request.
func (s *Server) readCrew(w http.ResponseWriter, r *http.Request) (crew.Crew, bool) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return crew.Crew{}, false
	}
	var in crewInput
	if err := decodeJSONLimit(w, r, &in, maxCrewBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return crew.Crew{}, false
	}
	c := crew.Crew{
		Name: in.Name, Goal: in.Goal, Cwd: in.Cwd, Where: in.Where, Isolation: in.Isolation,
		OpenAfterLaunch: in.OpenAfterLaunch, ViewLinkTTLSeconds: in.ViewLinkTTLSeconds, Yolo: in.Yolo, Members: in.Members, Tasks: in.Tasks, Setup: in.Setup,
		Manifest: in.Manifest,
	}
	trimmed := c
	trimmed.Name = strings.TrimSpace(c.Name)
	if err := trimmed.ValidateSave(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return crew.Crew{}, false
	}
	if err := c.CheckAgents(s.Catalog()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
		return crew.Crew{}, false
	}
	return c, true
}

// crewStoreError answers an error from the crew store. A failure of the
// store itself is logged with its cause, which names the data directory; the
// reply does not, and says whether a read or a write failed.
func (s *Server) crewStoreError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, crew.ErrUnreadable):
		// Says what is wrong with the file, which it names, without the
		// data directory's path.
		writeError(w, http.StatusConflict, "crew_unreadable", err.Error())
	case errors.Is(err, crew.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_crew", err.Error())
	case errors.Is(err, crew.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such crew")
	case errors.Is(err, crew.ErrWrite):
		s.log.Error("crew save failed", "crew", id, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "could not save the crews")
	default:
		// The crews directory, or a crew's file, could not be read.
		s.log.Error("crew read failed", "crew", id, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "could not read the crews")
	}
}

// SeedExampleCrews saves the example crews (crew.Examples) whose ids have no
// file in the crews directory, with the server's default working directory,
// and returns what it added and what it skipped. conductor serve --examples
// and POST /api/crews/examples both come here.
func (s *Server) SeedExampleCrews() (added, skipped []string, err error) {
	if s.crews == nil {
		return nil, nil, errors.New("no data directory is configured")
	}
	return s.crews.Seed(crew.Examples(s.cfg.DefaultCwd, time.Now().UTC()))
}

// handleSeedExamples answers POST /api/crews/examples with {added, skipped}.
// The examples' agents, the claude and codex built-ins, are not checked
// against the catalog: a catalog that hides them still gets the examples,
// the editor shows the agent as not in the catalog, and the launch refuses
// it.
func (s *Server) handleSeedExamples(w http.ResponseWriter, r *http.Request) {
	if s.crews == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	added, skipped, err := s.SeedExampleCrews()
	if err != nil {
		s.crewStoreError(w, "examples", err)
		return
	}
	s.log.Info("example crews seeded", "added", added, "skipped", skipped)
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped})
}

// handleCrewRuns lists a crew's runs, newest first: the live ones the engine
// holds, then the records of those that ended and left it (crew.Records,
// kept under runs/ in the data directory), each as GET /api/runs/{run}
// answers it. 404 for a crew that is neither saved nor recorded.
func (s *Server) handleCrewRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !crew.ValidID(id) {
		writeError(w, http.StatusNotFound, "not_found", "no such crew")
		return
	}
	live := []crew.Run{}
	seen := map[string]bool{}
	for _, run := range s.runs.List() {
		if run.CrewID == id {
			live = append(live, run)
			seen[run.ID] = true
		}
	}
	var recorded []crew.Run
	if s.records != nil {
		all, err := s.records.List(id)
		if err != nil {
			s.log.Warn("run records", "crew", id, "err", err)
		}
		for _, run := range all {
			if !seen[run.ID] {
				// A list carries no chat; a run read alone does.
				run.Chat = nil
				recorded = append(recorded, run)
			}
		}
	}
	known := len(live) > 0 || len(recorded) > 0
	if !known && s.crews != nil {
		if _, err := s.crews.Get(id); err == nil {
			known = true
		}
	}
	if !known {
		writeError(w, http.StatusNotFound, "not_found", "no such crew")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": append(live, recorded...), "live": len(live)})
}
