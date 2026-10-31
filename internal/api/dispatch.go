package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Dispatch over HTTP (docs/plans/dispatch.md): the projects with their
// labels and milestones, the people, the tickets with their comments,
// relations, links and attachments (dispatch_attachments.go), their plan
// revisions (dispatch_plans.go, docs/plans/dispatch-evidence.md), the saved
// views, the ticket a run is linked to, and the receipts. Every route is
// the admin's (Owner decisions, 17:
// share-link viewers see no ticket) and a switchyard's 403 (launching),
// since a switchyard keeps no tickets. Every write is one of the store's
// typed operations under the actor admin (dispatch.Store.Apply): its body
// carries the operation's id, which the client mints, the revision of the
// entity it expects and the display name kept as by, beside the
// operation's fields, and the answer is the receipt with the entity as it
// stands after it, so a reply lost to the network is asked for again with
// the same id and answered without a second change. Replies are written
// with HTML escaping off (writeDispatch), so a text field encodes to at
// most twice its bytes and the largest reply is bounded by its shape
// (MaxTicketReply).

const (
	// maxTicketBody bounds the body of a ticket's create and edit: every
	// field at its bound with each character doubled by JSON escaping (the
	// Markdown fields allow \n and \t alone of the control characters, so a
	// browser's JSON.stringify at most doubles them, and the CLI encodes
	// with HTML escaping off), with room for the rest. A field past its own
	// bound once decoded is the store's bound refusal; a body past this is
	// 413 bound.
	maxTicketBody = 192 << 10
	// MaxTicketReply bounds the replies that carry a ticket's comments: a
	// ticket's read (and an operation's on it, the ticket beside a receipt
	// of at most 4 KiB, and a session's read of its run's ticket, the
	// ticket beside its labels and its milestone) and a page of its
	// comments. The largest ticket the server can make, every field and set
	// at its bound and every text at its escaped worst (fifty comments at
	// 32 KiB, the description at 128 KiB, fifty links, fifty attachments, a
	// hundred relations and fifty children with their titles), is under it,
	// and so is the largest page of comments, maxCommentPage of them at
	// their escaped worst. dispatch_test.go builds both and measures their
	// replies. Exported for the CLI, whose limits are derived from it
	// (internal/cli: maxDispatchReply, and the session client's).
	MaxTicketReply = 3 << 20
	// MaxProjectReply bounds a project's read: its fields at their bounds,
	// 100 labels, 100 milestones with 4 KiB descriptions and 200 views with
	// 8 KiB queries, every text at its escaped worst, is under it, which
	// dispatch_test.go measures as it measures MaxTicketReply. Exported
	// for the CLI, which reads a project for its labels' and milestones'
	// names (internal/cli: maxDispatchReply).
	MaxProjectReply = 3 << 20
	// maxProjectsPage bounds a page of the projects: 200 summaries, none
	// with its description, folder, tracker URL or close policy, a name of
	// 60 characters encoding to at most 360 bytes.
	maxProjectsPage = 128 << 10
	// dispatchWriteTimeout bounds an operation past its request's end. An
	// operation is one transaction, which waits at most the store's busy
	// timeout for another writer's lock: this bounds only a hang.
	dispatchWriteTimeout = 30 * time.Second
	// maxCommentPage bounds a page of a ticket's comments, and is the page
	// when a read names none: as many as the ticket's read carries
	// (dispatch.MaxTicketComments), so the largest page, every comment at
	// its escaped worst, is under MaxTicketReply as the read is. The
	// store's own bound, dispatch.MaxCommentPage (200), would let a page
	// of comments of 32 KiB each pass twice MaxTicketReply.
	maxCommentPage = dispatch.MaxTicketComments
)

// dispatchRoute is one of Dispatch's routes: its pattern and its handler.
type dispatchRoute struct {
	pattern string
	h       http.HandlerFunc
}

// routeDispatch adds Dispatch's routes, every one the admin's and a
// switchyard's 403 (launching), and the two of an attempt's ticket, which
// take the session's own token too (dispatch_session.go) and are a
// switchyard's 403 as well (launchingAny).
func (s *Server) routeDispatch(mux *http.ServeMux) {
	for _, r := range s.dispatchRoutes() {
		mux.HandleFunc(r.pattern, s.launching(r.h))
	}
	mux.HandleFunc("GET /api/sessions/{id}/ticket", s.launchingAny(s.handleSessionTicket))
	mux.HandleFunc("POST /api/sessions/{id}/ticket/comments", s.launchingAny(s.handleSessionTicketComment))
}

// dispatchRoutes is Dispatch's routes, as routeDispatch adds them.
func (s *Server) dispatchRoutes() []dispatchRoute {
	return []dispatchRoute{
		{"GET /api/dispatch/projects", s.handleDispatchProjects},
		{"POST /api/dispatch/projects", s.handleDispatchCreateProject},
		{"GET /api/dispatch/projects/{key}", s.handleDispatchProject},
		{"POST /api/dispatch/projects/{key}/edit", s.handleDispatchEditProject},
		{"POST /api/dispatch/projects/{key}/archive", s.handleDispatchArchiveProject},
		{"POST /api/dispatch/projects/{key}/unarchive", s.handleDispatchUnarchiveProject},
		{"POST /api/dispatch/projects/{key}/labels", s.handleDispatchCreateLabel},
		{"POST /api/dispatch/projects/{key}/labels/{id}/edit", s.handleDispatchEditLabel},
		{"POST /api/dispatch/projects/{key}/labels/{id}/delete", s.handleDispatchDeleteLabel},
		{"POST /api/dispatch/projects/{key}/milestones", s.handleDispatchCreateMilestone},
		{"POST /api/dispatch/projects/{key}/milestones/{id}/edit", s.handleDispatchEditMilestone},
		{"POST /api/dispatch/projects/{key}/milestones/{id}/delete", s.handleDispatchDeleteMilestone},
		{"GET /api/dispatch/people", s.handleDispatchPeople},
		{"POST /api/dispatch/people", s.handleDispatchAddPerson},
		{"GET /api/dispatch/tickets", s.handleDispatchTickets},
		{"POST /api/dispatch/tickets", s.handleDispatchCreateTicket},
		{"GET /api/dispatch/tickets/{key}", s.handleDispatchTicket},
		{"POST /api/dispatch/tickets/{key}/edit", s.handleDispatchEditTicket},
		{"POST /api/dispatch/tickets/{key}/accept", s.handleDispatchAcceptTicket},
		{"POST /api/dispatch/tickets/{key}/unaccept", s.handleDispatchUnacceptTicket},
		{"POST /api/dispatch/tickets/{key}/delete", s.handleDispatchDeleteTicket},
		{"POST /api/dispatch/tickets/{key}/undelete", s.handleDispatchUndeleteTicket},
		{"GET /api/dispatch/tickets/{key}/history", s.handleDispatchHistory},
		{"GET /api/dispatch/tickets/{key}/plans", s.handleDispatchPlans},
		{"POST /api/dispatch/tickets/{key}/plans", s.handleDispatchProposePlan},
		{"GET /api/dispatch/plans/{id}", s.handleDispatchPlan},
		{"POST /api/dispatch/plans/{id}/approve", s.handleDispatchApprovePlan},
		{"POST /api/dispatch/plans/{id}/withdraw", s.handleDispatchWithdrawPlan},
		{"GET /api/dispatch/tickets/{key}/comments", s.handleDispatchComments},
		{"POST /api/dispatch/tickets/{key}/comments", s.handleDispatchAddComment},
		{"POST /api/dispatch/tickets/{key}/comments/{id}/edit", s.handleDispatchEditComment},
		{"POST /api/dispatch/tickets/{key}/comments/{id}/delete", s.handleDispatchDeleteComment},
		{"POST /api/dispatch/tickets/{key}/relations", s.handleDispatchAddRelation},
		{"POST /api/dispatch/tickets/{key}/relations/{id}/delete", s.handleDispatchRemoveRelation},
		{"POST /api/dispatch/tickets/{key}/links", s.handleDispatchAddLink},
		{"POST /api/dispatch/tickets/{key}/links/{id}/delete", s.handleDispatchRemoveLink},
		{"POST /api/dispatch/tickets/{key}/attachments", s.handleDispatchAddAttachment},
		{"POST /api/dispatch/tickets/{key}/attachments/{id}/delete", s.handleDispatchRemoveAttachment},
		{"GET /api/dispatch/attachments/{id}", s.handleDispatchAttachment},
		{"GET /api/dispatch/links", s.handleDispatchRunTicket},
		{"GET /api/dispatch/views", s.handleDispatchViews},
		{"POST /api/dispatch/views", s.handleDispatchCreateView},
		{"POST /api/dispatch/views/{id}/edit", s.handleDispatchEditView},
		{"POST /api/dispatch/views/{id}/delete", s.handleDispatchDeleteView},
		{"GET /api/dispatch/ops/{opId}", s.handleDispatchOp},
	}
}

