package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Drafts made work, by a person (docs/plans/brakeman.md, Drafts made work;
// PR-4): POST /api/brakeman/requests/{id}/items/{i}/make turns a brief's
// draft_ticket/1 into a ticket through Dispatch's own operation under the
// actor admin, with the person's name, a draft_crew/1 into a saved crew
// through the crew store's CreateFrom, never launched, and a draft_plan/1
// (PR-10) into a proposed plan revision of its ticket through Dispatch's
// own propose route under the actor admin, with the person's name, which
// the operator alone then approves. Brakeman's own token reaches none of
// it. Each make is written before what it starts
// (rule 5 of Events, orderings and invariants): its input, then its claim
// (made pending, with the derived op id the backend keeps it under), then
// the backend's operation, then its outcome; a server that dies between
// leaves the claim, which the desk's recoverMakes completes from the
// records before Brakeman's routes serve.

// The states an edge reaches beside brakeman's own: a relation Dispatch
// refused (a cycle with a relation made since, a ticket deleted).
const edgeRefused = "refused"

// maxMakeBody bounds a make's body: the input's own bound and its fields.
const maxMakeBody = brakeman.MaxMakeDoc + 4<<10

// deriveOp is an operation id the desk passes on: "bk" and the first 30
// hex digits of the SHA-256 of its parts joined by NUL, 32 characters,
// within Dispatch's OpIDPattern and the ledger's CommandIDPattern and
// never a client's id with a suffix.
func deriveOp(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "bk" + hex.EncodeToString(sum[:])[:30]
}

// makeOp is a make's derived op id: its request, its item and the
// person's op id.
func makeOp(request string, item int, opID string) string {
	return deriveOp("make", request, strconv.Itoa(item), opID)
}

// edgeOp is an edge's derived op id: the same whichever end is made last,
// and never another request's.
func edgeOp(request string, from, to int) string {
	return deriveOp("edge", request, strconv.Itoa(from), strconv.Itoa(to))
}

// makeSeam, when a test sets it, runs at each stage of a make ("input"
// after its input is written, "claimed" after its claim, "waiting" when
// its completion waits for another of the item's, "locked" once its
// completion holds the item, "committed" after the backend's operation,
// "edge" before an edge's relation); an error it answers stops the make
// there, as a server that died would.
var makeSeam atomic.Pointer[func(stage, request string, item int) error]

func atMakeSeam(stage, request string, item int) error {
	if f := makeSeam.Load(); f != nil {
		return (*f)(stage, request, item)
	}
	return nil
}

// makeBody is the make route's body: the person's op id, their display
// name, and the draft as they edited it (the draft's own shape; the draft
// as Brakeman wrote it when absent).
type makeBody struct {
	OpID string          `json:"opId"`
	By   string          `json:"by,omitempty"`
	Edit json.RawMessage `json:"edit,omitempty"`
}

// makeInput is a make's canonical input, as written to its document and
// digested into its claim: what the backend is given.
type makeInput struct {
	Kind   string              `json:"kind"`
	By     string              `json:"by,omitempty"`
	Ticket *dispatch.NewTicket `json:"ticket,omitempty"`
	Crew   json.RawMessage     `json:"crew,omitempty"`
	Gates  []brakeman.Gate     `json:"gates,omitempty"`
	Plan   *planMake           `json:"plan,omitempty"`
	From   []brakeman.Ref      `json:"from,omitempty"`
}

// planMake is a draft plan revision's make as Dispatch's propose route is
// given it: the ticket's key and the proposal, the revision it revises
// resolved to its id and the comments to theirs when the make claimed it.
type planMake struct {
	Key     string               `json:"key"`
	Request dispatch.PlanRequest `json:"request"`
}

// makeLock is the desk's mutex for one request's makes, held across a
// make's input and its claim.
func (d *brakemanDesk) makeLock(id string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.makes == nil {
		d.makes = map[string]*sync.Mutex{}
	}
	m, ok := d.makes[id]
	if !ok {
		m = &sync.Mutex{}
		d.makes[id] = m
	}
	return m
}

// completionLock is the desk's lock for one item's completion
// (completeMake): a channel of one slot, taken by a send and given back
// by a receive, so a wait for it ends with its context.
func (d *brakemanDesk) completionLock(id string, item int) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completing == nil {
		d.completing = map[string]chan struct{}{}
	}
	key := id + "/" + strconv.Itoa(item)
	c, ok := d.completing[key]
	if !ok {
		c = make(chan struct{}, 1)
		d.completing[key] = c
	}
	return c
}

