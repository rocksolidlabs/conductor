package api

import (
	"errors"
	"net/http"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
)

// Brakeman's opened marks (BKUI, #993): the briefs a person has not yet
// opened, kept by the server for each credential's actor (actorOf: the
// workbench token's "admin", an identity provider's "user:<subject>"),
// so every browser holding one credential shows one count. The actor is
// always the caller's own, taken from its credential, never from a body
// or a query; neither an answer nor the event names another actor's
// marks. Only the server that owns Brakeman's records keeps them: one
// that does not answers 503 brakeman_not_owner, and the app keeps its
// own marks.

// The opened routes' bounds.
const (
	// maxOpenedBody bounds POST /api/brakeman/opened's body.
	maxOpenedBody = 4 << 10
	// maxBrakemanOpenedEvent bounds a brakeman_opened event's data.
	maxBrakemanOpenedEvent = 32
)

// brakemanOpenedBody is POST /api/brakeman/opened's body: the requests
// marked opened, 1 to brakeman.MaxOpenedMark ids.
type brakemanOpenedBody struct {
	IDs []string `json:"ids"`
}

// handleBrakemanOpened answers GET /api/brakeman/opened: {unread, begun},
// the caller's unread briefs newest first, at most brakeman.MaxOpenedIDs,
// and when its count began (its first read).
func (s *Server) handleBrakemanOpened(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok || !noQuery(w, r) {
		return
	}
	u, err := d.st.Opened(actorOf(r))
	if err != nil {
		s.brakemanOpenedError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleBrakemanMarkOpened answers POST /api/brakeman/opened {ids}: the
// ids marked opened for the caller (a set union: the same ids again
// change nothing, so no op id), answered as the GET is. A change sends
// the brakeman_opened event, which names no one.
func (s *Server) handleBrakemanMarkOpened(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok || !noQuery(w, r) {
		return
	}
	var body brakemanOpenedBody
	if err := decodeJSONLimit(w, r, &body, maxOpenedBody); err != nil {
		msg := "the body is {ids}: 1 to 100 request ids"
		if errors.Is(err, errBodyTooLarge) {
			msg = "the body is at most 4 KiB"
		}
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	u, changed, err := d.st.MarkOpened(actorOf(r), body.IDs)
	if err != nil {
		s.brakemanOpenedError(w, err)
		return
	}
	if changed {
		s.events.brakemanOpened()
	}
	writeJSON(w, http.StatusOK, u)
}

// brakemanOpenedError answers an opened route's error: a document that
// does not read is the store's failure, said without the caller's actor.
func (s *Server) brakemanOpenedError(w http.ResponseWriter, err error) {
	if errors.Is(err, brakeman.ErrOpenedDamaged) {
		s.log.Warn("Brakeman's opened marks could not be read: kept as they are", "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "Brakeman's opened marks could not be read")
		return
	}
	s.brakemanError(w, err)
}

// brakemanOpened tells every admin page that opened marks changed: data
// {}, naming no actor and no request, so a page reads its own count
// again and learns nothing of anyone else's reading.
func (h *eventHub) brakemanOpened() {
	const data = "{}"
	if len(data) > maxBrakemanOpenedEvent {
		return
	}
	h.broadcast([]byte("event: brakeman_opened\ndata: " + data + "\n\n"))
}
