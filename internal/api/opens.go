package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// An agent's links, opened in the browser its settings name (#436): the
// agent asks with its session's token (conductor open, the MCP tool
// open_url, its $BROWSER), the server tells the workbenches over their event
// stream, and the desktop app, which alone starts a browser, claims the open
// and starts the browser on the computer it runs on. A workbench in a plain
// browser tab starts nothing: once no desktop app has claimed the open, it
// offers the link in a toast. docs/protocol.md, "Opening a link".
const (
	// selfOpensPerMinute bounds the links a session opens.
	selfOpensPerMinute = 10
	// MaxOpenURL bounds a link opened, in bytes.
	MaxOpenURL = 2048
	// maxOpenBody bounds the bodies of the open routes: a link of
	// MaxOpenURL bytes, each escaped in six, and the keys.
	maxOpenBody = 6*MaxOpenURL + 256
	// maxPendingOpens bounds the opens waiting for a desktop app across
	// the server; each session's are bounded by selfOpensPerMinute.
	maxPendingOpens = 64
	// maxOpenEvent bounds an open event's data: the link, each byte
	// escaped in six at most, a browser's name and profile (at most 100
	// characters of four bytes, escaped in six), the ids and the keys.
	maxOpenEvent = 6*MaxOpenURL + 6*4*catalog.MaxBrowserProfile + 512
	// openClaimWait is how long an open waits for a desktop app to claim
	// it before it is offered in the workbench: the event's way to the app
	// and the app's claim back, on a machine that may be loaded.
	openClaimWait = 3 * time.Second
	// openResultWait is how long an open claimed waits for the app to say
	// what became of it: the browser's start, which the app waits on.
	openResultWait = 15 * time.Second
)

// The states of an open event.
const (
	openPending = "pending" // for a desktop app to claim
	openOffered = "offered" // no desktop app claimed it: a workbench offers it
)

// The outcomes of an open: what the desktop app did (the first four), or
// that it was offered in the workbench.
const (
	OpenedBrowser = "browser" // in the browser the agent's settings name
	OpenedDefault = "default" // in the default browser: the agent names none
	OpenedMissing = "missing" // in the default browser: the one named is not installed on that computer
	OpenedFailed  = "failed"  // in the default browser: the one named did not start
	OpenOffered   = "offered" // in the workbench: no desktop app took it
	OpenHanded    = "handed"  // to a desktop app, which has not said what became of it
)

// openEvent is the data of an `open` event.
type openEvent struct {
	ID        string           `json:"id"`
	SessionID string           `json:"sessionId"`
	URL       string           `json:"url"`
	Browser   *catalog.Browser `json:"browser,omitempty"`
	State     string           `json:"state"`
}

// openRequest is the body of POST /api/sessions/{id}/open.
type openRequest struct {
	URL string `json:"url"`
}

// openResult is the body of POST /api/opens/{id}/result: what the desktop
// app did with an open it claimed.
type openResult struct {
	Outcome string `json:"outcome"`
}

// openReply is the answer of POST /api/sessions/{id}/open.
type openReply struct {
	Outcome string           `json:"outcome"`
	Browser *catalog.Browser `json:"browser,omitempty"`
	Message string           `json:"message"`
}

// pendingOpen is an open waiting for a desktop app.
type pendingOpen struct {
	ev      openEvent
	claimed bool
	offered bool
	// claim is closed when a desktop app claims the open; result takes
	// what it did, once.
	claim  chan struct{}
	result chan string
	told   bool
}

// openDesk holds the opens waiting for a desktop app.
type openDesk struct {
	mu      sync.Mutex
	pending map[string]*pendingOpen
	// claimWait and resultWait are openClaimWait and openResultWait; a
	// test sets its own.
	claimWait, resultWait time.Duration
}

func newOpenDesk() *openDesk {
	return &openDesk{pending: map[string]*pendingOpen{}, claimWait: openClaimWait, resultWait: openResultWait}
}