// handleBrakemanMake makes a brief's draft work: {opId, by?, edit?} → 201
// {request, made, ticket? | crew?}; the same opId again answers what it
// made (200); 409 already_made with the key or crew for another opId once
// made, 409 make_pending with the opId of a make under way, 409
// op_conflict for the same opId with other content, 409 make_superseded
// for the same opId rejoined while its make was settled and the item
// claimed again; 400 not_a_draft for
// any item but a draft ticket, a draft crew or a draft plan revision; 404 for a request pruned
// before the make claimed it, with no backend called; 503
// brakeman_not_owner on a server that does not hold Brakeman's owner lock.
func (s *Server) handleBrakemanMake(w http.ResponseWriter, r *http.Request) {
	d, ok := s.brakemanDeskOf(w)
	if !ok {
		return
	}
	if ro, why := d.st.ReadOnly(); ro {
		writeError(w, http.StatusServiceUnavailable, "brakeman_not_owner", why)
		return
	}
	// Admitted while the desk is open, and waited for by release before
	// the owner lock goes: a make never runs its backend's operation
	// beside the next owner's recovery of its claim.
	if !d.admitMake() {
		s.brakemanError(w, errDeskClosing)
		return
	}
	defer d.madeDone()
	var body makeBody
	if err := decodeJSONLimit(w, r, &body, maxMakeBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxMakeBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := r.PathValue("id")
	item, err := strconv.Atoi(r.PathValue("i"))
	okItem := err == nil && item >= 0 && strconv.Itoa(item) == r.PathValue("i")
	if !brakemanIDPattern(id) || !okItem || item >= brakeman.MaxItems {
		writeError(w, http.StatusNotFound, "not_found", "no such request or item")
		return
	}
	by := strings.TrimSpace(body.By)
	switch {
	case !dispatch.OpIDPattern.MatchString(body.OpID):
		writeError(w, http.StatusBadRequest, "invalid_request", "opId is 1 to 32 of A-Z, a-z, 0-9, . _ ~ -")
		return
	case len(by) > brakeman.MaxBy || !utf8.ValidString(by) || strings.ContainsFunc(by, unicode.IsControl):
		// Dispatch's display name holds no control character: a name it
		// refuses would leave the claim pending for good.
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("by is at most %d bytes with no control character", brakeman.MaxBy))
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !req.HasBrief {
		writeError(w, http.StatusConflict, "no_brief", "the request has no brief to make anything from")
		return
	}
	b, _, err := d.st.Brief(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if item >= len(b.Items) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("the brief has %d items", len(b.Items)))
		return
	}
	in, err := s.makeInputOf(r.Context(), b, item, body.Edit, by)
	var inv *brakeman.Invalid
	var unopened *errRefStore
	switch {
	case err == nil:
	case errors.As(err, &inv):
		writeDraftError(w, err)
		return
	case errors.As(err, &unopened):
		// A draft plan revision's basedOn is read from Dispatch's store.
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", unopened.Error())
		return
	default:
		s.log.Warn("a Brakeman draft's input could not be read", "request", id, "item", item, "err", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "the draft's input could not be read")
		return
	}
	raw, err := marshalNoEscape(in)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if len(raw) > brakeman.MaxMakeDoc {
		writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the make's input is more than %d MiB", brakeman.MaxMakeDoc>>20))
		return
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	op := makeOp(id, item, body.OpID)

	// One make of an item at a time: the claim decides, under the
	// request's mutex, before any backend runs.
	lock := d.makeLock(id)
	lock.Lock()
	cur, err := d.st.Get(id)
	if err != nil {
		lock.Unlock()
		s.brakemanError(w, err)
		return
	}
	if i := slices.IndexFunc(cur.Made, func(m brakeman.Made) bool { return m.Item == item }); i >= 0 {
		m := cur.Made[i]
		switch m.State {
		case brakeman.MadeDone, brakeman.MadeDeleted:
			lock.Unlock()
			if m.OpID != body.OpID {
				writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "already_made", "message": "the item was made already", "key": m.Key, "crew": m.Crew, "state": m.State}, "made": m})
				return
			}
			s.answerMade(w, r, http.StatusOK, cur, m)
			return
		case brakeman.MadePending:
			if m.OpID != body.OpID {
				lock.Unlock()
				writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "make_pending", "message": "another make of the item is under way", "opId": m.OpID}})
				return
			}
			if m.Input != digest {
				lock.Unlock()
				writeError(w, http.StatusConflict, "op_conflict", "the op id was used for this item with other content")
				return
			}
			// The same make again: rejoined, its claim and input as they
			// stand; the backend answers the derived op once.
			lock.Unlock()
			s.finishMake(w, r, d, id, m, http.StatusOK)
			return
		}
	}
	// Only a new claim is held to what the server holds now (a crew's
	// agents in the catalog): a make done, deleted or under way answers
	// as it was made, whatever changed since.
	if err := s.checkMakeNow(in); err != nil {
		lock.Unlock()
		writeDraftError(w, err)
		return
	}
	if err := d.st.PutMakeInput(id, item, raw); err != nil {
		lock.Unlock()
		s.brakemanError(w, err)
		return
	}
	if err := atMakeSeam("input", id, item); err != nil {
		lock.Unlock()
		writeError(w, http.StatusInternalServerError, "make_failed", err.Error())
		return
	}
	claim := brakeman.Made{Item: item, OpID: body.OpID, Op: op, Input: digest, State: brakeman.MadePending, By: by, At: d.now().UTC()}
	claimed, err := d.st.Update(id, func(q *brakeman.Request) error {
		q.Made = slices.DeleteFunc(q.Made, func(m brakeman.Made) bool { return m.Item == item })
		q.Made = append(q.Made, claim)
		if len(q.Edges) == 0 {
			q.Edges = briefEdges(id, b)
		}
		return nil
	})
	lock.Unlock()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	_ = claimed
	if err := atMakeSeam("claimed", id, item); err != nil {
		writeError(w, http.StatusInternalServerError, "make_failed", err.Error())
		return
	}
	s.finishMake(w, r, d, id, claim, http.StatusCreated)
}

