package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A project's share and its rekey over HTTP (docs/plans/sync-seams.md,
// OS-1b): admin, each body at most maxShareBody bytes, unknown fields
// refused, answering {receipt, project}, a repeat answering the same
// receipt. A share needs a registered sync provider (409 no_sync): with
// none, this machine shares nothing.

// maxShareBody bounds a share's or a rekey's body.
const maxShareBody = 256

// shareBody is project.share's body; rekeyBody project.rekey's, key the
// project's new key.
type (
	shareBody = dispatchOp
	rekeyBody struct {
		dispatchOp
		Key string `json:"key"`
	}
)

// handleDispatchShareProject asks a project to be shared (project.share):
// {opId, byName?}; {receipt, project}; 409 no_sync with no sync provider
// registered, 409 archived, 409 numbers_out_of_range, 409 invalid for a
// share pending or done already; 404 not_found. The store's answers (503
// with none, or one that cannot be written) and the body's bound come
// before the provider's, as on every other write.
func (s *Server) handleDispatchShareProject(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok {
		return
	}
	var body shareBody
	if !readOp(w, r, &body, &body, maxShareBody) {
		return
	}
	if s.syncName() == "" {
		writeError(w, http.StatusConflict, "no_sync", "no sync provider is registered: this machine shares nothing")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.ShareProject(ctx, body.op(r), key)
	s.writeOp(w, "project", rc, err, false, "project", projectAfter(ctx, ds, key))
}

// handleDispatchRekeyProject gives a project another key (project.rekey):
// {opId, byName?, key}; {receipt, project}, the project read by its new
// key; 409 share_in_progress while a share is pending or completing, 409
// shared once shared, 409 key_taken, 400 invalid for a key that is not
// one; 404 not_found; 409 scope_not_saved when Brakeman's scope names the
// key and cannot be saved with the new key in its place (carryScopeKey).
func (s *Server) handleDispatchRekeyProject(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok {
		return
	}
	var body rekeyBody
	if !readOp(w, r, &body, &body.dispatchOp, maxShareBody) {
		return
	}
	if !dispatch.KeyPattern.MatchString(body.Key) {
		writeError(w, http.StatusBadRequest, dispatch.RefusalInvalid, fmt.Sprintf("the key %q is not a project's key: a capital letter, then one to five capitals or digits", body.Key))
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	op := body.op(r)
	carried, ok := s.carryScopeKey(w, ctx, ds, op, key, body.Key)
	if !ok {
		return
	}
	rc, err := ds.RekeyProject(ctx, op, key, body.Key)
	after := key
	if rc.Status == dispatch.StatusDone {
		after = body.Key
	}
	if carried != nil && !(err == nil && rc.Status == dispatch.StatusDone && !rc.Repeat) {
		carried.putBack()
	}
	s.writeOp(w, "project", rc, err, false, "project", projectAfter(ctx, ds, after))
}

// carriedScope is what a rekey's carry changed of Brakeman's: the desk,
// the keys, the configuration it saved (none when its scope named no such
// key) and the projects that scope named before, and the requests whose
// stored scope it renamed, with the newest request's number kept then.
type carriedScope struct {
	s           *Server
	d           *brakemanDesk
	key, newKey string
	config      bool
	saved       brakeman.Config
	before      []string
	requests    []string
	newest      int64
}

// carryScopeKey carries a project's new key into Brakeman's scopes before
// the rekey: the configuration's scope that names the project's key names
// its new key instead, saved through the configuration's own save, and so
// does the scope stored with every request Brakeman keeps, which an
// earlier brief is given whole by (Scope.Covers). The rows held for the
// old key, applied once the key is free, are then received under a key no
// scope of Brakeman's names, and the person's grant and their earlier
// briefs follow their project. It carries nothing for a repeat of a rekey
// already answered (its receipt held) and for a new key another project
// holds (the rekey refuses it); ok false when a save cannot be made, what
// was carried put back and the rekey refused, answered here, 409
// scope_not_saved.
func (s *Server) carryScopeKey(w http.ResponseWriter, ctx context.Context, ds *dispatch.Store, op dispatch.Op, key, newKey string) (*carriedScope, bool) {
	d := s.brakeman
	if s.cfg.Switchyard.Enabled || d == nil || key == newKey {
		return nil, true
	}
	refuse := func(err error, words string) (*carriedScope, bool) {
		s.log.Warn("a rekey could not carry the new key into Brakeman's scopes", "project", key, "key", newKey, "err", err)
		writeError(w, http.StatusConflict, "scope_not_saved", words)
		return nil, false
	}
	cur, err := d.st.Config()
	if err != nil {
		return refuse(err, fmt.Sprintf("Brakeman's configuration could not be read, so %s's key is unchanged: try again", key))
	}
	if _, found, err := ds.Receipt(ctx, op.Actor, op.ID); err == nil && found {
		return nil, true // a repeat: its receipt answers it, as it did
	}
	if _, err := ds.Project(ctx, newKey); err == nil {
		return nil, true // the key is another project's: the rekey refuses it
	}
	c := &carriedScope{s: s, d: d, key: key, newKey: newKey}
	if slices.Contains(cur.Scope.Projects, key) {
		next := cur
		next.Scope.Projects = renamedKey(cur.Scope.Projects, key, newKey)
		saved, err := s.saveBrakemanConfig(d, next)
		if err != nil {
			return refuse(err, fmt.Sprintf("Brakeman's scope names %s, and its configuration could not be saved with %s in its place, so the key is unchanged: try again", key, newKey))
		}
		c.config, c.saved, c.before = true, saved, cur.Scope.Projects
	}
	c.requests, c.newest, err = d.st.RenameProject(key, newKey, nil)
	if err != nil {
		c.putBack()
		return refuse(err, fmt.Sprintf("Brakeman's earlier requests could not be saved with %s in place of %s, so the key is unchanged: try again", newKey, key))
	}
	return c, true
}

// putBack undoes the carry of a rekey that did not happen. The
// configuration saved by the carry and not since gets back the projects it
// named; one saved again meanwhile is the person's, left as it is. The
// requests the carry renamed, and those made since (their scope taken from
// the carried configuration), get the inverse rename, the new key back to
// the old; only a save that cannot be made is logged.
func (c *carriedScope) putBack() {
	if c.config {
		c.configBack()
	}
	renamed := map[string]bool{}
	for _, id := range c.requests {
		renamed[id] = true
	}
	only := func(id string) bool { return renamed[id] || brakeman.RequestNumber(id) > c.newest }
	if _, _, err := c.d.st.RenameProject(c.newKey, c.key, only); err != nil {
		c.s.log.Warn("Brakeman's earlier requests keep a rekey's new key: they could not be saved back", "project", c.key, "key", c.newKey, "err", err)
	}
}

// configBack gives the configuration back its scope (putBack), a few
// tries over saves made meanwhile.
func (c *carriedScope) configBack() {
	var err error
	for range 3 {
		var cur brakeman.Config
		if cur, err = c.d.st.Config(); err != nil {
			break
		}
		switch {
		case cur.Rev == c.saved.Rev:
			cur.Scope.Projects = c.before
		default:
			return
		}
		if _, err = c.s.saveBrakemanConfig(c.d, cur); err == nil || !errors.Is(err, brakeman.ErrStale) {
			break
		}
	}
	if err != nil {
		c.s.log.Warn("Brakeman's scope keeps a rekey's new key: it could not be saved back", "project", c.key, "key", c.newKey, "err", err)
	}
}

// renamedKey is projects with key in place of newKey, each once.
func renamedKey(projects []string, key, newKey string) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		if p == key {
			p = newKey
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}