var errTooManyOpens = errors.New("too many links wait for a desktop app")

// add records an open; errTooManyOpens past maxPendingOpens.
func (d *openDesk) add(ev openEvent) (*pendingOpen, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) >= maxPendingOpens {
		return nil, errTooManyOpens
	}
	p := &pendingOpen{ev: ev, claim: make(chan struct{}), result: make(chan string, 1)}
	d.pending[ev.ID] = p
	return p, nil
}

func (d *openDesk) remove(id string) {
	d.mu.Lock()
	delete(d.pending, id)
	d.mu.Unlock()
}

// offerUnclaimed marks p offered unless a desktop app claimed it first, and
// reports whether it did.
func (d *openDesk) offerUnclaimed(p *pendingOpen) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p.claimed {
		return false
	}
	p.offered = true
	return true
}

// take claims the open id for the desktop app asking: the first claim of
// an open not yet offered has it.
func (d *openDesk) take(id string) (openEvent, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.pending[id]
	switch {
	case !ok:
		return openEvent{}, http.StatusNotFound
	case p.claimed || p.offered:
		return openEvent{}, http.StatusConflict
	}
	p.claimed = true
	close(p.claim)
	return p.ev, http.StatusOK
}

// tell takes what the desktop app did with the open id it claimed, once.
func (d *openDesk) tell(id, outcome string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.pending[id]
	switch {
	case !ok:
		return http.StatusNotFound
	case !p.claimed || p.told:
		return http.StatusConflict
	}
	p.told = true
	p.result <- outcome
	return http.StatusOK
}

// checkOpenURL accepts an http: or https: URL with a host, of at most
// MaxOpenURL bytes, in UTF-8, with no white space, quote or control
// character.
func checkOpenURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > MaxOpenURL {
		return errors.New("url is longer than 2048 bytes")
	}
	if !utf8.ValidString(raw) || strings.ContainsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\'' || r == '`'
	}) {
		return errors.New("url must have no white space, quote or control character")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an http:// or https:// URL with a host")
	}
	return nil
}

// openWords is what the tool and the activity entry say of an outcome.
func openWords(outcome string, b *catalog.Browser) string {
	switch outcome {
	case OpenedBrowser:
		return "opened in " + browserWords(b)
	case OpenedDefault:
		return "opened in the default browser"
	case OpenedMissing:
		return "opened in the default browser: " + b.Name + " is not installed on that computer"
	case OpenedFailed:
		return "opened in the default browser: " + b.Name + " did not start"
	case OpenHanded:
		return "handed to the desktop app, which has not said what became of it"
	}
	return "offered in the workbench: no desktop app took it"
}

// browserWords names a browser and its profile.
func browserWords(b *catalog.Browser) string {
	if b == nil || b.Name == "" {
		return "the default browser"
	}
	if b.Profile != "" {
		return b.Name + " (" + b.Profile + ")"
	}
	return b.Name
}

// handleSelfOpen opens a link in the browser the session's agent's settings
// name: POST /api/sessions/{id}/open {url}, 200 {outcome, browser, message}.
// It takes the session's agent token or the workbench token, refuses a
// hosted session, follows agents.selfService and opens selfOpensPerMinute a
// minute, then 429 rate_limited. A managed run's attempt is never opened at
// once, nobody may be watching: its links are offered. Each open is
// recorded in the session's activity as an artifact entry with its URL and
// what became of it.
func (s *Server) handleSelfOpen(w http.ResponseWriter, r *http.Request) {
	local, ok := s.selfSession(w, r)
	if !ok {
		return
	}
	var in openRequest
	if err := decodeJSONLimit(w, r, &in, maxOpenBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := checkOpenURL(in.URL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	info := local.Info()
	if !s.selfLimits.take(s.selfLimits.opens, info.ID, selfOpensPerMinute, time.Minute) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "this session has opened its links for the minute")
		return
	}
	var browser *catalog.Browser
	if a, ok := s.Catalog().Get(info.AgentID); ok && a.Browser.Named() {
		b := *a.Browser
		browser = &b
	}
	managed := info.Crew != nil && info.Crew.Task != ""
	outcome, err := s.open(r.Context(), info.ID, in.URL, browser, managed)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	msg := openWords(outcome, browser)
	local.Record(session.ActivityEntry{Type: session.ActivityArtifact, Message: "link " + msg, URL: in.URL})
	writeJSON(w, http.StatusOK, openReply{Outcome: outcome, Browser: browser, Message: msg})
}