// openDispatch opens Dispatch's store under the data directory's
// dispatch/ (dispatch.Open), which the file routes never reach (the data
// directory is refused to them: fileDeny). A directory that cannot be
// made, or a database file that is a symbolic link, is logged and leaves
// the server without the store: the Dispatch routes answer 503
// dispatch_unavailable. A store that opens read-only (a newer Conductor
// wrote it, it fails its check, another writer held it past the busy
// timeout) is logged with why: its reads answer as they can and its
// writes are 503 dispatch_unavailable with why. What the store's sweep of
// its attachments' files at open could not delete is logged too: it is
// left for the next sweep, and the store serves regardless.
func openDispatch(st *store.Store, log logger) *dispatch.Store {
	ds, err := dispatch.Open(filepath.Join(st.Dir(), "dispatch"))
	if err != nil {
		log.Warn("Dispatch is off: its store could not be opened", "err", err)
		return nil
	}
	if ro, why := ds.ReadOnly(); ro {
		log.Warn("Dispatch is read-only: its writes are refused", "why", why)
	}
	if err := ds.SweepError(); err != nil {
		log.Warn("Dispatch's sweep of its attachments' files left some for the next", "err", err)
	}
	return ds
}

// runKnown reports whether a run with the id is one the engine holds or
// has a record of: what a Dispatch run link may name (the store's
// RunExists). It runs inside a Dispatch operation, the store's lock held,
// so it reads the engine and the records and never the store.
func (s *Server) runKnown(id string) bool {
	if !crew.ValidRunID(id) {
		return false
	}
	if _, ok := s.runs.Get(id); ok {
		return true
	}
	if s.records == nil {
		return false
	}
	_, ok, err := s.records.Get(id)
	if err != nil {
		s.log.Warn("a run's record could not be read for a Dispatch link", "run", id, "err", err)
	}
	return ok
}

// dispatchStore gives the store for a read, or answers why there is none:
// 503 store_unavailable without a data directory, 503
// dispatch_unavailable when it could not be opened (the server's log says
// why). When it reports false it has answered.
func (s *Server) dispatchStore(w http.ResponseWriter) (*dispatch.Store, bool) {
	switch {
	case s.dispatch != nil:
		return s.dispatch, true
	case s.store == nil:
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
	default:
		writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Dispatch's store could not be opened; see the server's log")
	}
	return nil, false
}

// dispatchWriter is dispatchStore for a write: a store opened read-only
// answers 503 dispatch_unavailable with why, and its reads go on.
func (s *Server) dispatchWriter(w http.ResponseWriter) (*dispatch.Store, bool) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return nil, false
	}
	if ro, why := ds.ReadOnly(); ro {
		writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Dispatch is read-only: "+why)
		return nil, false
	}
	return ds, true
}

// dispatchOp is what every Dispatch write takes beside its fields
// (dispatch.Op): the operation's id, which the client mints, 1 to 32 of
// letters, digits, '.', '_', '~' and '-'; the revision of the entity it
// acts on that the client expects, 0 for none (stale_revision once the
// entity's has moved on); and the display name the store records as by,
// which travels in the body as a broadcast's byName does, never in the
// query string. The admin's operations are kept under the actor admin.
type dispatchOp struct {
	OpID        string `json:"opId"`
	ExpectedRev int64  `json:"expectedRev,omitempty"`
	ByName      string `json:"byName,omitempty"`
}

// op is the store's operation of the admin's.
func (b dispatchOp) op() dispatch.Op {
	return dispatch.Op{Actor: adminActor, ID: b.OpID, ExpectedRev: b.ExpectedRev, By: b.ByName}
}

// The bodies of the writes: the operation and the fields its intent takes.
type (
	projectCreateBody struct {
		dispatchOp
		dispatch.NewProject
	}
	projectEditBody struct {
		dispatchOp
		dispatch.ProjectEdit
	}
	personBody struct {
		dispatchOp
		Name string `json:"name"`
	}
	ticketCreateBody struct {
		dispatchOp
		dispatch.NewTicket
	}
	ticketEditBody struct {
		dispatchOp
		dispatch.TicketEdit
	}
	acceptBody struct {
		dispatchOp
		Note string `json:"note,omitempty"`
	}
	commentBody struct {
		dispatchOp
		Body string `json:"body"`
	}
	labelCreateBody struct {
		dispatchOp
		dispatch.NewLabel
	}
	labelEditBody struct {
		dispatchOp
		dispatch.LabelEdit
	}
	milestoneCreateBody struct {
		dispatchOp
		dispatch.NewMilestone
	}
	milestoneEditBody struct {
		dispatchOp
		dispatch.MilestoneEdit
	}
	relationBody struct {
		dispatchOp
		Kind string `json:"kind"`
		To   string `json:"to"`
	}
	linkBody struct {
		dispatchOp
		dispatch.NewLink
	}
	// A view's query travels as the JSON it is, an object, never as a
	// string of it (viewQuery).
	viewCreateBody struct {
		dispatchOp
		Project string          `json:"project,omitempty"`
		Name    string          `json:"name"`
		Kind    string          `json:"kind"`
		Query   json.RawMessage `json:"query"`
	}
	viewEditBody struct {
		dispatchOp
		Name  *string         `json:"name,omitempty"`
		Kind  *string         `json:"kind,omitempty"`
		Query json.RawMessage `json:"query,omitempty"`
	}
)

