package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"sync"

	"github.com/rocksolidlabs/conductor/internal/agents"
	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// catalogFile is the document in the data directory that holds the overlay the
// Agents page edits. The configured catalog is never written.
const catalogFile = "catalog.json"

// catalogEntry is an agent as the catalog routes answer with it: env values
// masked, where the catalog took it from and, for a saved agent that replaces
// a built-in or configured one, where that one came from (deleting the saved
// agent brings it back), and whether its program resolves on this server
// (lookups: cached 30 s; a lookup that does not answer within 2 s counts)
// and is not a known other program of the same name: the Go migrations tool
// called goose is not Goose, so Goose is not available where only it is.
// Its site is the agent's own field.
type catalogEntry struct {
	catalog.Agent
	Source    catalog.Source `json:"source,omitempty"`
	Replaces  catalog.Source `json:"replaces,omitempty"`
	Available bool           `json:"available"`
	// OnWindows is where the program was found when that is a Windows
	// program seen from inside WSL (under /mnt): not installed here.
	OnWindows string `json:"onWindows,omitempty"`
	// Identity is what the adapter's probe found the program to be (see
	// Identity); absent for an agent that is not probed.
	Identity *Identity `json:"identity,omitempty"`
}

// entry is a as the routes show it, by the effective catalog cat, the
// configured one, base, and installed, which says whether a program could
// start on this server (the server's lookups), and identity, what its probe
// found (nil for none).
func entry(a catalog.Agent, cat, base catalog.Catalog, installed func(program string) bool, identity func(a catalog.Agent) *Identity) catalogEntry {
	e := catalogEntry{Agent: a.Redacted(), Source: cat.Source(a.ID)}
	if e.Source == catalog.SourceSaved {
		e.Replaces = base.Source(a.ID)
	}
	if len(a.Command) > 0 {
		e.Available = installed(a.Command[0])
		if identity != nil {
			e.Identity = identity(a)
		}
		// A known impostor is not the agent: never offered to launch. A
		// verified probe that matched nothing keeps the agent offered, with
		// its warning: a new release may print its version differently.
		if e.Identity != nil && e.Identity.Ran && e.Identity.Impostor {
			e.Available = false
		}
	}
	return e
}

// installedNow is the server's lookups' installed for one request's ctx, for
// the catalog routes that answer with one agent.
func (s *Server) installedNow(ctx context.Context) func(program string) bool {
	return func(program string) bool { return s.lookups.installed(ctx, program) }
}

// identityNow is the identity of one agent for one request's ctx: its
// program looked up now, its probe run or read from the cache.
func (s *Server) identityNow(ctx context.Context) func(a catalog.Agent) *Identity {
	return func(a catalog.Agent) *Identity {
		if len(a.Command) == 0 || relativePath(a.Command[0]) {
			return nil
		}
		path, ok, known := s.lookups.found(ctx, a.Command[0])
		if !ok || !known {
			return nil
		}
		return s.identityOf(ctx, a, path, false)
	}
}

// saveAgentRequest is the body of POST /api/catalog: an agent, as a client
// builds it or as GET /api/catalog lists it. source, replaces and available,
// which the listing adds, are taken and ignored, so an agent read there can be
// sent back as it is; any other field the agent does not have is refused.
type saveAgentRequest struct {
	catalog.Agent
	Identity  json.RawMessage `json:"identity,omitempty"`
	Source    json.RawMessage `json:"source,omitempty"`
	Replaces  json.RawMessage `json:"replaces,omitempty"`
	Available json.RawMessage `json:"available,omitempty"`
}

// handleCatalog lists the launchable agents, env values masked, and the IDs
// the overlay hides, which POST /api/catalog/{id}/unhide brings back.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	out, hidden := s.catalogEntries(r.Context())
	// yoloDefault is the server's yolo choice, which a launch or a crew that
	// says nothing follows.
	writeJSON(w, http.StatusOK, map[string]any{"agents": out, "hidden": hidden, "yoloDefault": s.cfg.Yolo})
}