// finishMake completes a claimed make and answers it.
func (s *Server) finishMake(w http.ResponseWriter, r *http.Request, d *brakemanDesk, id string, m brakeman.Made, status int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	done, err := s.completeMake(ctx, d, id, m)
	if err != nil {
		var unopened *errRefStore
		var superseded *errMakeSuperseded
		switch {
		case errors.As(err, &unopened):
			writeError(w, http.StatusServiceUnavailable, "store_unavailable", unopened.Error())
			return
		case errors.As(err, &superseded):
			// The make this one rejoined was settled meanwhile and the
			// item claimed again: nothing ran for it, and the item's
			// entry is the other claim's.
			out := map[string]any{"error": map[string]any{"code": "make_superseded", "message": superseded.Error()}}
			if superseded.now.OpID != "" {
				out["made"] = superseded.now
			}
			writeJSON(w, http.StatusConflict, out)
			return
		case errors.Is(err, errDeskClosing):
			// The server began to shut down during the make: its outcome
			// is the next start's recovery's, from the backend's receipt.
			s.brakemanError(w, err)
			return
		}
		s.log.Warn("a Brakeman draft's make did not finish", "request", id, "item", m.Item, "err", err)
		writeError(w, http.StatusInternalServerError, "make_failed", "the make did not finish: it is completed when it is asked again with the same opId, or at the next start")
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if done.State == brakeman.MadeRefused && status == http.StatusCreated {
		status = http.StatusConflict
	}
	s.answerMade(w, r, status, req, done)
}

// answerMade answers a make: the request, its entry, and the ticket or
// crew it made.
func (s *Server) answerMade(w http.ResponseWriter, r *http.Request, status int, req brakeman.Request, m brakeman.Made) {
	out := map[string]any{"request": req, "made": m}
	switch {
	case m.Key != "" && m.Plan == "" && s.dispatch != nil:
		if rc, ok, err := s.dispatch.Receipt(r.Context(), dispatch.ActorAdmin, m.Op); err == nil && ok {
			out["receipt"] = rc
		}
		out["ticket"] = map[string]any{"key": m.Key, "url": ticketURL(m.Key)}
	case m.Crew != "":
		out["crew"] = map[string]any{"id": m.Crew, "url": "/crews/" + m.Crew}
	case m.Plan != "" && s.dispatch != nil:
		// The revision as the plans' route lists it, waiting for the
		// operator's approval.
		if p, err := s.planReplies(s.dispatch).byID(r.Context(), m.Plan, false); err == nil {
			out["plan"] = p
		}
		if rc, ok, err := s.dispatch.Receipt(r.Context(), dispatch.ActorAdmin, m.Op); err == nil && ok {
			out["receipt"] = rc
		}
		out["ticket"] = map[string]any{"key": m.Key, "url": ticketURL(m.Key)}
	}
	if m.State == brakeman.MadeRefused {
		out["error"] = map[string]any{"code": "refused", "message": m.Why}
	}
	writeJSON(w, status, out)
}

// briefEdges is the edges a brief's draft tickets' blocks make, each with
// its derived op, pending.
func briefEdges(request string, b brakeman.Brief) []brakeman.Edge {
	var out []brakeman.Edge
	for i, it := range b.Items {
		if it.Kind != brakeman.ItemDraftTicket || it.DraftTicket == nil {
			continue
		}
		for _, k := range it.DraftTicket.Blocks {
			if len(out) == brakeman.MaxEdges {
				return out
			}
			out = append(out, brakeman.Edge{From: i, To: k, Op: edgeOp(request, i, k), State: brakeman.EdgePending})
		}
	}
	return out
}

// writeDraftError answers a make's input that is not a draft (400
// not_a_draft) or not a valid one (400 invalid_draft, with its field).
func writeDraftError(w http.ResponseWriter, err error) {
	var inv *brakeman.Invalid
	switch {
	case errors.As(err, &inv) && inv.Code == "not_a_draft":
		writeError(w, http.StatusBadRequest, "not_a_draft", inv.Why)
	case errors.As(err, &inv):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "invalid_draft", "message": inv.Error(), "field": inv.Field}})
	default:
		writeError(w, http.StatusBadRequest, "invalid_draft", err.Error())
	}
}