// readOp decodes a write's body into v, at most limit bytes, and checks
// its operation, op being v's: 413 bound past the limit, 400
// invalid_request for a body that is not one JSON object of v's fields
// and no other, an op id that is not one, or a revision below 0. When it
// reports false it has answered.
func readOp(w http.ResponseWriter, r *http.Request, v any, op *dispatchOp, limit int64) bool {
	if err := decodeJSONLimit(w, r, v, limit); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", limit>>10))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	if !dispatch.OpIDPattern.MatchString(op.OpID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
		return false
	}
	if op.ExpectedRev < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "expectedRev is the entity's revision, or 0 for none")
		return false
	}
	return true
}

// opContext is an operation's context: the request's values, not its
// end, so a client gone mid-operation leaves it whole (its resend is
// answered by the receipt), bounded by dispatchWriteTimeout.
func opContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), dispatchWriteTimeout)
}

// writeDispatch answers v as JSON with HTML escaping off, as the store
// writes its values: '<', '>' and '&' stay one byte each where the default
// encoder writes six, so a text field encodes to at most twice its bytes
// (U+2028 and U+2029, three bytes each, are written as six whatever the
// escaping), which the reply bounds are measured at.
func writeDispatch(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeOp answers an operation (rc, or err when the store took nothing).
// Done, now or before (receipt.repeat): 200 {receipt, <name>}, the entity
// as read after it (read: null when there is nothing to read, a create's
// whose receipt was pruned among them), 201 for a create made now.
// Refused, its failed receipt kept so the same op id answers it again:
// 404 {error, receipt} for not_found (every write on a deleted ticket but
// its undelete among it), 503 dispatch_unavailable for read_only, and 409
// {error, receipt} for every other refusal, the refusal its code. An
// error as dispatchError answers it.
func (s *Server) writeOp(w http.ResponseWriter, what string, rc dispatch.Receipt, err error, create bool, name string, read func(dispatch.Receipt) (any, error)) {
	if err != nil {
		s.dispatchError(w, what, err)
		return
	}
	if rc.Status != dispatch.StatusDone {
		status := http.StatusConflict
		switch rc.Refusal {
		case dispatch.RefusalNotFound:
			status = http.StatusNotFound
		case dispatch.RefusalReadOnly:
			writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Dispatch is read-only: "+rc.Error)
			return
		}
		writeDispatch(w, status, map[string]any{"error": apiError{Code: rc.Refusal, Message: rc.Error}, "receipt": rc})
		return
	}
	entity, err := read(rc)
	if err != nil {
		// Done all the same: the receipt answers, and the next read shows
		// the entity.
		s.log.Warn("Dispatch could not read what an operation changed", "intent", rc.Intent, "err", err)
	}
	status := http.StatusOK
	if create && !rc.Repeat {
		status = http.StatusCreated
	}
	writeDispatch(w, status, map[string]any{"receipt": rc, name: entity})
}

// dispatchError answers an error of the store's, which took nothing: 400
// invalid_request for what it would not look at (an op id, actor or
// display name that is not one, a query outside its vocabulary or past
// its bound), 404 not_found for what it does not hold, 503
// dispatch_unavailable for a store that cannot serve (nothing to read,
// closed), and 500 store_failed, logged, for a failure of the database.
func (s *Server) dispatchError(w http.ResponseWriter, what string, err error) {
	words := strings.TrimPrefix(err.Error(), "dispatch: ")
	switch {
	case errors.Is(err, dispatch.ErrBadOpID), errors.Is(err, dispatch.ErrBadActor), errors.Is(err, dispatch.ErrBadBy), errors.Is(err, dispatch.ErrBadQuery):
		writeError(w, http.StatusBadRequest, "invalid_request", words)
	case errors.Is(err, dispatch.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such "+what)
	case errors.Is(err, dispatch.ErrUnavailable), errors.Is(err, dispatch.ErrClosed), errors.Is(err, dispatch.ErrReadOnly):
		writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", words)
	default:
		// A read the client left is no failure of the store's.
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("Dispatch failed", "what", what, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "store_failed", "Dispatch's store failed; see the server's log")
	}
}

// projectKey reads a project route's key: a project's (CND), 400
// invalid_request otherwise. When it reports false it has answered.
func projectKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if !dispatch.ValidKey(key) {
		writeError(w, http.StatusBadRequest, "invalid_request", "a project's key is one upper-case letter then one to five upper-case letters or digits")
		return "", false
	}
	return key, true
}

// ticketKey reads a ticket route's key: a ticket's (CND-12), 400
// invalid_request otherwise. When it reports false it has answered.
func ticketKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if _, _, ok := dispatch.ParseKey(key); !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "a ticket's key is its project's key and its number, as CND-12")
		return "", false
	}
	return key, true
}

// pathID reads a route's {id}: the id of the row it names (a comment, a
// label, a milestone, a relation, a link or a view), 16 lower-case hex
// characters, 400 invalid_request otherwise, as a path's key that is not
// one is: the store would refuse it not_found and keep the refusal's
// receipt. When it reports false it has answered.
func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !dispatch.ValidID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "an id is 16 lower-case hex characters")
		return "", false
	}
	return id, true
}

// opAt starts the operation a route names by its path: the store for a
// write (dispatchWriter), then the path's key as keyOf reads it
// (projectKey or ticketKey; none when keyOf is nil), then its {id} when
// the route's pattern has one (pathID), then the body, at most maxBody,
// decoded into v, op being v's (readOp). When it reports false it has
// answered.
func (s *Server) opAt(w http.ResponseWriter, r *http.Request, keyOf func(http.ResponseWriter, *http.Request) (string, bool), v any, op *dispatchOp) (ds *dispatch.Store, key, id string, ok bool) {
	if ds, ok = s.dispatchWriter(w); !ok {
		return nil, "", "", false
	}
	if keyOf != nil {
		if key, ok = keyOf(w, r); !ok {
			return nil, "", "", false
		}
	}
	if strings.Contains(r.Pattern, "{id}") {
		if id, ok = pathID(w, r); !ok {
			return nil, "", "", false
		}
	}
	if !readOp(w, r, v, op, maxBody) {
		return nil, "", "", false
	}
	return ds, key, id, true
}

