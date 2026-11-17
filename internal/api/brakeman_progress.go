package api

import "time"

// Brakeman's progress while a turn runs: what the turn in progress has
// read of its budget and what it read last, kept by the desk in memory
// only and answered on GET /api/brakeman (progress) and on the events
// stream (brakeman_progress). Never kept in a record.

// brakemanProgress is GET /api/brakeman's progress, and the
// brakeman_progress event's data.
type brakemanProgress struct {
	ID           string              `json:"id"`
	Reads        int                 `json:"reads"`
	ReadsPerTurn int                 `json:"readsPerTurn"`
	StartedAt    *time.Time          `json:"startedAt,omitempty"`
	Deadline     *time.Time          `json:"deadline,omitempty"`
	Reading      *brakemanReadingNow `json:"reading,omitempty"`
}

// brakemanReadingNow is the read a turn made last: what it read (an
// observe operation's what) and the ref it named, when it named one.
type brakemanReadingNow struct {
	What string `json:"what"`
	Ref  string `json:"ref,omitempty"`
}

// brakemanLive is the desk's view of the turn in progress's reads.
type brakemanLive struct{}

// liveState fills GET /api/brakeman's progress and nextBriefing from the
// desk's live view; it fills nothing yet.
func (s *Server) liveState(d *brakemanDesk, out *brakemanState) {
	_ = &d.live
}