// checkMakeNow holds a new claim's input to what the server holds now: a
// crew's body checked as POST /api/crews checks it, its agents in the
// catalog among it. A ticket's fields are the draft's own bounds, checked
// with its input (makeInputOf).
func (s *Server) checkMakeNow(in makeInput) error {
	if in.Kind != brakeman.ItemDraftCrew {
		return nil
	}
	if err := (brakemanFacts{s: s}).CheckCrew(in.Crew); err != nil {
		return &brakeman.Invalid{Code: "invalid_draft", Field: "crew", Why: err.Error()}
	}
	return nil
}

// makeInputOf is the canonical input of a make of the brief's item, the
// draft as edited (edit, its own shape) or as written: a ticket's fields
// held to the draft's bounds, a crew's body as the JSON it is (checked
// against the catalog, as POST /api/crews checks it, only for a new
// claim: checkMakeNow), a plan revision's proposal with the revision it
// revises and its comments named by their ids (planMakeOf). An
// *brakeman.Invalid with Code not_a_draft for any other item.
func (s *Server) makeInputOf(ctx context.Context, b brakeman.Brief, item int, edit json.RawMessage, by string) (makeInput, error) {
	it := b.Items[item]
	switch it.Kind {
	case brakeman.ItemDraftTicket:
		x := *it.DraftTicket
		if len(edit) > 0 {
			x = brakeman.DraftTicket{}
			if err := store.DecodeStrict(edit, &x); err != nil {
				return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "edit", Why: err.Error()}
			}
		}
		if err := checkTicketDraft(x); err != nil {
			return makeInput{}, err
		}
		return makeInput{Kind: it.Kind, By: by, From: x.From, Ticket: &dispatch.NewTicket{Project: x.Project, Title: strings.TrimSpace(x.Title), Description: x.Description, Acceptance: x.Acceptance, Priority: x.Priority}}, nil
	case brakeman.ItemDraftCrew:
		x := *it.DraftCrew
		if len(edit) > 0 {
			x = brakeman.DraftCrew{}
			if err := store.DecodeStrict(edit, &x); err != nil {
				return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "edit", Why: err.Error()}
			}
		}
		if len(x.Crew) == 0 {
			return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "crew", Why: "a draft crew is the body POST /api/crews takes"}
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, x.Crew); err != nil {
			return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "crew", Why: err.Error()}
		}
		return makeInput{Kind: it.Kind, By: by, Crew: compact.Bytes(), Gates: x.Gates, From: x.From}, nil
	case brakeman.ItemDraftPlan:
		x := *it.DraftPlan
		if len(edit) > 0 {
			x = brakeman.DraftPlan{}
			if err := store.DecodeStrict(edit, &x); err != nil {
				return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "edit", Why: err.Error()}
			}
			if err := brakeman.CheckDraftPlan(&x); err != nil {
				return makeInput{}, err
			}
		}
		pm, err := s.planMakeOf(ctx, x)
		if err != nil {
			return makeInput{}, err
		}
		return makeInput{Kind: it.Kind, By: by, Plan: &pm, From: x.From}, nil
	}
	return makeInput{}, &brakeman.Invalid{Code: "not_a_draft", Field: "item", Why: fmt.Sprintf("item %d is %s: only a draft_ticket/1, a draft_crew/1 or a draft_plan/1 is made work", item, it.Kind)}
}

