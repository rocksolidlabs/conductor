package api

import (
	"fmt"
	"net/http"

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
// one; 404 not_found.
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
	rc, err := ds.RekeyProject(ctx, body.op(r), key, body.Key)
	after := key
	if rc.Status == dispatch.StatusDone {
		after = body.Key
	}
	s.writeOp(w, "project", rc, err, false, "project", projectAfter(ctx, ds, after))
}