// projectAfter is what a project operation's reply carries: the project's
// row as read after it, by the key the receipt names (a create's), else
// key; nothing when there is neither.
func projectAfter(ctx context.Context, ds *dispatch.Store, key string) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		k := cmp.Or(key, rc.Key)
		if k == "" {
			return nil, nil
		}
		v, err := ds.Project(ctx, k)
		if err != nil {
			return nil, err
		}
		return v.Project, nil
	}
}

// ticketAfter is what a ticket operation's reply carries: the ticket in
// full as read after it, by the key the receipt names (a create's), else
// key; nothing when there is neither.
func ticketAfter(ctx context.Context, ds *dispatch.Store, key string) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		k := cmp.Or(key, rc.Key)
		if k == "" {
			return nil, nil
		}
		v, err := ds.Ticket(ctx, k)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
}

// rowAfter is what an operation on a row with a rev of its own (a
// comment, a label, a milestone, a view) answers beside its receipt: the
// row as read after it, deleted or not, by the id the route names, else
// the one the receipt names (a create's); nothing when there is neither.
func rowAfter[T any](id string, read func(id string) (T, error)) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		rid := cmp.Or(id, rc.ID)
		if rid == "" {
			return nil, nil
		}
		v, err := read(rid)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
}

// commentAfter is a comment of the ticket with the key as an operation's
// reply carries it, with its number (rowAfter).
func commentAfter(ctx context.Context, ds *dispatch.Store, key, id string) func(dispatch.Receipt) (any, error) {
	return rowAfter(id, func(id string) (dispatch.Comment, error) { return ds.Comment(ctx, key, id) })
}

// labelAfter is a label of the project with the key as an operation's
// reply carries it (rowAfter).
func labelAfter(ctx context.Context, ds *dispatch.Store, project, id string) func(dispatch.Receipt) (any, error) {
	return rowAfter(id, func(id string) (dispatch.Label, error) { return ds.Label(ctx, project, id) })
}

// milestoneAfter is a milestone of the project with the key as an
// operation's reply carries it (rowAfter).
func milestoneAfter(ctx context.Context, ds *dispatch.Store, project, id string) func(dispatch.Receipt) (any, error) {
	return rowAfter(id, func(id string) (dispatch.Milestone, error) { return ds.Milestone(ctx, project, id) })
}

// viewAfter is a saved view as an operation's reply carries it, its
// query the JSON it is (rowAfter).
func viewAfter(ctx context.Context, ds *dispatch.Store, id string) func(dispatch.Receipt) (any, error) {
	return rowAfter(id, func(id string) (dispatch.View, error) { return ds.View(ctx, id) })
}

// dispatchQuery is a read's query string, each parameter one the route
// takes (400 invalid_request for another, as a body's unknown field is)
// and each that takes one value given at most once.
type dispatchQuery url.Values

// readQuery reads the query string of a route that takes the parameters
// named (parseQuery).
func readQuery(r *http.Request, names ...string) (dispatchQuery, error) {
	v, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for k := range v {
		if !slices.Contains(names, k) {
			return nil, fmt.Errorf("this route takes no parameter %q", cut(k, 40))
		}
	}
	return dispatchQuery(v), nil
}

// maxQueryParams bounds the parameters of one query string, as
// url.ParseQuery bounds them by default: a query of more is refused whole,
// before any is read.
const maxQueryParams = 10000

// parseQuery parses a query string as a form's is read
// (application/x-www-form-urlencoded, as a browser's URLSearchParams
// reads one): its parameters are separated by '&' alone, so a ';' is
// part of a value, as a search or a name may hold one. url.ParseQuery
// refuses a query that holds a ';' unescaped, which a client's encoder
// leaves so (ufo's, the web app's, among them). A '+' is a space; a
// %-escape that is not one is an error, and so are more than
// maxQueryParams parameters. Every route reads its query through it:
// Dispatch's reads through readQuery, the others through queryValues, and
// a token in the query through presentedToken.
func parseQuery(raw string) (url.Values, error) {
	if strings.Count(raw, "&") >= maxQueryParams {
		return nil, fmt.Errorf("the query string holds more than %d parameters", maxQueryParams)
	}
	v := url.Values{}
	for raw != "" {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(key)
		if err == nil {
			value, err = url.QueryUnescape(value)
		}
		if err != nil {
			return nil, errors.New("the query string holds a %-escape that is not one")
		}
		v[key] = append(v[key], value)
	}
	return v, nil
}

// queryValues is the query string of a route that reads its parameters by
// name, read by parseQuery: a query it cannot read is 400 invalid_request,
// never read as no parameters. When it reports false it has answered.
func queryValues(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	v, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	return v, true
}

// noQuery refuses any parameter on a read that takes none, as readQuery
// refuses one a route does not take: 400 invalid_request. When it reports
// false it has answered.
func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if _, err := readQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}

// one is the value of a parameter given at most once, "" when absent.
func (q dispatchQuery) one(name string) (string, error) {
	switch vs := q[name]; len(vs) {
	case 0:
		return "", nil
	case 1:
		return vs[0], nil
	}
	return "", fmt.Errorf("%s is given more than once", name)
}

// flag is a parameter that is true or false (1 or 0), false when absent.
func (q dispatchQuery) flag(name string) (bool, error) {
	s, err := q.one(name)
	if err != nil || s == "" {
		return false, err
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("%s is 1 or 0, true or false", name)
	}
	return b, nil
}

// number is a parameter that is a whole number, 0 when absent.
func (q dispatchQuery) number(name string) (int64, error) {
	s, err := q.one(name)
	if err != nil || s == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is a whole number", name)
	}
	return n, nil
}

// count is a parameter that is a whole number of things (a page's size, a
// comment's number), 0 when absent; the reader checks its range.
func (q dispatchQuery) count(name string) (int, error) {
	s, err := q.one(name)
	if err != nil || s == "" {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s is a whole number", name)
	}
	return n, nil
}

// cut is s cut to at most n bytes at a character's boundary, with "…"
// where it was cut, for an error that quotes what a client sent.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// The projects.