// open tells the workbenches of the link and waits for a desktop app to
// claim it and say what it did; unclaimed, or managed, it is offered.
func (s *Server) open(ctx context.Context, sessionID, link string, browser *catalog.Browser, managed bool) (string, error) {
	ev := openEvent{ID: "op-" + strings.ToLower(rand.Text()[:16]), SessionID: sessionID, URL: link, Browser: browser, State: openPending}
	if managed {
		ev.State = openOffered
		s.events.open(ev)
		return OpenOffered, nil
	}
	p, err := s.opens.add(ev)
	if err != nil {
		return "", err
	}
	defer s.opens.remove(ev.ID)
	s.events.open(ev)
	claimWait := time.NewTimer(s.opens.claimWait)
	defer claimWait.Stop()
	select {
	case <-p.claim:
	case <-claimWait.C:
	case <-ctx.Done():
	}
	if s.opens.offerUnclaimed(p) {
		ev.State = openOffered
		s.events.open(ev)
		return OpenOffered, nil
	}
	resultWait := time.NewTimer(s.opens.resultWait)
	defer resultWait.Stop()
	select {
	case outcome := <-p.result:
		return outcome, nil
	case <-resultWait.C:
	case <-ctx.Done():
	}
	return OpenHanded, nil
}

// handleClaimOpen is a desktop app's claim of an open: POST
// /api/opens/{id}/claim, the workbench's token. 200 {open} for the first
// claim while the open waits, 409 open_taken after it or once the open was
// offered, 404 for an open the server no longer holds.
func (s *Server) handleClaimOpen(w http.ResponseWriter, r *http.Request) {
	ev, status := s.opens.take(r.PathValue("id"))
	switch status {
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]any{"open": ev})
	case http.StatusConflict:
		writeError(w, status, "open_taken", "another desktop app took this link, or it was offered in the workbench")
	default:
		writeError(w, http.StatusNotFound, "not_found", "no such link waits to be opened")
	}
}

// handleOpenResult is what the desktop app did with an open it claimed:
// POST /api/opens/{id}/result {outcome}, the workbench's token; once.
func (s *Server) handleOpenResult(w http.ResponseWriter, r *http.Request) {
	var in openResult
	if err := decodeJSONLimit(w, r, &in, 1<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch in.Outcome {
	case OpenedBrowser, OpenedDefault, OpenedMissing, OpenedFailed:
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "outcome must be browser, default, missing or failed")
		return
	}
	switch s.opens.tell(r.PathValue("id"), in.Outcome) {
	case http.StatusOK:
		w.WriteHeader(http.StatusNoContent)
	case http.StatusConflict:
		writeError(w, http.StatusConflict, "open_taken", "the link was not claimed, or its outcome was told already")
	default:
		writeError(w, http.StatusNotFound, "not_found", "no such link waits to be opened")
	}
}

// open queues an open event for every client, as droppable as activity: a
// client past activityQueueLimit misses it and keeps its stream (a missed
// pending one is offered; a missed offer is in the activity). An event past
// maxOpenEvent is dropped; none is, the bound being the longest one's.
func (h *eventHub) open(ev openEvent) {
	b, err := json.Marshal(ev)
	if err != nil || len(b) > maxOpenEvent {
		return
	}
	msg := []byte("event: open\ndata: " + string(b) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		if len(ch) >= activityQueueLimit {
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}
