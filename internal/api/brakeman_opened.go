package api

import "net/http"

// handleBrakemanOpened answers GET /api/brakeman/opened: the caller's
// briefs not yet opened.
func (s *Server) handleBrakemanOpened(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "this build does not keep opened marks yet")
}

// handleBrakemanMarkOpened answers POST /api/brakeman/opened: the caller
// marks briefs opened.
func (s *Server) handleBrakemanMarkOpened(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "this build does not keep opened marks yet")
}
