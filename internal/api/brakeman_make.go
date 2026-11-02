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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Drafts made work, by a person (docs/plans/brakeman.md, Drafts made work;
// PR-4): POST /api/brakeman/requests/{id}/items/{i}/make turns a brief's
// draft_ticket/1 into a ticket through Dispatch's own operation under the
// actor admin, with the person's name, and a draft_crew/1 into a saved
// crew through the crew store's CreateFrom, never launched. Brakeman's own
// token reaches none of it. Each make is written before what it starts
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
// after its input is written, "claimed" after its claim, "committed"
// after the backend's operation, "edge" before an edge's relation); an
// error it answers stops the make there, as a server that died would.
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
	From   []brakeman.Ref      `json:"from,omitempty"`
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

// handleBrakemanMake makes a brief's draft work: {opId, by?, edit?} → 201
// {request, made, ticket? | crew?}; the same opId again answers what it
// made (200); 409 already_made with the key or crew for another opId once
// made, 409 make_pending with the opId of a make under way, 409
// op_conflict for the same opId with other content; 400 not_a_draft for
// any item but a draft ticket or a draft crew; 404 for a request pruned
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
	case len(by) > brakeman.MaxBy || strings.ContainsAny(by, "\n\t") || !utf8.ValidString(by):
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("by is one line of at most %d bytes", brakeman.MaxBy))
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
	in, err := s.makeInputOf(b, item, body.Edit, by)
	var inv *brakeman.Invalid
	switch {
	case errors.As(err, &inv) && inv.Code == "not_a_draft":
		writeError(w, http.StatusBadRequest, "not_a_draft", inv.Why)
		return
	case errors.As(err, &inv):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "invalid_draft", "message": inv.Error(), "field": inv.Field}})
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_draft", err.Error())
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
		if errors.As(err, &unopened) {
			writeError(w, http.StatusServiceUnavailable, "store_unavailable", unopened.Error())
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
	case m.Key != "" && s.dispatch != nil:
		if rc, ok, err := s.dispatch.Receipt(r.Context(), dispatch.ActorAdmin, m.Op); err == nil && ok {
			out["receipt"] = rc
		}
		out["ticket"] = map[string]any{"key": m.Key, "url": ticketURL(m.Key, "")}
	case m.Crew != "":
		out["crew"] = map[string]any{"id": m.Crew, "url": "/crews/" + m.Crew}
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

// makeInputOf is the canonical input of a make of the brief's item, the
// draft as edited (edit, its own shape) or as written: a ticket's fields
// held to the draft's bounds, a crew's body checked as POST /api/crews
// checks it. An *brakeman.Invalid with Code not_a_draft for any other
// item.
func (s *Server) makeInputOf(b brakeman.Brief, item int, edit json.RawMessage, by string) (makeInput, error) {
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
		if err := (brakemanFacts{s: s}).CheckCrew(x.Crew); err != nil {
			return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "crew", Why: err.Error()}
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, x.Crew); err != nil {
			return makeInput{}, &brakeman.Invalid{Code: "invalid_draft", Field: "crew", Why: err.Error()}
		}
		return makeInput{Kind: it.Kind, By: by, Crew: compact.Bytes(), Gates: x.Gates, From: x.From}, nil
	}
	return makeInput{}, &brakeman.Invalid{Code: "not_a_draft", Field: "item", Why: fmt.Sprintf("item %d is %s: only a draft_ticket/1 or a draft_crew/1 is made work", item, it.Kind)}
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
func (s *Server) completeMake(ctx context.Context, d *brakemanDesk, id string, m brakeman.Made) (brakeman.Made, error) {
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
			if rc, err = ds.CreateTicket(ctx, dispatch.Op{Actor: dispatch.ActorAdmin, ID: m.Op, By: in.By}, *in.Ticket); err != nil {
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
	default:
		return m, fmt.Errorf("the make's input is of %q", in.Kind)
	}
	out.At = d.now().UTC()
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

// crewOfBody is the crew a draft crew's body makes, decoded strictly as
// POST /api/crews decodes its body.
func crewOfBody(body json.RawMessage) (crew.Crew, error) {
	var in crewInput
	if err := store.DecodeStrict(body, &in); err != nil {
		return crew.Crew{}, err
	}
	return crew.Crew{Name: strings.TrimSpace(in.Name), Goal: in.Goal, Cwd: in.Cwd, Where: in.Where, Isolation: in.Isolation,
		OpenAfterLaunch: in.OpenAfterLaunch, ViewLinkTTLSeconds: in.ViewLinkTTLSeconds, Yolo: in.Yolo, Members: in.Members, Tasks: in.Tasks, Setup: in.Setup,
		Manifest: in.Manifest}, nil
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
// tickets, deciding each from a durable record (completeMake). It runs
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
		switch {
		case p.Made != nil:
			if _, err := d.s.completeMake(ctx, d, p.Request, *p.Made); err != nil {
				d.s.log.Warn("a Brakeman draft's make could not be recovered yet", "request", p.Request, "item", p.Made.Item, "err", err)
			}
		case p.Edge != nil && !edges[p.Request]:
			edges[p.Request] = true
			if err := d.s.completeEdges(ctx, d, p.Request); err != nil {
				d.s.log.Warn("a Brakeman draft's blocks could not be recovered yet", "request", p.Request, "err", err)
			}
		}
	}
}

// recover runs recoverMakes with Brakeman's routes held (503
// brakeman_recovering) until it is done.
func (d *brakemanDesk) recover() {
	d.recovering.Store(true)
	defer d.recovering.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d.recoverMakes(ctx)
}