// handleDispatchProjects answers a page of the projects by key: {projects,
// next?}, each a summary {id, key, name, tracker, archivedAt?, open, done,
// rev, updatedAt}, its open and done tickets counted and its description,
// folder, tracker URL and close policy left to its read, so a page is
// under maxProjectsPage; the archived ones only with ?archived=1; ?limit=
// 1 to 200 (200 when absent); ?cursor= the next of the page before. 400
// invalid_request for a bad value.
func (s *Server) handleDispatchProjects(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "archived", "limit", "cursor")
	var archived bool
	var limit int
	var cursor string
	if err == nil {
		archived, err = q.flag("archived")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err == nil {
		cursor, err = q.one("cursor")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := ds.Projects(r.Context(), archived, limit, cursor)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// handleDispatchProject answers a project with what its pages offer:
// {project, labels, milestones, views}, archived or not, a view's query
// the JSON it is; 404 not_found; 400 invalid_request for any parameter.
func (s *Server) handleDispatchProject(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	v, err := ds.Project(r.Context(), key)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	writeDispatch(w, http.StatusOK, v)
}

// handleDispatchCreateProject makes a project (project.create): body
// {opId, byName?, key, name, description?, cwd?, tracker?, trackerUrl?,
// closeRequires?}; 201 {receipt, project}; 409 key_taken when another
// project holds the key, 409 invalid or bound for a field the store
// refuses.
func (s *Server) handleDispatchCreateProject(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body projectCreateBody
	if !readOp(w, r, &body, &body.dispatchOp, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.CreateProject(ctx, body.op(), body.NewProject)
	s.writeOp(w, "project", rc, err, true, "project", projectAfter(ctx, ds, ""))
}

// handleDispatchEditProject sets the fields given of a project
// (project.edit): body {opId, expectedRev?, byName?, name?, description?,
// cwd?, tracker?, trackerUrl?, closeRequires?}; {receipt, project}; 409
// archived for an archived project.
func (s *Server) handleDispatchEditProject(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok {
		return
	}
	var body projectEditBody
	if !readOp(w, r, &body, &body.dispatchOp, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditProject(ctx, body.op(), key, body.ProjectEdit)
	s.writeOp(w, "project", rc, err, false, "project", projectAfter(ctx, ds, key))
}

// handleDispatchArchiveProject archives a project (project.archive):
// body {opId, expectedRev?, byName?}; {receipt, project}.
func (s *Server) handleDispatchArchiveProject(w http.ResponseWriter, r *http.Request) {
	s.archiveProject(w, r, true)
}

// handleDispatchUnarchiveProject unarchives a project (project.unarchive).
func (s *Server) handleDispatchUnarchiveProject(w http.ResponseWriter, r *http.Request) {
	s.archiveProject(w, r, false)
}

func (s *Server) archiveProject(w http.ResponseWriter, r *http.Request, archive bool) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok {
		return
	}
	var body dispatchOp
	if !readOp(w, r, &body, &body, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	archiveOrNot := ds.UnarchiveProject
	if archive {
		archiveOrNot = ds.ArchiveProject
	}
	rc, err := archiveOrNot(ctx, body.op(), key)
	s.writeOp(w, "project", rc, err, false, "project", projectAfter(ctx, ds, key))
}

// The labels and the milestones.

// handleDispatchCreateLabel makes a label in a project (label.create):
// body {opId, byName?, name, color}, the name 1 to 40 characters with no
// control character that no live label of the project holds, whatever
// its case, and the colour one of the twelve (dispatch.LabelColors); 201
// {receipt, label}; 404 not_found for a project missing; 409 invalid for
// a name or a colour the store refuses, bound past 100 live labels,
// archived for an archived project.
func (s *Server) handleDispatchCreateLabel(w http.ResponseWriter, r *http.Request) {
	var body labelCreateBody
	ds, key, _, ok := s.opAt(w, r, projectKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.CreateLabel(ctx, body.op(), key, body.NewLabel)
	s.writeOp(w, "label", rc, err, true, "label", labelAfter(ctx, ds, key, ""))
}

// handleDispatchEditLabel sets the fields given of a live label of the
// project (label.edit): body {opId, expectedRev?, byName?, name?,
// color?}, each held to its create's rule; {receipt, label}; 404
// not_found for a label missing, deleted or another project's; 409
// invalid for an edit that changes nothing.
func (s *Server) handleDispatchEditLabel(w http.ResponseWriter, r *http.Request) {
	var body labelEditBody
	ds, key, id, ok := s.opAt(w, r, projectKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditLabel(ctx, body.op(), key, id, body.LabelEdit)
	s.writeOp(w, "label", rc, err, false, "label", labelAfter(ctx, ds, key, id))
}

// handleDispatchDeleteLabel deletes a live label of the project
// (label.delete): body {opId, expectedRev?, byName?}; {receipt, label},
// a tombstone whose name is free for a new label. It leaves every ticket
// that has it, deleted ones included, one change row on each, and its
// commit's dispatch events name the label and those tickets: one event
// for them when they are more than maxDispatchEach (events.go). 404
// not_found for a label missing, deleted already or another project's.
func (s *Server) handleDispatchDeleteLabel(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, projectKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DeleteLabel(ctx, body.op(), key, id)
	s.writeOp(w, "label", rc, err, false, "label", labelAfter(ctx, ds, key, id))
}

// handleDispatchCreateMilestone makes a milestone in a project
// (milestone.create): body {opId, byName?, name, description?, target?,
// state?}, the name 1 to 60 characters, the description Markdown of at
// most 4 KiB, the target a date (YYYY-MM-DD) or none, the state open (the
// default) or closed; 201 {receipt, milestone}; 404 not_found for a
// project missing; 409 invalid for a field the store refuses, bound past
// 100 live milestones, archived for an archived project.
func (s *Server) handleDispatchCreateMilestone(w http.ResponseWriter, r *http.Request) {
	var body milestoneCreateBody
	ds, key, _, ok := s.opAt(w, r, projectKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.CreateMilestone(ctx, body.op(), key, body.NewMilestone)
	s.writeOp(w, "milestone", rc, err, true, "milestone", milestoneAfter(ctx, ds, key, ""))
}

// handleDispatchEditMilestone sets the fields given of a live milestone
// of the project (milestone.edit): body {opId, expectedRev?, byName?,
// name?, description?, target?, state?}, each held to its create's rule,
// the target "" for none; {receipt, milestone}; 404 not_found for a
// milestone missing, deleted or another project's; 409 invalid for an
// edit that changes nothing.
func (s *Server) handleDispatchEditMilestone(w http.ResponseWriter, r *http.Request) {
	var body milestoneEditBody
	ds, key, id, ok := s.opAt(w, r, projectKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditMilestone(ctx, body.op(), key, id, body.MilestoneEdit)
	s.writeOp(w, "milestone", rc, err, false, "milestone", milestoneAfter(ctx, ds, key, id))
}

// handleDispatchDeleteMilestone deletes a live milestone of the project
// (milestone.delete): body {opId, expectedRev?, byName?}; {receipt,
// milestone}, a tombstone. It leaves every ticket naming it, deleted ones
// included, its milestone cleared with one change row on each, and its
// commit's dispatch events name the milestone and those tickets, as a
// label's delete does. 404 not_found for a milestone missing, deleted
// already or another project's.
func (s *Server) handleDispatchDeleteMilestone(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, projectKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DeleteMilestone(ctx, body.op(), key, id)
	s.writeOp(w, "milestone", rc, err, false, "milestone", milestoneAfter(ctx, ds, key, id))
}

// The people.

// handleDispatchPeople answers the directory the owner and assignee
// pickers offer, by name: {people}, each {name, createdAt}; 400
// invalid_request for any parameter.
func (s *Server) handleDispatchPeople(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok || !noQuery(w, r) {
		return
	}
	people, err := ds.People(r.Context())
	if err != nil {
		s.dispatchError(w, "person", err)
		return
	}
	if people == nil {
		people = []dispatch.Person{}
	}
	writeDispatch(w, http.StatusOK, map[string]any{"people": people})
}

// handleDispatchAddPerson adds a name to the directory (person.add): body
// {opId, byName?, name}; 201 {receipt, person}; 409 invalid for a name
// held already or not one.
func (s *Server) handleDispatchAddPerson(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body personBody
	if !readOp(w, r, &body, &body.dispatchOp, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.AddPerson(ctx, body.op(), body.Name)
	s.writeOp(w, "person", rc, err, true, "person", func(rc dispatch.Receipt) (any, error) {
		if rc.ID == "" {
			return nil, nil
		}
		people, err := ds.People(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			if p.Name == rc.ID {
				return p, nil
			}
		}
		return nil, nil
	})
}

// The tickets.

// ticketParams is what a ticket list's query takes: status, priority,
// label, assignee and horizon as often as they are wanted (a ticket holding
// any of them), the rest once.
var ticketParams = []string{"project", "status", "priority", "label", "owner", "assignee", "milestone", "horizon", "parent",
	"blocked", "q", "sort", "desc", "deleted", "limit", "cursor"}

// ticketQuery reads a ticket list's query (dispatch.Query) from the query
// string: a priority is its number, 0 (none) to 4 (urgent); a label and a
// milestone their ids; an assignee person:<name> or agent:<catalog id>; a
// horizon now, next, later or empty for not planned; blocked, desc and
// deleted 1 or 0. The store checks each value against its vocabulary and
// bound (dispatch.ErrBadQuery).
func ticketQuery(q dispatchQuery) (dispatch.Query, error) {
	var out dispatch.Query
	for _, s := range []struct {
		name string
		dst  *string
	}{
		{"project", &out.Project}, {"owner", &out.Owner}, {"milestone", &out.Milestone}, {"parent", &out.Parent},
		{"q", &out.Q}, {"sort", &out.Sort}, {"cursor", &out.Cursor},
	} {
		v, err := q.one(s.name)
		if err != nil {
			return out, err
		}
		*s.dst = v
	}
	for _, f := range []struct {
		name string
		dst  *bool
	}{{"blocked", &out.Blocked}, {"desc", &out.Desc}, {"deleted", &out.Deleted}} {
		v, err := q.flag(f.name)
		if err != nil {
			return out, err
		}
		*f.dst = v
	}
	for _, p := range q["priority"] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, errors.New("a priority is its number, 0 (none) to 4 (urgent)")
		}
		out.Priority = append(out.Priority, n)
	}
	out.Status, out.Labels, out.Assignees, out.Horizon = q["status"], q["label"], q["assignee"], q["horizon"]
	limit, err := q.count("limit")
	out.Limit = limit
	return out, err
}

// handleDispatchTickets answers a page of a list of tickets, filtered and
// sorted on the server: {tickets, next?, total}, each a summary without
// its description, its acceptance criteria and its acceptance; ?project=
// a project's key (every project's when absent); ?status=, ?priority=,
// ?label=, ?assignee=, ?horizon= each as often as wanted; ?owner=,
// ?milestone=, ?parent= (a ticket's key), ?blocked=1, ?q= (words, every
// one in the ticket or its comments, the last a prefix), ?sort= (updated,
// created, priority, status, title, key, milestone) and ?desc=1, ?deleted=1
// (the tombstones beside the live tickets, which a list leaves out
// otherwise), ?limit= 1 to 200 (100 when absent) and ?cursor= the next of
// the page before. 400 invalid_request for a bad value.
func (s *Server) handleDispatchTickets(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, ticketParams...)
	var query dispatch.Query
	if err == nil {
		query, err = ticketQuery(q)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := ds.List(r.Context(), query)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// handleDispatchTicket answers a ticket, deleted or not, in full from one
// read transaction: {ticket} with its assignees, labels, parent, children
// (the first 50 live and how many), relations both ways, whether it is
// blocked, links, attachments, the last 50 comments and its close
// policy's state (close); 404 not_found; 400 invalid_request for any
// parameter.
func (s *Server) handleDispatchTicket(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	v, err := ds.Ticket(r.Context(), key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"ticket": v})
}

// handleDispatchCreateTicket makes a ticket (ticket.create): body {opId,
// byName?, project, title, description?, acceptance?, status?,
// priority?, horizon?, owner?, assignees?, labels?, milestone?, parent?},
// at most maxTicketBody; 201 {receipt, ticket}, the ticket in full; 404
// not_found for a project, parent, label or milestone missing or deleted;
// 409 for what the store refuses (archived, invalid, bound, cycle,
// close_policy).
func (s *Server) handleDispatchCreateTicket(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	var body ticketCreateBody
	if !readOp(w, r, &body, &body.dispatchOp, maxTicketBody) {
		return
	}
	if !dispatch.ValidKey(body.Project) {
		writeError(w, http.StatusBadRequest, "invalid_request", "project is a project's key")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.CreateTicket(ctx, body.op(), body.NewTicket)
	s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, ""))
}

// handleDispatchEditTicket edits a live ticket (ticket.edit): body {opId,
// expectedRev?, byName?, set?: {title?, description?, acceptance?,
// status?, priority?, horizon?, owner?, parent?, milestone?}, add?:
// {assignees?, labels?}, remove?: {assignees?, labels?}}, at most
// maxTicketBody; {receipt, ticket}; 409 close_policy, the receipt's
// missing naming what the policy lacks, for a move to done it refuses;
// 409 cycle for a parent that would make a cycle or a chain past 8; 404
// not_found for a deleted ticket.
func (s *Server) handleDispatchEditTicket(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body ticketEditBody
	if !readOp(w, r, &body, &body.dispatchOp, maxTicketBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditTicket(ctx, body.op(), key, body.TicketEdit)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchAcceptTicket records the owner's acceptance of a live
// ticket (ticket.accept): body {opId, expectedRev?, byName?, note?};
// {receipt, ticket}, accepted by the display name, for the owner the
// ticket has now and against its criteria now; 409 invalid when it has no
// owner.
func (s *Server) handleDispatchAcceptTicket(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body acceptBody
	if !readOp(w, r, &body, &body.dispatchOp, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.AcceptTicket(ctx, body.op(), key, body.Note)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchUnacceptTicket clears a live ticket's acceptance
// (ticket.unaccept): body {opId, expectedRev?, byName?}; {receipt,
// ticket}; 409 invalid when it is not accepted.
func (s *Server) handleDispatchUnacceptTicket(w http.ResponseWriter, r *http.Request) {
	s.ticketOp(w, r, (*dispatch.Store).UnacceptTicket)
}

// handleDispatchDeleteTicket deletes a live ticket (ticket.delete): a
// tombstone and nothing else written, its key kept; body {opId,
// expectedRev?, byName?}; {receipt, ticket}. A deleted ticket is listed
// only with ?deleted=1, its page read, and every write on it but its
// undelete is 404 not_found.
func (s *Server) handleDispatchDeleteTicket(w http.ResponseWriter, r *http.Request) {
	s.ticketOp(w, r, (*dispatch.Store).DeleteTicket)
}

// handleDispatchUndeleteTicket restores a deleted ticket whole
// (ticket.undelete): body {opId, expectedRev?, byName?}; {receipt,
// ticket}; 409 invalid for a live ticket.
func (s *Server) handleDispatchUndeleteTicket(w http.ResponseWriter, r *http.Request) {
	s.ticketOp(w, r, (*dispatch.Store).UndeleteTicket)
}

// ticketOp runs an operation on a ticket that takes nothing but its key.
func (s *Server) ticketOp(w http.ResponseWriter, r *http.Request, apply func(*dispatch.Store, context.Context, dispatch.Op, string) (dispatch.Receipt, error)) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body dispatchOp
	if !readOp(w, r, &body, &body, maxBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := apply(ds, ctx, body.op(), key)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchHistory answers a page of a ticket's history, deleted or
// not, as the store recorded it, field by field: {changes, next?}, the
// changes of the ticket and of its comments after ?after= (a change's seq,
// 0 when absent), at most ?limit= (1 to 500, 100 when absent) and about
// 1 MiB of their text; next is the seq to read after for the next page.
func (s *Server) handleDispatchHistory(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "after", "limit")
	var after int64
	var limit int
	if err == nil {
		after, err = q.number("after")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := ds.History(r.Context(), key, after, limit)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// handleDispatchComments answers a ticket's comments beyond the page its
// read carries: {comments}, those numbered after ?after= (0 when absent),
// at most ?limit= (1 to maxCommentPage, 50, and 50 when absent: the page
// the read carries), each with its number n, tombstones among them so the
// numbers have no gap; the largest page is under MaxTicketReply.
func (s *Server) handleDispatchComments(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "after", "limit")
	var after, limit int
	if err == nil {
		after, err = q.count("after")
	}
	if err == nil {
		limit, err = q.count("limit")
	}
	if err == nil && limit == 0 {
		limit = maxCommentPage
	}
	// The range is checked here, at maxCommentPage, under the store's own
	// (whose refusal of a page out of its range is no ErrBadQuery).
	if err == nil && (after < 0 || limit < 1 || limit > maxCommentPage) {
		err = fmt.Errorf("a page of comments is 1 to %d after a number of 0 or more", maxCommentPage)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	comments, err := ds.Comments(r.Context(), key, after, limit)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"comments": comments})
}

// handleDispatchAddComment comments on a live ticket (comment.add): body
// {opId, expectedRev?, byName?, body}, Markdown of 1 to 16 KiB, its
// author the admin, kept as the actor admin and by the display name; 201
// {receipt, comment}, the comment with its number n; expectedRev the
// ticket's; 404 not_found for a ticket missing or deleted; 409 invalid
// for an empty body, bound past 16 KiB or past the ticket's 2,000
// comments, archived for an archived project.
func (s *Server) handleDispatchAddComment(w http.ResponseWriter, r *http.Request) {
	var body commentBody
	ds, key, _, ok := s.opAt(w, r, ticketKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.AddComment(ctx, body.op(), key, body.Body)
	s.writeOp(w, "comment", rc, err, true, "comment", commentAfter(ctx, ds, key, ""))
}

// handleDispatchEditComment replaces the body of a live comment of a live
// ticket (comment.edit): body {opId, expectedRev?, byName?, body}, the
// body it replaces kept in the history; {receipt, comment}; expectedRev
// the comment's; 404 not_found for a comment missing, deleted or another
// ticket's; 409 invalid for the body it holds already.
func (s *Server) handleDispatchEditComment(w http.ResponseWriter, r *http.Request) {
	var body commentBody
	ds, key, id, ok := s.opAt(w, r, ticketKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditComment(ctx, body.op(), key, id, body.Body)
	s.writeOp(w, "comment", rc, err, false, "comment", commentAfter(ctx, ds, key, id))
}

// handleDispatchDeleteComment deletes a live comment of a live ticket
// (comment.delete): body {opId, expectedRev?, byName?}; {receipt,
// comment}, a tombstone that keeps its place and its number; 404
// not_found for a comment missing, deleted already or another ticket's.
func (s *Server) handleDispatchDeleteComment(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, ticketKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DeleteComment(ctx, body.op(), key, id)
	s.writeOp(w, "comment", rc, err, false, "comment", commentAfter(ctx, ds, key, id))
}

// The relations and the links: members of a ticket's sets, with no rev of
// their own, so an operation on one answers the ticket, whose rev it
// bumps and expectedRev is.

// handleDispatchAddRelation relates a live ticket to another of its
// project (relation.add): body {opId, expectedRev?, byName?, kind, to},
// kind blocks (the ticket blocks to) or relates, to the other's key; 201
// {receipt, ticket}, the ticket in full, the receipt naming the
// relation's id; both tickets' revs bumped, expectedRev the first's. 404
// not_found for either ticket missing or deleted; 409 cycle for a blocks
// that would close a cycle, the ticket blocking itself among them; 409
// invalid for a to of another project, a kind that is none of the two or
// a relation held already; bound past 100 on either ticket.
func (s *Server) handleDispatchAddRelation(w http.ResponseWriter, r *http.Request) {
	var body relationBody
	ds, key, _, ok := s.opAt(w, r, ticketKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.AddRelation(ctx, body.op(), key, body.Kind, body.To)
	s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchRemoveRelation removes a live relation from a live ticket
// at either of its ends (relation.remove): body {opId, expectedRev?,
// byName?}; {receipt, ticket}, the ticket with the key, both tickets'
// revs bumped; 404 not_found for a relation missing, removed or not the
// ticket's, and for one whose other end is deleted.
func (s *Server) handleDispatchRemoveRelation(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, ticketKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.RemoveRelation(ctx, body.op(), key, id)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchAddLink links a live ticket outward (link.add): body
// {opId, expectedRev?, byName?, kind, ref?, url?, note?}. A delivery or
// an external link names its url (http or https) and takes it as its ref
// when it names none; a run link names its run as ref, one the engine
// holds or has a record of (404 not_found otherwise) and that no other
// live ticket's live run link names (409 run_linked, the receipt's key
// naming that ticket). 201 {receipt, ticket}, the receipt naming the
// link's id; 409 invalid for a second live link of the same kind and ref
// or a field the store refuses, bound past 50.
func (s *Server) handleDispatchAddLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	ds, key, _, ok := s.opAt(w, r, ticketKey, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.AddLink(ctx, body.op(), key, body.NewLink)
	s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchRemoveLink removes a live link from a live ticket
// (link.remove): body {opId, expectedRev?, byName?}; {receipt, ticket};
// a link added again after it is a new one with a new id; 404 not_found
// for a link missing, removed or another ticket's.
func (s *Server) handleDispatchRemoveLink(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, id, ok := s.opAt(w, r, ticketKey, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.RemoveLink(ctx, body.op(), key, id)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchRunTicket answers the ticket a run is linked to, for the
// run page's chip: {ticket: {id, key, title, status, deletedAt?}}, the
// live ticket whose live run link names ?run= (one at most), else, when
// only deleted tickets' links name it, the latest-linked of them with
// deletedAt; {ticket: null} when no live link names the run. 400
// invalid_request for a run id that is not one.
func (s *Server) handleDispatchRunTicket(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "run")
	var run string
	if err == nil {
		run, err = q.one("run")
	}
	if err == nil && !crew.ValidRunID(run) {
		err = errors.New("run is a run's id")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	tk, found, err := ds.RunTicket(r.Context(), run)
	if err != nil {
		s.dispatchError(w, "run", err)
		return
	}
	var out *dispatch.RelatedTicket
	if found {
		out = &dispatch.RelatedTicket{ID: tk.ID, Key: tk.Key, Title: tk.Title, Status: tk.Status, DeletedAt: tk.DeletedAt}
	}
	writeDispatch(w, http.StatusOK, map[string]any{"ticket": out})
}

// The saved views.

// viewQuery is a view's query as the store keeps it: the JSON the body
// carried, compacted, so what is kept is what every reply shows (a
// json.RawMessage is written compacted) and its bound, 8 KiB as stored,
// counts no space; "" when the body carried none, which the store
// refuses. The body decoded, so it is JSON, an object or not: the store
// refuses any other (invalid).
func viewQuery(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// handleDispatchViews answers the live saved views, by name: {views},
// each {id, projectId, name, kind, query, createdAt, updatedAt, rev}, its
// query the JSON it is; with ?project= a project's key, the views its
// pages offer, its own and every project's, else every live view. 404
// not_found for a project missing; 400 invalid_request for a key that is
// not one and for any other parameter. At most 200 live views with
// queries of 8 KiB, every project's together: under MaxProjectReply, as
// the project's read that carries them is.
func (s *Server) handleDispatchViews(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "project")
	var project string
	if err == nil {
		project, err = q.one("project")
	}
	if err == nil && project != "" && !dispatch.ValidKey(project) {
		err = errors.New("project is a project's key")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	views, err := ds.Views(r.Context(), project)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"views": views})
}

// handleDispatchCreateView saves a view (view.create): body {opId,
// byName?, project?, name, kind, query}, of the project with the key or,
// without one, of every project; the name 1 to 60 characters; the kind
// list, board or roadmap; the query a JSON object of a list's filter,
// sort and grouping (dispatch.ViewQuery), its values each in its
// vocabulary, at most 8 KiB as kept (viewQuery). 201 {receipt, view}; 404
// not_found for a project missing; 409 invalid for a field the store
// refuses, a query that is not one among them, bound past 8 KiB or past
// 200 live views, archived for an archived project.
func (s *Server) handleDispatchCreateView(w http.ResponseWriter, r *http.Request) {
	var body viewCreateBody
	ds, _, _, ok := s.opAt(w, r, nil, &body, &body.dispatchOp)
	if !ok {
		return
	}
	if body.Project != "" && !dispatch.ValidKey(body.Project) {
		writeError(w, http.StatusBadRequest, "invalid_request", "project is a project's key, or absent for every project")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.CreateView(ctx, body.op(), dispatch.NewView{Project: body.Project, Name: body.Name, Kind: body.Kind, Query: viewQuery(body.Query)})
	s.writeOp(w, "view", rc, err, true, "view", viewAfter(ctx, ds, ""))
}

// handleDispatchEditView sets the fields given of a live view
// (view.edit): body {opId, expectedRev?, byName?, name?, kind?, query?},
// each held to its create's rule, its project never changing; {receipt,
// view}; 404 not_found for a view missing or deleted; 409 invalid for an
// edit that changes nothing, archived for a view of an archived project.
func (s *Server) handleDispatchEditView(w http.ResponseWriter, r *http.Request) {
	var body viewEditBody
	ds, _, id, ok := s.opAt(w, r, nil, &body, &body.dispatchOp)
	if !ok {
		return
	}
	edit := dispatch.ViewEdit{Name: body.Name, Kind: body.Kind}
	if body.Query != nil {
		query := viewQuery(body.Query)
		edit.Query = &query
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.EditView(ctx, body.op(), id, edit)
	s.writeOp(w, "view", rc, err, false, "view", viewAfter(ctx, ds, id))
}

// handleDispatchDeleteView deletes a live view (view.delete): body {opId,
// expectedRev?, byName?}; {receipt, view}, a tombstone; 404 not_found for
// a view missing or deleted already; 409 archived for a view of an
// archived project.
func (s *Server) handleDispatchDeleteView(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, _, id, ok := s.opAt(w, r, nil, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DeleteView(ctx, body.op(), id)
	s.writeOp(w, "view", rc, err, false, "view", viewAfter(ctx, ds, id))
}

// handleDispatchOp answers an operation's receipt of the admin's without
// acting, as GET /api/runs/{run}/commands/{command} answers a command's:
// {receipt}; 404 not_found when the store holds none (never given, or
// pruned past the newest 20,000: a resend of a pruned one that ran is
// still answered done, from the store's log); 400 invalid_request for an
// id that is not one, and for any parameter. A client that lost a reply
// reads here before it sends again.
func (s *Server) handleDispatchOp(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id := r.PathValue("opId")
	if !dispatch.OpIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "an operation's id is 1 to 32 letters, digits, '.', '_', '~' or '-'")
		return
	}
	if !noQuery(w, r) {
		return
	}
	rc, found, err := ds.Receipt(r.Context(), adminActor, id)
	if err != nil {
		s.dispatchError(w, "operation", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such operation")
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"receipt": rc})
}