// catalogEntries lists the catalog's agents with what this server knows of
// them, waiting for ctx at most about lookupWait for the programs and
// probeWait for the probes, and the IDs the overlay hides.
func (s *Server) catalogEntries(ctx context.Context) ([]catalogEntry, []string) {
	s.catalogMu.Lock()
	cat, hidden := s.catalog, uniqueIDs(s.overlay.Hidden)
	s.catalogMu.Unlock()
	list := cat.List()
	// The programs are asked about together: a request waits about one
	// lookup's time for those not looked up lately, at most lookupWait.
	programs := make([]string, 0, len(list))
	for _, a := range list {
		if len(a.Command) > 0 {
			programs = append(programs, a.Command[0])
		}
	}
	answers := s.lookups.warmPaths(ctx, programs)
	installed := func(program string) bool { return answers[program].installed }
	// The probes of the agents found are asked for together, and waited for
	// together: probeWait in all, not each (asked one after another, a cold
	// catalog waited up to probeWait per agent, seen on the Windows app as
	// the Launch dialog's long empty list on its first open). They run
	// probeWorkers at a time; one not answered by then is reported pending.
	ids := make(map[string]*Identity, len(list))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range list {
		if len(a.Command) == 0 || answers[a.Command[0]].path == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := s.identityOf(ctx, a, answers[a.Command[0]].path, false)
			mu.Lock()
			ids[a.ID] = id
			mu.Unlock()
		}()
	}
	wg.Wait()
	identity := func(a catalog.Agent) *Identity { return ids[a.ID] }
	out := make([]catalogEntry, 0, len(list))
	for _, a := range list {
		e := entry(a, cat, s.base, installed, identity)
		if len(a.Command) > 0 {
			e.OnWindows = answers[a.Command[0]].onWindows
		}
		out = append(out, e)
	}
	return out, hidden
}

// WarmCatalog does at the server's start what the first GET /api/catalog
// would: it looks the agents' programs up and runs their probes, so the
// Launch dialog first opened after the app starts finds them known (inside
// WSL the first lookups and probes of a dozen agents take seconds).
func (s *Server) WarmCatalog(ctx context.Context) {
	s.catalogEntries(ctx)
}

// handleSaveAgent adds an agent to the overlay, or replaces the agent with the
// same ID (a built-in too), and persists the overlay.
func (s *Server) handleSaveAgent(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	var req saveAgentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	saved, aerr := s.saveAgent(r.Context(), req.Agent)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": saved})
}

// saveAgent adds a to the overlay, or replaces the entry with its ID, and
// returns it as the catalog now lists it. The reply is built from the catalog
// this save published (storeAgent), so it is this save's and no later edit's;
// its program is looked up after catalogEditMu is released, for as long as
// ctx lasts (at most lookupWait).
func (s *Server) saveAgent(ctx context.Context, a catalog.Agent) (catalogEntry, *apiError) {
	saved, cat, aerr := s.storeAgent(a)
	if aerr != nil {
		return catalogEntry{}, aerr
	}
	return entry(saved, cat, s.base, s.installedNow(ctx), s.identityNow(ctx)), nil
}

// storeAgent is saveAgent's edit. It holds catalogEditMu from reading the
// stored agent to publishing the catalog, and returns the agent as that
// catalog lists it, and the catalog; readers and launches wait for none of it.
func (s *Server) storeAgent(a catalog.Agent) (catalog.Agent, catalog.Catalog, *apiError) {
	s.catalogEditMu.Lock()
	defer s.catalogEditMu.Unlock()
	// The overlay is read under the editor lock, so no other save can change
	// it in between.
	base, _ := s.base.Get(a.ID)
	if err := keepMaskedEnv(&a, savedAgent(s.overlay.Agents, a.ID), base); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	// Check a by itself, so the message is about this agent and not an index
	// into the overlay.
	var probe catalog.Catalog
	if err := probe.Upsert(a); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	if err := checkAdapter(a); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	ov := s.overlay
	ov.Agents = upsertAgent(ov.Agents, a)
	ov.Hidden = withoutID(ov.Hidden, a.ID)
	if err := s.commitOverlay(ov); err != nil {
		s.log.Error("catalog save failed", "agent", a.ID, "err", err)
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusInternalServerError, "store_failed", "could not save the catalog")
	}
	s.log.Info("catalog agent saved", "agent", a.ID)
	cat := s.Catalog()
	saved, _ := cat.Get(a.ID)
	return saved, cat, nil
}