// planMakeOf is a draft plan revision as Dispatch's propose route takes
// it: its ticket's key, the crew, the base, the revision it revises (a
// plan: ref of the same ticket, by its id now), the references, the
// attachments, the comments (comment: refs of the same ticket, by their
// ids), the constraints, the decisions and the note.
func (s *Server) planMakeOf(ctx context.Context, x brakeman.DraftPlan) (planMake, error) {
	bad := func(field, why string, a ...any) error {
		return &brakeman.Invalid{Code: "invalid_draft", Field: field, Why: fmt.Sprintf(why, a...)}
	}
	key := x.Ticket.Ticket
	req := dispatch.PlanRequest{Crew: x.Crew, Base: x.Base, Constraints: x.Constraints, Note: x.Note,
		References: []dispatch.PlanReferenceRequest{}, Attachments: slices.Clone(x.Attachments), Comments: []string{}, Decisions: []dispatch.PlanDecision{}}
	for _, r := range x.References {
		req.References = append(req.References, dispatch.PlanReferenceRequest{Path: r.Path, Start: r.Start, End: r.End, Note: r.Note})
	}
	if req.Attachments == nil {
		req.Attachments = []string{}
	}
	for i, c := range x.Comments {
		if c.Ticket != key {
			return planMake{}, bad(fmt.Sprintf("comments[%d]", i), "%s is not a comment of %s: a revision hands over its own ticket's comments", c, key)
		}
		req.Comments = append(req.Comments, c.Comment)
	}
	for _, d := range x.Decisions {
		before := slices.Clone(d.Before)
		if before == nil {
			before = []string{}
		}
		req.Decisions = append(req.Decisions, dispatch.PlanDecision{ID: d.ID, Question: d.Question, Kind: d.Kind, Before: before})
	}
	if x.BasedOn != nil {
		if x.BasedOn.Ticket != key {
			return planMake{}, bad("basedOn", "%s is not a revision of %s", x.BasedOn, key)
		}
		ds := s.dispatch
		if ds == nil {
			return planMake{}, &errRefStore{what: "Dispatch's store"}
		}
		plans, err := ds.Plans(ctx, key)
		if err != nil && !errors.Is(err, dispatch.ErrNotFound) {
			return planMake{}, err
		}
		i := slices.IndexFunc(plans, func(p dispatch.PlanSummary) bool { return p.N == x.BasedOn.N })
		if i < 0 {
			return planMake{}, bad("basedOn", "%s has no revision %d", key, x.BasedOn.N)
		}
		req.BasedOn = plans[i].ID
	}
	return planMake{Key: key, Request: req}, nil
}

// checkTicketDraft holds an edited draft ticket to the draft's own bounds.
func checkTicketDraft(x brakeman.DraftTicket) error {
	bad := func(field, why string) error { return &brakeman.Invalid{Code: "invalid_draft", Field: field, Why: why} }
	title := strings.TrimSpace(x.Title)
	switch {
	case !dispatch.ValidKey(x.Project):
		return bad("project", "a project's key")
	case title == "" || utf8.RuneCountInString(title) > brakeman.MaxTitle:
		return bad("title", fmt.Sprintf("1 to %d characters", brakeman.MaxTitle))
	case len(x.Description) > 16<<10:
		return bad("description", "at most 16 KiB")
	case len(x.Acceptance) > 8<<10:
		return bad("acceptance", "at most 8 KiB")
	case x.Priority < 0 || x.Priority > 4:
		return bad("priority", "0 to 4")
	}
	return nil
}

