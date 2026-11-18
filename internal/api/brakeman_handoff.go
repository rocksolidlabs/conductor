package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// Sending a handoff package to a task's next attempt (issue #1012): three
// admin routes, each through requireAdmin and brakemanDeskOf, none taking
// a query (a query is 400 invalid_request, and is never logged):
//
//	GET  /api/brakeman/requests/{id}/items/{i}/hand   the preview: the exact text the server would hand, where it goes, or why it cannot go
//	POST /api/brakeman/requests/{id}/items/{i}/hand   the send: {opId, sha256, by?}
//	POST /api/brakeman/handoffs/{hid}/withdraw        the withdraw: {by?}
//
// The target is the request's own subject as the server resolves it (a
// request of kind handoff about a task or an attempt); nothing of the
// item, the brief or a body names a run, a task or a text. Brakeman's
// turn token reaches none of them (they are admin routes), no grant names
// them, and no tool calls them.

// The routes' bounds.
const (
	// maxHandBody bounds the send's and the withdraw's bodies.
	maxHandBody = 1 << 10
	// maxHandPreview bounds the preview's reply as written: encoded
	// without HTML escaping, a text of MaxHandoffText bytes takes at most
	// twice that (a line break, a tab, a quote and a backslash are the
	// only characters written as two), beside a record of at most
	// MaxHandoffRecord and the target, the refusal and the field names.
	maxHandPreview = 2*brakeman.MaxHandoffText + brakeman.MaxHandoffRecord + 2<<10
)

// The refusals of a preview (answered 200 with the code in refusal) and of
// a send (answered with the status beside each). An unknown code is a
// newer server's: a page draws it as "cannot be sent now".
const (
	refusalNoTask        = "no_task"           // 409: the request is not of kind handoff, or its subject is a session
	refusalRunNotRunning = "run_not_running"   // 409: the newest run of the subject's chain is not held by this server, or is stopped
	refusalRunContinuing = "run_continuing"    // 409: a run of the subject's chain is being continued now
	refusalNotManaged    = "not_managed"       // 409: the run has no tasks, or is a Brakeman turn's own run
	refusalComparison    = "comparison"        // 409: the run compares candidates
	refusalTaskNotFound  = "task_not_found"    // 404: the run's crew as launched has no such task
	refusalTooLarge      = "package_too_large" // 409: the text is past MaxHandoffText (16 KiB)
)

// The send's own refusals, beside the preview's: 400 invalid_request (a
// body that does not decode, an unknown field, an opId or a sha256 of the
// wrong form, a by refused by crew.HandoffByOK), 413 bound (a body past
// maxHandBody), 404 not_found (no request, no brief, no such item),
// 409 not_a_handoff (the item is not handoff/1), 409 op_conflict (the
// opId sent another item), 409 text_changed (the text is not the one
// whose sha256 was sent), 409 handoffs_full (MaxHandoffsWaiting wait: the
// reply's error names them, handFullError), 503 brakeman_not_owner,
// store_unavailable, brakeman_recovering, shutting_down, 403 switchyard,
// and 401 for any credential but the admin's.

// handBody is the send's body. opId is the page's own (Dispatch's op id
// form); sha256 is that of the text the preview showed; by is the name the
// person gives, at most brakeman.MaxBy bytes on one line (crew.HandoffByOK).
type handBody struct {
	OpID   string `json:"opId"`
	SHA256 string `json:"sha256"`
	By     string `json:"by,omitempty"`
}

// withdrawBody is the withdraw's body; it may be absent.
type withdrawBody struct {
	By string `json:"by,omitempty"`
}

// handPreview is the preview's reply: the text exactly as the server would
// hand it (HandoffText of the item: "" when it is past MaxHandoffText,
// whose refusal is then package_too_large), its size in bytes and its
// SHA-256, and exactly one of target and refusal. handoff is the newest
// record made from this item, absent when none was sent.
type handPreview struct {
	Text    string       `json:"text"`
	Bytes   int          `json:"bytes"`
	SHA256  string       `json:"sha256"`
	Target  *handTarget  `json:"target,omitempty"`
	Refusal *handRefusal `json:"refusal,omitempty"`
	Handoff *sentRecord  `json:"handoff,omitempty"`
}