// handleDeleteAgent removes the overlay entry with the given ID, which restores
// a built-in or configured agent it replaced. An agent that is not in the
// overlay is hidden instead. Sessions already running are not touched.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	if aerr := s.deleteAgent(r.PathValue("id")); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAgent removes the overlay entry with the given ID, or hides an agent
// that has none, under catalogEditMu.
func (s *Server) deleteAgent(id string) *apiError {
	s.catalogEditMu.Lock()
	defer s.catalogEditMu.Unlock()
	ov := s.overlay
	inOverlay := slices.ContainsFunc(ov.Agents, func(a catalog.Agent) bool { return a.ID == id })
	_, listed := s.Catalog().Get(id)
	action := "removed"
	switch {
	case inOverlay:
		ov.Agents = withoutAgent(ov.Agents, id)
	case listed:
		ov.Hidden = append(slices.Clone(ov.Hidden), id)
		action = "hidden"
	default:
		return newAPIError(http.StatusNotFound, "not_found", "no such agent")
	}
	if err := s.commitOverlay(ov); err != nil {
		s.log.Error("catalog save failed", "agent", id, "err", err)
		return newAPIError(http.StatusInternalServerError, "store_failed", "could not save the catalog")
	}
	s.log.Info("catalog agent changed", "agent", id, "action", action)
	return nil
}

// handleUnhideAgent takes an ID off the overlay's hidden list, which brings
// back the built-in or configured agent it hid, and persists the overlay. The
// reply carries that agent, or no agent when none has the ID any more (a
// hand-edited file, or an agent since removed from the config).
func (s *Server) handleUnhideAgent(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	a, found, aerr := s.unhideAgent(r.Context(), r.PathValue("id"))
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	out := map[string]any{}
	if found {
		out["agent"] = a
	}
	writeJSON(w, http.StatusOK, out)
}

// unhideAgent takes id off the hidden list and returns the agent it brings
// back, if one has that ID, as the reply shows it: built from the catalog the
// change published (unhide), its program looked up after catalogEditMu is
// released.
func (s *Server) unhideAgent(ctx context.Context, id string) (catalogEntry, bool, *apiError) {
	a, cat, ok, aerr := s.unhide(id)
	if aerr != nil {
		return catalogEntry{}, false, aerr
	}
	return entry(a, cat, s.base, s.installedNow(ctx), s.identityNow(ctx)), ok, nil
}

// unhide is unhideAgent's edit, under catalogEditMu: it returns the agent the
// published catalog has under id, whether it has one, and the catalog.
func (s *Server) unhide(id string) (catalog.Agent, catalog.Catalog, bool, *apiError) {
	s.catalogEditMu.Lock()
	defer s.catalogEditMu.Unlock()
	ov := s.overlay
	if !slices.Contains(ov.Hidden, id) {
		return catalog.Agent{}, catalog.Catalog{}, false, newAPIError(http.StatusNotFound, "not_found", "no hidden agent with that id")
	}
	ov.Hidden = withoutID(ov.Hidden, id)
	if err := s.commitOverlay(ov); err != nil {
		s.log.Error("catalog save failed", "agent", id, "err", err)
		return catalog.Agent{}, catalog.Catalog{}, false, newAPIError(http.StatusInternalServerError, "store_failed", "could not save the catalog")
	}
	s.log.Info("catalog agent changed", "agent", id, "action", "unhidden")
	cat := s.Catalog()
	a, ok := cat.Get(id)
	return a, cat, ok, nil
}

// handleCloneAgent makes a copy of the agent {id} under another id (#291):
// POST /api/catalog/{id}/clone, the body the copy as a client builds it, as
// POST /api/catalog takes it. What the body leaves out that the Agents page
// has no control for (adapter, signal, working directory, icon, yolo and
// session recipes, trust prompt and answers, startup questions, probe) is
// the source's as the catalog resolves it, so the copy launches as the
// source does: with its hooks, its trust answers, its Resume; an env value
// of *** is the source's value for the key, copied and never shown. The copy
// is a saved agent of its own, which replaces nothing and follows nothing:
// the id must be free, 409 id_taken otherwise.
func (s *Server) handleCloneAgent(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
		return
	}
	var req saveAgentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	saved, aerr := s.cloneAgent(r.Context(), r.PathValue("id"), req.Agent)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": saved})
}