// completeMake runs a claimed make's backend operation from its recorded
// input and writes its outcome, deciding each case from a durable record
// (rule 5): a ticket's receipt under (admin, op), else the ticket the
// change log names for it (the receipt pruned), else the operation never
// ran and runs now with the same op, which Dispatch answers once; a
// crew's by its origin, its tombstone, or a save under it. Then each edge
// whose two ends are tickets is recorded. An error leaves the claim
// pending, for the next ask or recoverMakes.
//
// One completion of an item runs at a time (completionLock), and one
// that waited for another reads the item's entry again and answers it
// when that one settled it: the same make asked again while it runs
// would otherwise run the backend beside it, and a refusal one of them
// read (a draft plan's crew deleted after the other had read it) could
// settle the claim refused while the other's proposal commits. Before it
// runs the backend it holds the item's claim to the one it was given:
// still pending, under the same opId, op and input digest (a claim's
// input is never rewritten while the claim stands, so the input read
// next is that claim's). A completion that waited while the make it
// rejoined was settled (refused, which leaves no receipt) and the item
// was claimed again under another opId or other content answers
// errMakeSuperseded and runs nothing: it would otherwise propose the new
// claim's input under the old claim's op, which the new claim's outcome
// never records, so the new claim's own completion would make it twice.
func (s *Server) completeMake(ctx context.Context, d *brakemanDesk, id string, m brakeman.Made) (brakeman.Made, error) {
	lock := d.completionLock(id, m.Item)
	select {
	case lock <- struct{}{}:
	default:
		if err := atMakeSeam("waiting", id, m.Item); err != nil {
			return m, err
		}
		select {
		case lock <- struct{}{}:
		case <-ctx.Done():
			return m, ctx.Err()
		}
	}
	defer func() { <-lock }()
	if err := atMakeSeam("locked", id, m.Item); err != nil {
		return m, err
	}
	cur, err := d.st.Get(id)
	if err != nil {
		return m, err
	}
	i := slices.IndexFunc(cur.Made, func(x brakeman.Made) bool { return x.Item == m.Item })
	switch {
	case i < 0:
		return m, &errMakeSuperseded{}
	case cur.Made[i].OpID != m.OpID || cur.Made[i].Op != m.Op || cur.Made[i].Input != m.Input:
		return m, &errMakeSuperseded{now: cur.Made[i]}
	case cur.Made[i].State != brakeman.MadePending:
		return cur.Made[i], nil
	}
	raw, err := d.st.MakeInput(id, m.Item)
	if err != nil {
		return m, fmt.Errorf("the make's input: %w", err)
	}
	var in makeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return m, fmt.Errorf("the make's input: %w", err)
	}
	out := m
	switch in.Kind {
	case brakeman.ItemDraftTicket:
		ds := s.dispatch
		if ds == nil || in.Ticket == nil {
			return m, &errRefStore{what: "Dispatch's store"}
		}
		rc, found, err := ds.Receipt(ctx, dispatch.ActorAdmin, m.Op)
		if err != nil {
			return m, err
		}
		if !found {
			key, named, err := ds.OpTicket(ctx, dispatch.ActorAdmin, m.Op)
			if err != nil {
				return m, err
			}
			if named {
				rc, found = dispatch.Receipt{OpID: m.Op, Status: dispatch.StatusDone, Key: key}, true
			}
		}
		if !found {
			rc, err = ds.CreateTicket(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: m.Op, By: in.By}, *in.Ticket)
			switch {
			case errors.Is(err, dispatch.ErrBadBy), errors.Is(err, dispatch.ErrBadOpID), errors.Is(err, dispatch.ErrBadActor):
				// The same input is refused every time: settled refused,
				// so the item can be made again, never left pending.
				rc, err = dispatch.Receipt{OpID: m.Op, Status: dispatch.StatusFailed, Error: err.Error()}, nil
			case err != nil:
				return m, err
			}
		}
		if err := atMakeSeam("committed", id, m.Item); err != nil {
			return m, err
		}
		if rc.Status == dispatch.StatusDone {
			out.State, out.Key, out.Rev, out.Why = brakeman.MadeDone, rc.Key, rc.Rev, ""
		} else {
			out.State, out.Why = brakeman.MadeRefused, cutBytes(cmpOr(rc.Error, rc.Refusal, "Dispatch refused it"), brakeman.MaxWhy)
		}
	case brakeman.ItemDraftCrew:
		if s.crews == nil {
			return m, &errRefStore{what: "the crew store"}
		}
		c, err := crewOfBody(in.Crew)
		if err != nil {
			out.State, out.Why = brakeman.MadeRefused, cutBytes(err.Error(), brakeman.MaxWhy)
			break
		}
		origin := "brakeman:" + id + "/" + strconv.Itoa(m.Item) + "/" + m.Op
		saved, _, err := s.crews.CreateFrom(c, origin)
		switch {
		case errors.Is(err, crew.ErrOriginDeleted):
			out.State, out.Why = brakeman.MadeDeleted, cutBytes(err.Error(), brakeman.MaxWhy)
		case errors.Is(err, crew.ErrInvalid):
			out.State, out.Why = brakeman.MadeRefused, cutBytes(err.Error(), brakeman.MaxWhy)
		case err != nil:
			return m, err
		default:
			if err := atMakeSeam("committed", id, m.Item); err != nil {
				return m, err
			}
			out.State, out.Crew, out.Rev, out.Why = brakeman.MadeDone, saved.ID, saved.ManifestRevision, ""
		}
	case brakeman.ItemDraftPlan:
		if s.dispatch == nil || in.Plan == nil {
			return m, &errRefStore{what: "Dispatch's store"}
		}
		res, err := s.proposeAsOperator(ctx, *in.Plan, m.Op, in.By)
		if err != nil {
			return m, err
		}
		if err := atMakeSeam("committed", id, m.Item); err != nil {
			return m, err
		}
		if res.done {
			out.State, out.Key, out.Plan, out.Rev, out.Why = brakeman.MadeDone, in.Plan.Key, res.plan, res.rev, ""
		} else {
			out.State, out.Why = brakeman.MadeRefused, cutBytes(res.why, brakeman.MaxWhy)
		}
	default:
		return m, fmt.Errorf("the make's input is of %q", in.Kind)
	}
	out.At = d.now().UTC()
	// The outcome is held to the entry's bound whole (its words cut to
	// the room its other fields leave), so the write never refuses it
	// and leaves the claim pending.
	out = out.Fit()
	if _, err := d.update(id, func(q *brakeman.Request) error {
		i := slices.IndexFunc(q.Made, func(x brakeman.Made) bool {
			return x.Item == m.Item && x.OpID == m.OpID && x.State == brakeman.MadePending
		})
		if i >= 0 {
			q.Made[i] = out
		}
		return nil
	}); err != nil {
		return m, err
	}
	if out.State == brakeman.MadeDone && in.Kind == brakeman.ItemDraftTicket {
		if err := s.completeEdges(ctx, d, id); err != nil {
			s.log.Warn("a Brakeman draft's blocks could not be recorded yet", "request", id, "err", err)
		}
	}
	return out, nil
}

