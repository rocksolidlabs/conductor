package api

import "net/http"

// handleBrakemanSince answers GET /api/brakeman/since: Conductor's own
// counts since the last briefing kept.
func (s *Server) handleBrakemanSince(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "this build does not answer the since read yet")
}