func (s *Server) cloneAgent(ctx context.Context, from string, a catalog.Agent) (catalogEntry, *apiError) {
	saved, cat, aerr := s.storeClone(from, a)
	if aerr != nil {
		return catalogEntry{}, aerr
	}
	return entry(saved, cat, s.base, s.installedNow(ctx), s.identityNow(ctx)), nil
}

// storeClone saves a, a client's copy of the agent from, filled from the
// source where it says nothing (copyFromSource), under an id no agent has.
func (s *Server) storeClone(from string, a catalog.Agent) (catalog.Agent, catalog.Catalog, *apiError) {
	s.catalogEditMu.Lock()
	defer s.catalogEditMu.Unlock()
	src, ok := s.Catalog().Get(from)
	if !ok {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusNotFound, "not_found", "no such agent")
	}
	if err := copyFromSource(&a, src); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	var probe catalog.Catalog
	if err := probe.Upsert(a); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	if err := checkAdapter(a); err != nil {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusBadRequest, "invalid_agent", err.Error())
	}
	if s.idTaken(a.ID) {
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusConflict, "id_taken", fmt.Sprintf("an agent with the id %s exists; pick another for the copy", a.ID))
	}
	ov := s.overlay
	ov.Agents = upsertAgent(ov.Agents, a)
	if err := s.commitOverlay(ov); err != nil {
		s.log.Error("catalog save failed", "agent", a.ID, "err", err)
		return catalog.Agent{}, catalog.Catalog{}, newAPIError(http.StatusInternalServerError, "store_failed", "could not save the catalog")
	}
	s.log.Info("catalog agent cloned", "agent", a.ID, "from", from)
	cat := s.Catalog()
	saved, _ := cat.Get(a.ID)
	return saved, cat, nil
}

// idTaken says whether an agent has the id: one the catalog lists, a
// built-in or configured one (hidden, it keeps its id), a saved one, or an
// id on the hidden list. The caller holds catalogEditMu.
func (s *Server) idTaken(id string) bool {
	if _, ok := s.Catalog().Get(id); ok {
		return true
	}
	if _, ok := s.base.Get(id); ok {
		return true
	}
	return savedAgent(s.overlay.Agents, id).ID == id || slices.Contains(s.overlay.Hidden, id)
}

// copyFromSource fills what a, a client's copy of src, leaves out: the fields
// the Agents page has no control for, taken from src as the catalog resolved
// it, and every env value given as catalog.RedactedValue, which is src's
// value for the key. A masked key src has no value for is an error, the
// first by key name.
func copyFromSource(a *catalog.Agent, src catalog.Agent) error {
	src = src.Copy()
	if a.Adapter == "" {
		a.Adapter = src.Adapter
	}
	switch {
	case a.Signal == nil:
		a.Signal = src.Signal
	case a.Signal.WorkingTitle == "" && src.Signal != nil:
		// The form has no control for the working title (as inherit in the
		// catalog reads an override saved without one).
		a.Signal.WorkingTitle = src.Signal.WorkingTitle
	}
	if a.Cwd == "" {
		a.Cwd = src.Cwd
	}
	if a.Icon == "" {
		a.Icon = src.Icon
	}
	if a.Yolo == nil {
		a.Yolo = src.Yolo
	}
	if a.TrustPrompt == "" {
		a.TrustPrompt = src.TrustPrompt
	}
	if a.PromptShape == "" {
		a.PromptShape = src.PromptShape
	}
	if a.TrustAnswers == nil {
		a.TrustAnswers = src.TrustAnswers
	}
	if a.Questions == nil {
		a.Questions = src.Questions
	}
	if a.Session == nil {
		a.Session = src.Session
	}
	if a.Probe == nil {
		a.Probe = src.Probe
	}
	// A page older than the browser's control (#436) sends none: the copy
	// opens its links where the source does. {} is the default browser.
	if a.Browser == nil {
		a.Browser = src.Browser
	}
	for _, k := range slices.Sorted(maps.Keys(a.Env)) {
		if a.Env[k] != catalog.RedactedValue {
			continue
		}
		v, ok := src.Env[k]
		if !ok || v == catalog.RedactedValue {
			return fmt.Errorf("env %s: value is redacted, and the agent copied has none for it; set a real value", k)
		}
		a.Env[k] = v
	}
	return nil
}