// errMakeSuperseded is a completion whose claim is no longer the item's:
// it was settled and the item claimed again (another opId, or the same
// with other content) before the completion held the item. now is the
// item's entry as it stands, zero when it has none.
type errMakeSuperseded struct{ now brakeman.Made }

func (e *errMakeSuperseded) Error() string {
	return "the make was settled and the item claimed again since"
}

// proposed is what Dispatch's propose route answered a draft plan
// revision's make: done with the revision's id and rev, or refused with
// its words.
type proposed struct {
	done bool
	plan string
	rev  int64
	why  string
}

// proposeAsOperator proposes a draft plan revision through Dispatch's own
// propose route (handleDispatchProposePlan: its replay first, then the
// crew, git and the store's checks), as the workbench token's person
// sends it: under the actor admin, the operator, with the person's name
// as by and op, the make's derived id, as the operation's, so a make
// asked again, or recovered after a crash, is answered from its receipt
// and never proposes twice. Brakeman's principal is never its caller. A
// refusal (a 4xx: no such crew, one without tasks, a comment not the
// ticket's, a stale revision) settles the make refused; a failure of the
// server's own (a 5xx: git, the store) is an error, the claim left
// pending.
func (s *Server) proposeAsOperator(ctx context.Context, pm planMake, op, by string) (proposed, error) {
	body := map[string]any{"opId": op}
	if by != "" {
		body["byName"] = by
	}
	raw, err := json.Marshal(pm.Request)
	if err != nil {
		return proposed{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return proposed{}, err
	}
	for k, v := range fields {
		body[k] = v
	}
	b, err := marshalNoEscape(body)
	if err != nil {
		return proposed{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/dispatch/tickets/"+url.PathEscape(pm.Key)+"/plans", bytes.NewReader(b))
	if err != nil {
		return proposed{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("key", pm.Key)
	req = withPrincipal(req, principal{admin: true})
	rec := newReplyRecorder()
	s.handleDispatchProposePlan(rec, req)
	var reply struct {
		Receipt *dispatch.Receipt `json:"receipt"`
		Plan    *struct {
			ID  string `json:"id"`
			Rev int64  `json:"rev"`
		} `json:"plan"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &reply)
	switch {
	case rec.status >= 500:
		msg := http.StatusText(rec.status)
		if reply.Error != nil {
			msg = reply.Error.Code + ": " + reply.Error.Message
		}
		return proposed{}, fmt.Errorf("the propose route of Dispatch answered %d: %s", rec.status, msg)
	case rec.status >= 400:
		why := "Dispatch refused the proposal"
		if reply.Error != nil {
			why = cmpOr(reply.Error.Message, reply.Error.Code, why)
		}
		return proposed{why: why}, nil
	case reply.Receipt == nil || reply.Receipt.Status != dispatch.StatusDone:
		return proposed{}, fmt.Errorf("the propose route of Dispatch answered %d with no receipt done", rec.status)
	}
	p := proposed{done: true, plan: reply.Receipt.ID}
	if reply.Plan != nil {
		p.plan, p.rev = cmpOr(reply.Plan.ID, p.plan), reply.Plan.Rev
	}
	return p, nil
}

// crewOfBody is the crew a draft crew's body makes, decoded strictly as
// POST /api/crews decodes its body, by the one decoder of an agent's
// members (decodeAgentMembers): a draft names no account and no binding,
// at its check (CheckCrew) and at its making alike.
func crewOfBody(body json.RawMessage) (crew.Crew, error) {
	var in crewInput
	if aerr := decodeAgentMembers(body, &in); aerr != nil {
		return crew.Crew{}, aerr
	}
	return in.crew(), nil
}

// completeEdges records each pending edge of the request whose two ends
// are tickets now, as one blocks relation under the edge's derived op:
// its receipt again when Dispatch keeps one, else the relation added.
func (s *Server) completeEdges(ctx context.Context, d *brakemanDesk, id string) error {
	ds := s.dispatch
	if ds == nil {
		return &errRefStore{what: "Dispatch's store"}
	}
	req, err := d.st.Get(id)
	if err != nil {
		return err
	}
	keyOf := func(item int) string {
		i := slices.IndexFunc(req.Made, func(m brakeman.Made) bool { return m.Item == item && m.State == brakeman.MadeDone })
		if i < 0 {
			return ""
		}
		return req.Made[i].Key
	}
	by := func(item int) string {
		i := slices.IndexFunc(req.Made, func(m brakeman.Made) bool { return m.Item == item })
		if i < 0 {
			return ""
		}
		return req.Made[i].By
	}
	for _, e := range req.Edges {
		from, to := keyOf(e.From), keyOf(e.To)
		if e.State != brakeman.EdgePending || from == "" || to == "" {
			continue
		}
		if err := atMakeSeam("edge", id, e.From); err != nil {
			return err
		}
		rc, found, err := ds.Receipt(ctx, dispatch.ActorAdmin, e.Op)
		if err != nil {
			return err
		}
		if !found {
			if rc, err = ds.AddRelation(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: e.Op, By: cmpOr(by(e.To), by(e.From))}, from, dispatch.RelationBlocks, to); err != nil {
				return err
			}
		}
		state := brakeman.EdgeMade
		if rc.Status != dispatch.StatusDone {
			state = edgeRefused
		}
		if _, err := d.st.Update(id, func(q *brakeman.Request) error {
			for i := range q.Edges {
				if q.Edges[i].Op == e.Op && q.Edges[i].State == brakeman.EdgePending {
					q.Edges[i].State = state
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// recoverMakes completes what the last owner left of the makes: each
// pending claim from its recorded input, and each edge whose two ends are
// tickets, deciding each from a durable record (completeMake); and, as
// recoverActs, each act left pending, from its operation's own store
// (completeAct). It runs
// once the desk's store holds the owner lock, before Brakeman's routes
// serve; what it cannot complete now (Dispatch not open) is left pending
// and logged.
func (d *brakemanDesk) recoverMakes(ctx context.Context) {
	pending, err := d.st.Pending()
	if err != nil {
		d.s.log.Warn("Brakeman's pending makes could not be listed", "err", err)
		return
	}
	edges := map[string]bool{}
	for _, p := range pending {
		if ctx.Err() != nil {
			// The desk is closing: the rest is the next start's.
			return
		}
		switch {
		case p.Made != nil:
			var superseded *errMakeSuperseded
			if _, err := d.s.completeMake(ctx, d, p.Request, *p.Made); err != nil && !errors.As(err, &superseded) {
				d.s.log.Warn("a Brakeman draft's make could not be recovered yet", "request", p.Request, "item", p.Made.Item, "err", err)
			}
		case p.Edge != nil && !edges[p.Request]:
			edges[p.Request] = true
			if err := d.s.completeEdges(ctx, d, p.Request); err != nil {
				d.s.log.Warn("a Brakeman draft's blocks could not be recovered yet", "request", p.Request, "err", err)
			}
		case p.Act != nil:
			// recoverActs (brakeman_act.go): the act's outcome from its
			// operation's own store.
			if err := d.s.completeAct(ctx, d, p.Request, *p.Act); err != nil {
				d.s.log.Warn("a Brakeman act's outcome could not be recovered yet", "request", p.Request, "opId", p.Act.OpID, "err", err)
			}
		}
	}
}

// recover runs recoverMakes with Brakeman's routes held (503
// brakeman_recovering) until it is done.
func (d *brakemanDesk) recover() {
	d.recovering.Store(true)
	defer d.recovering.Store(false)
	ctx, cancel := context.WithTimeout(d.recoverCtx, 2*time.Minute)
	defer cancel()
	d.recoverMakes(ctx)
}
