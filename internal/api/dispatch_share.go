package api

import (
	"context"
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

// carriedScope is Brakeman's scope as a rekey's carry left it: the desk,
// the configuration it saved and the projects the scope named before.
type carriedScope struct {
	s      *Server
	d      *brakemanDesk
	saved  brakeman.Config
	before []string
	key    string
}

// carryScopeKey carries a project's new key into Brakeman's scope before
// the rekey: a scope that names the project's key names its new key
// instead, saved through the configuration's own save, so the rows held
// for the old key, applied once the key is free, are received under a
// key the scope does not name, and the person's grant follows their
// project. It carries nothing where the scope names no such key, for a
// repeat of a rekey already answered (its receipt held), and for a new
// key another project holds (the rekey refuses it); ok false when the
// save cannot be made, the rekey refused, answered here, 409
// scope_not_saved.
func (s *Server) carryScopeKey(w http.ResponseWriter, ctx context.Context, ds *dispatch.Store, op dispatch.Op, key, newKey string) (*carriedScope, bool) {
	d := s.brakeman
	if s.cfg.Switchyard.Enabled || d == nil || key == newKey {
		return nil, true
	}
	cur, err := d.st.Config()
	if err != nil {
		s.log.Warn("a rekey could not read Brakeman's configuration", "project", key, "err", err)
		writeError(w, http.StatusConflict, "scope_not_saved", fmt.Sprintf("Brakeman's configuration could not be read, so %s's key is unchanged: try again", key))
		return nil, false
	}
	if !slices.Contains(cur.Scope.Projects, key) {
		return nil, true
	}
	if _, found, err := ds.Receipt(ctx, op.Actor, op.ID); err == nil && found {
		return nil, true // a repeat: its receipt answers it, as it did
	}
	if _, err := ds.Project(ctx, newKey); err == nil {
		return nil, true // the key is another project's: the rekey refuses it
	}
	next := cur
	next.Scope.Projects = renamedKey(cur.Scope.Projects, key, newKey)
	saved, err := s.saveBrakemanConfig(d, next)
	if err != nil {
		s.log.Warn("a rekey could not carry the new key into Brakeman's scope", "project", key, "key", newKey, "err", err)
		writeError(w, http.StatusConflict, "scope_not_saved", fmt.Sprintf("Brakeman's scope names %s, and its configuration could not be saved with %s in its place, so the key is unchanged: try again", key, newKey))
		return nil, false
	}
	return &carriedScope{s: s, d: d, saved: saved, before: cur.Scope.Projects, key: key}, true
}

// putBack gives Brakeman's scope back the projects it named before a
// rekey that did not happen, when nothing has saved the configuration
// since; a later save is the person's, left as it is.
func (c *carriedScope) putBack() {
	cur, err := c.d.st.Config()
	if err != nil || cur.Rev != c.saved.Rev {
		c.s.log.Warn("Brakeman's scope keeps a rekey's new key: its configuration was saved again or could not be read", "project", c.key, "err", err)
		return
	}
	cur.Scope.Projects = c.before
	if _, err := c.s.saveBrakemanConfig(c.d, cur); err != nil {
		c.s.log.Warn("Brakeman's scope keeps a rekey's new key: it could not be saved back", "project", c.key, "err", err)
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