// checkCommandRequest is the body of POST /api/catalog/check.
type checkCommandRequest struct {
	Command []string `json:"command"`
	// Adapter, when given, runs its identity probe on the program found,
	// with Env as the agent's own variables; the reply carries `identity`.
	Adapter string            `json:"adapter,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// handleCheckCommand reports whether the program a command starts resolves on
// this server, the way exec resolves it when a session launches. Only
// command[0] is looked up, now and not from the cache, which keeps the answer
// for GET /api/catalog; nothing is run. A program that is not found is a
// normal answer, not an error: the agent may be meant for `conductor host`.
// Two programs are not judged here, and unknown says why; the catalog's
// available counts both as installed: a relative path with a separator
// ("relative", resolved at launch in the session's working directory, which
// the server's own cannot stand for) and one whose lookup does not answer
// within lookupWait ("timeout"; its answer is kept when it lands).
func (s *Server) handleCheckCommand(w http.ResponseWriter, r *http.Request) {
	var req checkCommandRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.Command) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "command must have at least one element")
		return
	}
	program := req.Command[0]
	if relativePath(program) {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "unknown": "relative"})
		return
	}
	path, ok, known := s.lookups.check(r.Context(), program)
	switch {
	case !known:
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "unknown": "timeout"})
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
	default:
		out := map[string]any{"found": true, "path": path}
		if req.Adapter != "" {
			if err := agents.CheckAdapter(req.Adapter); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			if id := s.identityOf(r.Context(), catalog.Agent{Command: req.Command, Adapter: req.Adapter, Env: req.Env}, path, true); id != nil {
				out["identity"] = id
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// checkAdapter rejects an agent whose adapter Conductor does not have.
func checkAdapter(a catalog.Agent) error {
	if err := agents.CheckAdapter(a.Adapter); err != nil {
		return fmt.Errorf("agent %s: %w", a.ID, err)
	}
	return nil
}

// keepMaskedEnv decides what the overlay stores for each env entry of a. A
// value of catalog.RedactedValue is what GET /api/catalog shows in place of
// every value, so a client that sends back what it read means "unchanged". If
// the overlay entry a replaces (saved) holds a value of its own for the key,
// that value is kept. Otherwise, if the agent a overrides (base) has the key,
// the mask itself is stored: ApplyOverlay reads it as base's value, so a
// change in the config reaches the agent. A masked key that neither has is an
// error. A value, sent or kept, equal to base's for the key is stored as the
// mask too, as ApplyOverlay reads it (New applies the same rule to what an
// earlier version saved). The first error by key name is reported, so the
// answer does not depend on map order.
func keepMaskedEnv(a *catalog.Agent, saved, base catalog.Agent) error {
	for _, k := range slices.Sorted(maps.Keys(a.Env)) {
		v := a.Env[k]
		bv, inBase := base.Env[k]
		if v == catalog.RedactedValue {
			sv, ok := saved.Env[k]
			switch {
			case ok && sv != catalog.RedactedValue:
				v = sv
			case !inBase:
				return fmt.Errorf("env %s: value is redacted; set a real value", k)
			}
		}
		if inBase && v == bv {
			v = catalog.RedactedValue // the base agent's value
		}
		a.Env[k] = v
	}
	return nil
}

// maskConfiguredEnv returns ov with every env value of an override equal to
// the value the configured agent it replaces (in base) has for that key set
// to catalog.RedactedValue, the rule keepMaskedEnv applies on save, and the
// IDs of the overrides it changed, each once (none when nothing changed, and
// then ov itself). ov is not modified.
func maskConfiguredEnv(ov catalog.Overlay, base catalog.Catalog) (catalog.Overlay, []string) {
	var ids []string
	var out []catalog.Agent
	for i, a := range ov.Agents {
		b, ok := base.Get(a.ID)
		if !ok {
			continue
		}
		var env map[string]string
		for k, v := range a.Env {
			if bv, inBase := b.Env[k]; inBase && v == bv && v != catalog.RedactedValue {
				if env == nil {
					env = maps.Clone(a.Env)
				}
				env[k] = catalog.RedactedValue
			}
		}
		if env == nil {
			continue
		}
		if out == nil {
			out = slices.Clone(ov.Agents)
		}
		out[i].Env = env
		if !slices.Contains(ids, a.ID) {
			ids = append(ids, a.ID)
		}
	}
	if ids == nil {
		return ov, nil
	}
	ov.Agents = out
	return ov, ids
}

// savedAgent returns the overlay's entry for id, the last one when a
// hand-edited file repeats it (the one that wins at startup), or the zero
// Agent.
func savedAgent(agents []catalog.Agent, id string) catalog.Agent {
	for i := len(agents) - 1; i >= 0; i-- {
		if agents[i].ID == id {
			return agents[i]
		}
	}
	return catalog.Agent{}
}

// commitOverlay makes ov the current overlay. It builds the catalog ov yields
// from the configured one and writes ov to catalog.json without catalogMu.
// Only then does it publish both, under catalogMu for that instant, so a
// failure leaves the running catalog and the file as they were. Deriving the
// catalog from base and the overlay, as a restart does, keeps the two
// identical. Agents is saved as [] rather than null. The caller holds
// catalogEditMu.
func (s *Server) commitOverlay(ov catalog.Overlay) error {
	next := s.base.Clone()
	if err := next.ApplyOverlay(ov); err != nil {
		return fmt.Errorf("apply overlay: %w", err)
	}
	if ov.Agents == nil {
		ov.Agents = []catalog.Agent{}
	}
	if err := s.writeCatalog(ov); err != nil {
		return fmt.Errorf("save %s: %w", catalogFile, err)
	}
	s.catalogMu.Lock()
	s.overlay, s.catalog = ov, next
	s.catalogMu.Unlock()
	return nil
}

// loadCatalog returns base with the overlay saved in the data directory applied,
// and that overlay. Without a store, or without a catalog.json yet, the overlay
// is empty. Every error is meant to be fatal: an overlay that cannot be read or
// that fails validation must not be replaced by a later save.
func loadCatalog(st *store.Store, base catalog.Catalog) (catalog.Catalog, catalog.Overlay, error) {
	effective := base.Clone()
	if st == nil {
		return effective, catalog.Overlay{}, nil
	}
	path := filepath.Join(st.Dir(), catalogFile)
	var ov catalog.Overlay
	ok, err := st.Load(catalogFile, &ov)
	if err != nil {
		return catalog.Catalog{}, catalog.Overlay{}, fmt.Errorf("%s: %w", path, err)
	}
	if !ok {
		return effective, catalog.Overlay{}, nil
	}
	for i, a := range ov.Agents {
		if err := checkAdapter(a); err != nil {
			return catalog.Catalog{}, catalog.Overlay{}, fmt.Errorf("%s: agents[%d]: %w", path, i, err)
		}
	}
	if err := effective.ApplyOverlay(ov); err != nil {
		return catalog.Catalog{}, catalog.Overlay{}, fmt.Errorf("%s: %w", path, err)
	}
	return effective, ov, nil
}

// upsertAgent returns agents with a in place of the entry with the same ID, or
// appended when there is none. Repeats of the ID, possible in a hand-edited
// file, collapse into a. The input is not modified.
func upsertAgent(agents []catalog.Agent, a catalog.Agent) []catalog.Agent {
	out := make([]catalog.Agent, 0, len(agents)+1)
	replaced := false
	for _, cur := range agents {
		switch {
		case cur.ID != a.ID:
			out = append(out, cur)
		case !replaced:
			out = append(out, a)
			replaced = true
		}
	}
	if !replaced {
		out = append(out, a)
	}
	return out
}

// withoutAgent returns a copy of agents without the entries for id.
func withoutAgent(agents []catalog.Agent, id string) []catalog.Agent {
	return slices.DeleteFunc(slices.Clone(agents), func(a catalog.Agent) bool { return a.ID == id })
}

// uniqueIDs returns a copy of ids without repeats, in order, never nil.
func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// withoutID returns a copy of ids without id.
func withoutID(ids []string, id string) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(other string) bool { return other == id })
}