// handTarget is where a send would go: the run (the newest of the
// subject's chain, held by this server), its name, the task, the task's
// state now (crew's task states) and the number of its latest attempt (0
// for none).
type handTarget struct {
	Run     string `json:"run"`
	RunName string `json:"runName"`
	Task    string `json:"task"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
	// Starting is true while that attempt is open, still reserved and its
	// agent has not started (no session yet): the dispatch is preparing
	// it, and it is handed the package if its task file is not written
	// yet, otherwise the package waits for the attempt after it. Absent
	// when false.
	Starting bool `json:"starting,omitempty"`
}

// handRefusal is why a preview's package cannot be sent now: one of the
// refusal codes above and the server's words for other callers.
type handRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// sentRecord is a record as the routes answer it: the store's record
// without its text, at most brakeman.MaxHandoffRecord bytes, and, for a
// handed record whose attempt closed without its agent ever starting,
// notStarted (judged from the engine's run while it holds it, else from
// the ledger's row of that attempt: sentRecordOf): the attempt took the
// package and never read it, so the person may send it again.
type sentRecord struct {
	brakeman.SentHandoff
	NotStarted bool `json:"notStarted,omitempty"`
}

// handSent is the send's and the withdraw's reply: 201 for a new send,
// 200 for a repeat of the same opId and for every withdraw.
type handSent struct {
	Handoff sentRecord `json:"handoff"`
}

// handFullError is 409 handoffs_full's error: the waiting records, so a
// page can link each one's brief and withdraw it there.
type handFullError struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Waiting []handWaiting `json:"waiting"`
}

// handWaiting is one waiting record as handoffs_full names it.
type handWaiting struct {
	ID      string `json:"id"`
	Request string `json:"request"`
	Run     string `json:"run"`
	Task    string `json:"task"`
}

// routeBrakemanHandoff registers the three routes (routeBrakeman calls it).
func (s *Server) routeBrakemanHandoff(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/brakeman/requests/{id}/items/{i}/hand", s.requireAdmin(s.handleHandPreview))
	mux.HandleFunc("POST /api/brakeman/requests/{id}/items/{i}/hand", s.requireAdmin(s.handleHand))
	mux.HandleFunc("POST /api/brakeman/handoffs/{hid}/withdraw", s.requireAdmin(s.handleHandWithdraw))
}

// sha256Pattern is a SHA-256 in lower-case hex.
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handNoQuery refuses a query on a route that takes none, without
// repeating it.
func handNoQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeError(w, http.StatusBadRequest, "invalid_request", "this route takes no query")
		return false
	}
	return true
}

// handItem is the package a route names: the request, the item's index,
// the item and its text as the server would hand it.
type handItem struct {
	req  brakeman.Request
	item int
	text string
}

// handItemOf reads the request's item the route names: 404 not_found for
// no request, no brief or no such item, 409 not_a_handoff for an item
// that is not a handoff package. ok false is answered.
func (s *Server) handItemOf(w http.ResponseWriter, r *http.Request, d *brakemanDesk) (handItem, bool) {
	id := r.PathValue("id")
	item, err := strconv.Atoi(r.PathValue("i"))
	if !brakemanIDPattern(id) || err != nil || item < 0 || strconv.Itoa(item) != r.PathValue("i") || item >= brakeman.MaxItems {
		writeError(w, http.StatusNotFound, "not_found", "no such request or item")
		return handItem{}, false
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return handItem{}, false
	}
	if !req.HasBrief {
		writeError(w, http.StatusNotFound, "not_found", "the request has no brief")
		return handItem{}, false
	}
	b, _, err := d.st.Brief(id)
	if err != nil {
		s.brakemanError(w, err)
		return handItem{}, false
	}
	if item >= len(b.Items) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("the brief has %d items", len(b.Items)))
		return handItem{}, false
	}
	it := b.Items[item]
	if it.Kind != brakeman.ItemHandoff || it.Handoff == nil {
		writeError(w, http.StatusConflict, "not_a_handoff", "the item is not a handoff package")
		return handItem{}, false
	}
	return handItem{req: req, item: item, text: brakeman.HandoffText(*it.Handoff)}, true
}

// handGoal is where a request's package goes: the target and the run's
// chain (every run of it the server knows), or the refusal with its
// status.
type handGoal struct {
	target  *handTarget
	chain   []string
	refusal *handRefusal
	status  int
}

// handRefused is a goal refused with code, its status and words.
func handRefused(status int, code, message string) handGoal {
	return handGoal{refusal: &handRefusal{Code: code, Message: message}, status: status}
}

// handGoalOf resolves the request's own subject, and nothing else, to the
// run that would take the package now and the task: the request must be
// of kind handoff about a task or an attempt; the newest run of its chain
// must be held by this server and not stopped or continued, a managed run
// that is neither a Brakeman turn's nor a comparison's, whose crew as
// launched has the task. An error is the ledger's.
func (s *Server) handGoalOf(ctx context.Context, req brakeman.Request) (handGoal, error) {
	if req.Kind != brakeman.KindHandoff || len(req.Subjects) != 1 || (req.Subjects[0].Kind != brakeman.RefTask && req.Subjects[0].Kind != brakeman.RefAttempt) {
		return handRefused(http.StatusConflict, refusalNoTask, "only a handoff package of a request to prepare a handoff about a task or an attempt goes to a task"), nil
	}
	subject := req.Subjects[0]
	nodes, _, _, err := s.runChain(ctx, subject.Run)
	if errors.Is(err, errRunContinuing) {
		return handRefused(http.StatusConflict, refusalRunContinuing, "a run of the task's chain is being continued: send the package once it runs"), nil
	}
	if err != nil {
		return handGoal{}, err
	}
	forward := chainForward(nodes, subject.Run)
	newest := forward[len(forward)-1]
	run, held := s.runs.Get(newest)
	if !held || run.StoppedAt != nil || run.ResumedBy != "" {
		return handRefused(http.StatusConflict, refusalRunNotRunning, "The run is not running on this server. Continue it, then send the package: it goes to the continuation."), nil
	}
	if run.Managed == nil || len(run.Tasks) == 0 || run.Class == crew.ClassBrakeman {
		return handRefused(http.StatusConflict, refusalNotManaged, "the run has no tasks to hand a package to"), nil
	}
	if run.Compare != nil {
		return handRefused(http.StatusConflict, refusalComparison, "a comparison's lanes are handed alike: a package goes to no lane"), nil
	}
	i := slices.IndexFunc(run.Tasks, func(t crew.TaskState) bool { return t.ID == subject.Task })
	if i < 0 {
		return handRefused(http.StatusNotFound, refusalTaskNotFound, "the run's crew has no task "+subject.Task), nil
	}
	t := run.Tasks[i]
	target := &handTarget{Run: run.ID, RunName: run.Name, Task: t.ID, State: t.State}
	if t.Attempt != nil {
		target.Attempt = t.Attempt.N
		// Only an attempt the dispatch is still preparing: open, no
		// session, and still reserved (a dispatched attempt is reserved
		// until its launch; a Check again's attempt is verifying from its
		// start, asks for no package and has no task file).
		target.Starting = t.Attempt.EndedAt == nil && t.Attempt.SessionID == "" && t.Attempt.State == ledger.AttemptReserved
	}
	chain := []string{run.ID}
	for _, n := range nodes {
		if !slices.Contains(chain, n.id) {
			chain = append(chain, n.id)
		}
	}
	return handGoal{target: target, chain: chain}, nil
}

// sentRecordOf is a record as the routes answer it: notStarted set for a
// handed one whose attempt closed without its agent ever starting (no
// session), judged from the engine's run while it holds it, else from the
// ledger's row of that attempt (the ledger keeps every attempt of a
// managed run; a run's kept record keeps only each task's latest).
func (s *Server) sentRecordOf(ctx context.Context, h brakeman.SentHandoff) sentRecord {
	out := sentRecord{SentHandoff: h}
	if h.State != brakeman.HandoffHanded || len(h.Handed) == 0 {
		return out
	}
	last := h.Handed[len(h.Handed)-1]
	if _, attempts, err := s.runs.TaskOf(last.Run, h.Task); err == nil {
		for _, a := range attempts {
			if a.N == last.Attempt && a.EndedAt != nil && a.SessionID == "" {
				out.NotStarted = true
			}
		}
		return out
	} else if !errors.Is(err, crew.ErrRunNotFound) {
		return out
	}
	if s.ledger != nil {
		if rows, err := s.ledger.Attempts(ctx, last.Run); err == nil {
			for _, a := range rows {
				if a.TaskID == h.Task && a.N == last.Attempt {
					out.NotStarted = !a.ClosedAt.IsZero() && a.SessionID == ""
				}
			}
		}
	}
	return out
}

// handoffStoreError answers an error of the handoffs document.
func (s *Server) handoffStoreError(w http.ResponseWriter, err error) {
	var full *brakeman.FullError
	switch {
	case errors.As(err, &full):
		e := handFullError{Code: "handoffs_full", Message: fmt.Sprintf("%d handoff packages wait for their tasks: withdraw one from its brief, then send this one", len(full.Waiting))}
		for _, h := range full.Waiting {
			e.Waiting = append(e.Waiting, handWaiting{ID: h.ID, Request: h.Request, Run: h.Run, Task: h.Task})
		}
		writeJSON(w, http.StatusConflict, map[string]handFullError{"error": e})
	case errors.Is(err, brakeman.ErrHandoffNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such handoff package")
	case errors.Is(err, brakeman.ErrHandoffTooLarge):
		writeError(w, http.StatusConflict, refusalTooLarge, err.Error())
	case errors.Is(err, brakeman.ErrHandoffsDamaged):
		s.log.Warn("Brakeman's handoffs.json does not read: nothing is sent or handed from it", "err", err)
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "the sent handoff packages' record does not read: it is kept as it is")
	default:
		s.brakemanError(w, err)
	}
}

// writeHandJSON writes v without HTML escaping, as a task file holds the
// text, refused past limit (which no reply of the routes reaches).
func (s *Server) writeHandJSON(w http.ResponseWriter, status int, v any, limit int) {
	b, err := marshalNoEscape(v)
	if err == nil && len(b) > limit {
		err = fmt.Errorf("the reply is %d bytes, more than %d", len(b), limit)
	}
	if err != nil {
		s.log.Warn("a handoff package's reply could not be written", "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "the reply could not be written")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// handleHandPreview is GET .../items/{i}/hand: the exact text, where it
// would go now or why it cannot, and the newest record made from it.
func (s *Server) handleHandPreview(w http.ResponseWriter, r *http.Request) {
	if !handNoQuery(w, r) {
		return
	}
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	hi, ok := s.handItemOf(w, r, d)
	if !ok {
		return
	}
	goal, err := s.handGoalOf(r.Context(), hi.req)
	if err != nil {
		s.log.Warn("a handoff package's target could not be read", "request", hi.req.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "the run's chain could not be read")
		return
	}
	p := handPreview{Text: hi.text, Bytes: len(hi.text), SHA256: brakeman.TextSum(hi.text), Target: goal.target, Refusal: goal.refusal}
	if p.Bytes > brakeman.MaxHandoffText {
		p.Text, p.Target = "", nil
		p.Refusal = &handRefusal{Code: refusalTooLarge, Message: fmt.Sprintf("the package is %d bytes: a handoff package is sent at most %d KiB", p.Bytes, brakeman.MaxHandoffText>>10)}
	}
	h, found, err := d.st.HandoffOf(hi.req.ID, hi.item)
	if err != nil {
		s.handoffStoreError(w, err)
		return
	}
	if found {
		rec := s.sentRecordOf(r.Context(), h)
		p.Handoff = &rec
	}
	s.writeHandJSON(w, http.StatusOK, p, maxHandPreview)
}

// handleHand is POST .../items/{i}/hand: the send, under the person's op
// id, of the text whose SHA-256 the preview showed, to the target the
// request's subject resolves to now.
func (s *Server) handleHand(w http.ResponseWriter, r *http.Request) {
	if !handNoQuery(w, r) {
		return
	}
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	if ro, why := d.st.ReadOnly(); ro {
		writeError(w, http.StatusServiceUnavailable, "brakeman_not_owner", why)
		return
	}
	if !d.admitMake() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	defer d.madeDone()
	var body handBody
	if err := decodeJSONLimit(w, r, &body, maxHandBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxHandBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch {
	case !dispatch.OpIDPattern.MatchString(body.OpID):
		writeError(w, http.StatusBadRequest, "invalid_request", "opId is 1 to 32 of A-Z, a-z, 0-9, . _ ~ -")
		return
	case !sha256Pattern.MatchString(body.SHA256):
		writeError(w, http.StatusBadRequest, "invalid_request", "sha256 is the SHA-256 of the text the preview showed, in lower-case hex")
		return
	case !brakeman.HandoffByOK(body.By) || !crew.HandoffByOK(body.By):
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("by is one line of at most %d bytes, with no line end, tab, hidden character or space at either end", brakeman.MaxBy))
		return
	}
	hi, ok := s.handItemOf(w, r, d)
	if !ok {
		return
	}
	// The same op id again answers what it made, whatever the target is now.
	if h, found, err := d.st.HandoffByOp(body.OpID); err != nil {
		s.handoffStoreError(w, err)
		return
	} else if found {
		if h.Request != hi.req.ID || h.Item != hi.item {
			writeError(w, http.StatusConflict, "op_conflict", "the op id sent another item")
			return
		}
		s.writeHandJSON(w, http.StatusOK, handSent{Handoff: s.sentRecordOf(r.Context(), h)}, brakeman.MaxHandoffRecord+64)
		return
	}
	if len(hi.text) > brakeman.MaxHandoffText {
		writeError(w, http.StatusConflict, refusalTooLarge, fmt.Sprintf("the package is %d bytes: a handoff package is sent at most %d KiB", len(hi.text), brakeman.MaxHandoffText>>10))
		return
	}
	if brakeman.TextSum(hi.text) != body.SHA256 {
		writeError(w, http.StatusConflict, "text_changed", "the package's text is not the one shown: read it again before sending")
		return
	}
	goal, err := s.handGoalOf(r.Context(), hi.req)
	if err != nil {
		s.log.Warn("a handoff package's target could not be read", "request", hi.req.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "the run's chain could not be read")
		return
	}
	if goal.refusal != nil {
		writeError(w, goal.status, goal.refusal.Code, goal.refusal.Message)
		return
	}
	h, created, err := d.st.Send(brakeman.SendHandoff{OpID: body.OpID, Request: hi.req.ID, Item: hi.item, Run: goal.target.Run, Chain: goal.chain, Task: goal.target.Task,
		Actor: actorOf(r), By: body.By, Text: hi.text, SHA256: body.SHA256})
	if err != nil {
		s.handoffStoreError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		s.runs.Note(h.Run, session.ActivityStatus, fmt.Sprintf("handoff package %s sent %sfrom %s: waits for %s's next attempt", h.ID, byWords(h.By), h.Request, h.Task))
	}
	s.writeHandJSON(w, status, handSent{Handoff: s.sentRecordOf(r.Context(), h)}, brakeman.MaxHandoffRecord+64)
}

// byWords is "by <name> " for a name given, "" for none.
func byWords(by string) string {
	if by == "" {
		return ""
	}
	return "by " + by + " "
}

// handleHandWithdraw is POST /api/brakeman/handoffs/{hid}/withdraw.
func (s *Server) handleHandWithdraw(w http.ResponseWriter, r *http.Request) {
	if !handNoQuery(w, r) {
		return
	}
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	if ro, why := d.st.ReadOnly(); ro {
		writeError(w, http.StatusServiceUnavailable, "brakeman_not_owner", why)
		return
	}
	if !d.admitMake() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	defer d.madeDone()
	var body withdrawBody
	if err := decodeOptionalJSON(w, r, &body, maxHandBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxHandBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !brakeman.HandoffByOK(body.By) {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("by is one line of at most %d bytes, with no line end, tab, hidden character or space at either end", brakeman.MaxBy))
		return
	}
	hid := r.PathValue("hid")
	if !brakeman.ValidHandoffID(hid) {
		writeError(w, http.StatusNotFound, "not_found", "no such handoff package")
		return
	}
	h, withdrawn, err := d.st.Withdraw(hid, body.By)
	if err != nil {
		s.handoffStoreError(w, err)
		return
	}
	if withdrawn {
		s.runs.Note(h.Run, session.ActivityStatus, strings.TrimSpace(fmt.Sprintf("handoff package %s withdrawn %s", h.ID, byWords(h.EndedBy))))
	}
	s.writeHandJSON(w, http.StatusOK, handSent{Handoff: s.sentRecordOf(r.Context(), h)}, brakeman.MaxHandoffRecord+64)
}

// handoffFor is the engine's handoff function (wireContext): the package
// waiting for the attempt q names, taken from Brakeman's records, the
// take written before it answers (brakeman.Store.Take), so a package goes
// to one attempt. Nil while Brakeman's records are not this server's to
// write (a switchyard, no data directory, recovering, not the owner), for
// none, and for a record that does not hold the text it was sent with.
// The run's log says the take by ids and names; the text is never logged.
func (s *Server) handoffFor(ctx context.Context, q crew.HandoffQuery) *crew.TaskHandoff {
	if s.cfg.Switchyard.Enabled || s.brakeman == nil || s.brakeman.recovering.Load() {
		return nil
	}
	d := s.brakeman
	var th crew.TaskHandoff
	h, text, found, err := d.st.Take(brakeman.TakeQuery{Run: q.Run, Chain: q.Chain, Task: q.Task, Attempt: q.Attempt}, func(r brakeman.SentHandoff) bool {
		th = crew.TaskHandoff{ID: r.ID, By: r.By, SentAt: r.SentAt, Brief: r.Request, Item: r.Item, Text: r.Text}
		return th.Valid()
	})
	switch {
	case errors.Is(err, brakeman.ErrNotOwner):
		return nil
	case err != nil:
		s.log.Warn("a handoff package was not handed", "run", q.Run, "task", q.Task, "attempt", q.Attempt, "err", err)
		return nil
	case !found:
		return nil
	}
	th.Text = text
	s.runs.Note(q.Run, session.ActivityStatus, fmt.Sprintf("handoff package %s taken for %s #%d", h.ID, q.Task, q.Attempt))
	return &th
}
