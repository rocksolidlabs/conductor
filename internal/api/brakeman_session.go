package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A Brakeman turn's two routes (docs/plans/brakeman.md, Session routes):
// its session reads its request and submits one brief, with its own
// session's token, while its request is starting or running. The admin's
// token reads the request too, so a person can see what Brakeman was
// given; a brief is always Brakeman's own (403 agent_only).

// maxBriefBody bounds POST …/brakeman/brief's body: the brief as sent,
// before it is measured as kept (brakeman.MaxBrief, 413 bound past it).
const maxBriefBody = 1 << 20

// turnSession answers the session the route names and the request its
// launch marked it with (CrewRef.Brakeman): 404 not_found for no such
// session, 404 not_brakeman for a session that is not a Brakeman turn's.
// agentOnly takes only the session's own token (the admin's answers 403
// agent_only); else the admin's too, and own reports which it was.
func (s *Server) turnSession(w http.ResponseWriter, r *http.Request, agentOnly bool) (d session.Driver, request string, own, ok bool) {
	if _, ok := s.brakemanDeskOf(w); !ok {
		return nil, "", false, false
	}
	d, found := s.registry.Get(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such session")
		return nil, "", false, false
	}
	own = agentTokenOK(d, presentedToken(r, false))
	if !own {
		if _, ok := s.taskSession(w, r, agentOnly); !ok {
			return nil, "", false, false
		}
		if p := s.authenticate(r, false); !p.admin {
			writeError(w, http.StatusUnauthorized, "unauthorized", "the session's own token or the workbench token is required")
			return nil, "", false, false
		}
	}
	ref := d.Info().Crew
	if ref == nil || ref.Brakeman == nil {
		writeError(w, http.StatusNotFound, "not_brakeman", "the session is not a Brakeman turn's")
		return nil, "", false, false
	}
	return d, ref.Brakeman.Request, own, true
}

// turnSubject is a request's subject as its turn reads it: resolved now
// against the records and the grant, or why it is not.
type turnSubject struct {
	Ref   brakeman.Ref `json:"ref"`
	URL   string       `json:"url,omitempty"`
	State string       `json:"state,omitempty"`
	Rev   string       `json:"rev,omitempty"`
	Why   string       `json:"why,omitempty"`
}

// turnBudget is what a turn has left: its reads and its time.
type turnBudget struct {
	ReadsLeft int       `json:"readsLeft"`
	Deadline  time.Time `json:"deadline,omitzero"`
	Seconds   int       `json:"secondsLeft"`
}

// turnBriefing is the last briefing a turn may build on: its request and
// when it was kept, with the server's coverage of what its turn read.
type turnBriefing struct {
	ID       string             `json:"id"`
	At       time.Time          `json:"at"`
	Coverage *brakeman.Coverage `json:"coverage,omitempty"`
}

// turnRequest is GET …/brakeman/request's request.
type turnRequest struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	State    string        `json:"state"`
	Subjects []turnSubject `json:"subjects"`
	Note     string        `json:"note,omitempty"`
	Since    time.Time     `json:"since,omitzero"`
	// LastBriefing is the newest briefing kept whose grant this turn's
	// covers (its footprint visible, its scope within this one's), absent
	// for none.
	LastBriefing *turnBriefing `json:"lastBriefing,omitempty"`
	// Scope is the scope the turn began under; ScopeNow the
	// configuration's now, when it differs: an item must be admitted by
	// both. Visibility and Permissions are the snapshot's and the
	// configuration's together.
	Scope       brakeman.Scope       `json:"scope"`
	ScopeNow    *brakeman.Scope      `json:"scopeNow,omitempty"`
	Visibility  brakeman.Visibility  `json:"visibility"`
	Permissions brakeman.Permissions `json:"permissions"`
	Budget      turnBudget           `json:"budget"`
	// Batch is a wake request's batch, the wakes the turn is about, as
	// the background role holds it (BK3); Notes the role's durable notes,
	// what the person chose to keep, for every turn.
	Batch *crew.Batch     `json:"batch,omitempty"`
	Notes []crew.RoleNote `json:"notes,omitempty"`
}

// bothVisible is the visibility two configurations allow together.
func bothVisible(a, b brakeman.Visibility) brakeman.Visibility {
	return brakeman.Visibility{Runs: a.Runs && b.Runs, Logs: a.Logs && b.Logs, Dispatch: a.Dispatch && b.Dispatch,
		Sessions: a.Sessions && b.Sessions, Touched: a.Touched && b.Touched, Terminals: a.Terminals && b.Terminals}
}

// bothPermit is the permissions two configurations allow together.
func bothPermit(a, b brakeman.Permissions) brakeman.Permissions {
	return brakeman.Permissions{Observe: a.Observe && b.Observe, Draft: a.Draft && b.Draft, Propose: a.Propose && b.Propose}
}

// handleSessionBrakemanRequest answers the turn's request: {request} with
// its kind, its subjects as resolved, its note and since, the last
// briefing it may build on, the scope, visibility and permissions it may
// use and what its budget has left; 404 not_brakeman for another session,
// 409 turn_over once the request is closed. Read with the session's own
// token, it marks the request read: a session that ends after it ended
// its turn, one that ends before failed to start.
func (s *Server) handleSessionBrakemanRequest(w http.ResponseWriter, r *http.Request) {
	_, id, own, ok := s.turnSession(w, r, false)
	if !ok {
		return
	}
	d := s.brakeman
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if !req.Open() || req.State == brakeman.StateQueued {
		writeError(w, http.StatusConflict, "turn_over", "the request is "+req.State)
		return
	}
	cfg, err := d.st.Config()
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if own {
		d.mu.Lock()
		d.read[id] = true
		d.mu.Unlock()
	}
	now := d.now()
	out := turnRequest{ID: req.ID, Kind: req.Kind, State: req.State, Subjects: []turnSubject{}, Note: req.Note, Since: req.Since,
		Scope: req.Config.Scope, Visibility: bothVisible(req.Config.Visibility, cfg.Visibility), Permissions: bothPermit(req.Config.Permissions, cfg.Permissions),
		Budget: turnBudget{ReadsLeft: max(0, req.Config.Budget.ReadsPerTurn-req.Reads), Deadline: req.Deadline, Seconds: max(0, int(req.Deadline.Sub(now).Seconds()))}}
	if b, _ := json.Marshal(cfg.Scope); string(b) != mustJSON(req.Config.Scope) {
		sc := cfg.Scope
		out.ScopeNow = &sc
	}
	log, err := d.st.Reads(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	rr := s.newRefResolver(d.st, req, log, cfg)
	for _, ref := range req.Subjects {
		res, why, err := rr.resolve(r.Context(), ref)
		if err != nil {
			s.log.Warn("a Brakeman request's subject could not be resolved", "request", id, "ref", ref.String(), "err", err)
			why = "could not be read now"
		}
		sub := turnSubject{Ref: ref, Why: why}
		if why == "" {
			sub.URL, sub.State, sub.Rev = res.Now.URL, res.Now.State, res.Now.Rev
		}
		out.Subjects = append(out.Subjects, sub)
	}
	out.LastBriefing, err = d.lastBriefing(req, rr.grant)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	if req.Batch != "" {
		if b := d.roleBatch(req.Batch); b != nil {
			// Only what the turn's grant admits: the configuration narrowed
			// since the batch gathered narrows what the turn reads of it.
			out.Batch = rr.visibleBatch(r.Context(), *b)
		}
	}
	if rec, err := d.roleRecord(); err == nil {
		out.Notes = rec.Notes
	}
	if own && out.Batch != nil {
		// The batch given is read: its wakes' classes are noted, under the
		// read mutex a brief is kept under, before the answer goes, so the
		// brief's footprint carries them (a summary of a ticket made ready
		// is withheld from a turn that may not see Dispatch).
		d.readMu.Lock()
		for _, c := range batchClasses(out.Batch) {
			d.usedClass(id, c)
		}
		d.readMu.Unlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": out})
}

// batchClasses are the visibility classes a batch's wakes are of, by
// their subjects' kinds; its overflow, which names none, counts every
// class a wake may be of.
func batchClasses(b *crew.Batch) []string {
	used := map[string]bool{}
	for _, w := range b.Wakes {
		if kind, _, ok := strings.Cut(w.Subject, ":"); ok {
			if c := refClass(kind); c != "" {
				used[c] = true
			}
		}
	}
	if b.Overflow != nil {
		used[brakeman.ClassRuns], used[brakeman.ClassSessions], used[brakeman.ClassDispatch] = true, true, true
	}
	var out []string
	for _, c := range brakeman.Classes {
		if used[c] {
			out = append(out, c)
		}
	}
	return out
}

// mustJSON is v as JSON, "" when it does not encode.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// lastBriefing is the newest briefed briefing before req whose grant g
// covers: every class its footprint used visible, its scope covered.
func (d *brakemanDesk) lastBriefing(req brakeman.Request, g refGrant) (*turnBriefing, error) {
	after := req.ID
	for range brakeman.Keep/brakeman.MaxPage + 2 {
		page, next, err := d.st.List(after, brakeman.MaxPage)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r.Kind != brakeman.KindBriefing || r.State != brakeman.StateBriefed {
				continue
			}
			if r.Footprint != nil && slices.ContainsFunc(r.Footprint.Classes, func(c string) bool { return !g.sees(c) }) {
				continue
			}
			if !g.covers(r.Config.Scope) {
				continue
			}
			return &turnBriefing{ID: r.ID, At: r.BriefAt, Coverage: r.Coverage}, nil
		}
		if next == "" {
			return nil, nil
		}
		after = next
	}
	return nil, nil
}

// briefBody is POST …/brakeman/brief's body.
type briefBody struct {
	OpID  string          `json:"opId"`
	Brief json.RawMessage `json:"brief"`
}

// handleSessionBrakemanBrief keeps the turn's brief: {opId, brief} →
// 201 {request}. The opId is looked up first: the same opId and brief
// answer 200 with the request whatever its state now (a lost reply asked
// again), any other brief 409 brief_exists; only a new submission is held
// to the request's state (409 turn_over once closed), the bounds (400
// invalid_brief with the item and why, 413 bound past 256 KiB as kept or
// resolutions past 4 MiB) and the refs (422 bad_refs naming each ref that
// resolves to nothing or lies outside the scope or the visibility, or a
// revision not the one read), all against the configuration as it is
// when the brief is kept (409 stale_revision when it keeps changing
// while the brief is checked). Nothing is written on a refusal. The
// session's own token only (403 agent_only for the admin's).
func (s *Server) handleSessionBrakemanBrief(w http.ResponseWriter, r *http.Request) {
	sd, id, _, ok := s.turnSession(w, r, true)
	if !ok {
		return
	}
	d := s.brakeman
	var body briefBody
	if err := decodeJSONLimit(w, r, &body, maxBriefBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxBriefBody>>10))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(body.Brief) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "the body is {opId, brief}")
		return
	}
	req, err := d.st.Get(id)
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	info := sd.Info()
	runID := ""
	if info.Crew != nil {
		runID = info.Crew.RunID
	}
	if req.HasBrief {
		s.repeatBrief(w, req, body)
		return
	}
	if req.State != brakeman.StateStarting && req.State != brakeman.StateRunning {
		writeError(w, http.StatusConflict, "turn_over", "the request is "+req.State+": a brief is kept only during its turn")
		return
	}
	if req.RunID != "" && req.RunID != runID {
		writeError(w, http.StatusForbidden, "not_the_turn", "the session is not the request's run's")
		return
	}
	// The brief is checked against the configuration read here and kept
	// under admit, the order a configuration's save takes, only while the
	// configuration is still at the revision it was checked against: a
	// save meanwhile (drafting refused, the visibility or the scope
	// narrowed) has the brief checked again against it, and one that
	// keeps moving refuses it (409 stale_revision), nothing kept. Every
	// new brief is kept under admit, so a brief found kept there, or by a
	// check refusing this one, is an overlapping submission's: this one
	// is its repeat (200) or another brief (409 brief_exists), never
	// created again nor refused against a configuration that moved after
	// the kept one was checked.
	for attempt := 1; ; attempt++ {
		if f := d.beforeCheck.Load(); f != nil {
			(*f)(id)
		}
		b, refs, rev, refuse := s.checkBrief(r, req, body.Brief)
		if refuse != nil {
			if now, err := d.st.Get(id); err == nil && now.HasBrief {
				s.repeatBrief(w, now, body)
				return
			}
			refuse(w)
			return
		}
		if f := d.beforeKeep.Load(); f != nil {
			(*f)(id)
		}
		d.admit.Lock()
		now, err := d.st.Get(id)
		if err != nil {
			d.admit.Unlock()
			s.brakemanError(w, err)
			return
		}
		if now.HasBrief {
			d.admit.Unlock()
			s.repeatBrief(w, now, body)
			return
		}
		cfg, err := d.st.Config()
		if err != nil {
			d.admit.Unlock()
			s.brakemanError(w, err)
			return
		}
		if cfg.Rev != rev {
			d.admit.Unlock()
			if attempt < briefChecks {
				continue
			}
			writeError(w, http.StatusConflict, "stale_revision", "Brakeman's configuration changed while the brief was checked: submit it again")
			return
		}
		// The coverage is the server's record of what the turn's reads
		// reached, and the classes its answers used beside its read
		// log's, read under the reads' mutex so none is left out of them.
		d.readMu.Lock()
		var coverage *brakeman.Coverage
		if cur, err := d.st.Get(id); err == nil {
			coverage = cur.Coverage
		}
		kept, err := d.st.KeepBrief(id, brakeman.Kept{OpID: body.OpID, Brief: b, Refs: refs, RunID: runID, SessionID: info.ID, Coverage: coverage, Classes: d.classesUsed(id)})
		d.readMu.Unlock()
		d.admit.Unlock()
		if err != nil {
			s.brakemanError(w, err)
			return
		}
		d.publish(id)
		d.poke()
		writeJSON(w, http.StatusCreated, map[string]any{"request": kept})
		return
	}
}

// repeatBrief answers a submission to a request whose brief is kept
// (req, as read): the same opId and brief 200 with the request, compared
// as kept, never resolved or checked again against the catalog and the
// folders, whatever the records, the catalog and the request say now (a
// drafted crew's agent removed since is still the brief kept); any other
// brief 409 brief_exists.
func (s *Server) repeatBrief(w http.ResponseWriter, req brakeman.Request, body briefBody) {
	b, err := brakeman.DecodeBrief(body.Brief, req.Config.Permissions, keptFacts{brakemanFacts{s: s}})
	if err != nil {
		s.brakemanError(w, fmt.Errorf("%w: %s", brakeman.ErrBriefExists, req.ID))
		return
	}
	kept, err := s.brakeman.st.KeepBrief(req.ID, brakeman.Kept{OpID: body.OpID, Brief: b})
	if err != nil {
		s.brakemanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": kept})
}

// briefChecks is how many times a brief is checked against a
// configuration that moves under it before it is refused.
const briefChecks = 3

// checkBrief holds a new brief to the configuration as it is now and to
// the request's snapshot: decoded under the permissions both allow, its
// proposals held to the guide, its refs resolved under the grant both
// allow. It answers the brief, its resolutions and the configuration's
// revision they were checked against, or the refusal to write (refuse
// non-nil), which the caller writes unless a brief was kept meanwhile.
func (s *Server) checkBrief(r *http.Request, req brakeman.Request, raw json.RawMessage) (brakeman.Brief, []brakeman.Resolution, int64, func(http.ResponseWriter)) {
	d := s.brakeman
	id := req.ID
	refused := func(err error) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) { s.brakemanError(w, err) }
	}
	cfg, err := d.st.Config()
	if err != nil {
		return brakeman.Brief{}, nil, 0, refused(err)
	}
	b, err := brakeman.DecodeBrief(raw, bothPermit(req.Config.Permissions, cfg.Permissions), brakemanFacts{s: s})
	if err != nil {
		return brakeman.Brief{}, nil, 0, refused(err)
	}
	// A briefing answers with facts (the live check kept one of its
	// summary alone).
	if err := brakeman.CheckAnswers(b, req.Kind); err != nil {
		return brakeman.Brief{}, nil, 0, refused(err)
	}
	if err := checkProposals(b); err != nil {
		return brakeman.Brief{}, nil, 0, refused(err)
	}
	log, err := d.st.Reads(id)
	if err != nil {
		return brakeman.Brief{}, nil, 0, refused(err)
	}
	refs, err := s.resolveBriefRefs(r.Context(), d.st, req, b, log, cfg)
	var bad *badRefsError
	var unopened *errRefStore
	switch {
	case errors.As(err, &bad):
		return brakeman.Brief{}, nil, 0, func(w http.ResponseWriter) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "bad_refs", "message": bad.Error(), "refs": bad.Refs}})
		}
	case errors.As(err, &unopened):
		// A store the server has and could not open, as the other
		// routes answer one.
		return brakeman.Brief{}, nil, 0, func(w http.ResponseWriter) {
			writeError(w, http.StatusServiceUnavailable, "store_unavailable", unopened.Error())
		}
	case err != nil:
		s.log.Warn("a brief's refs could not be resolved", "request", id, "err", err)
		return brakeman.Brief{}, nil, 0, func(w http.ResponseWriter) {
			writeError(w, http.StatusInternalServerError, "store_failed", "the records a brief cites could not be read")
		}
	}
	return b, refs, cfg.Rev, nil
}

// keptFacts are the facts a repeat of a kept brief is decoded with: a
// drafted crew was checked as the brief was kept, so it is not checked
// again against what the catalog holds now; the decoding still
// normalises it as it did, for the comparison with the brief kept.
type keptFacts struct{ brakemanFacts }

func (keptFacts) CheckCrew(json.RawMessage) error { return nil }

// checkProposals holds each proposal to the guide's table: its operation
// one the guide lists, and, for an operation whose route itself checks a
// revision (Operation.Revision), the revision it was read at, above 0, so
// the control Conductor renders for it is refused by the backend when the
// target moved. The error is an *Invalid (invalid_brief).
func checkProposals(b brakeman.Brief) error {
	ops := Operations()
	for i, it := range b.Items {
		if it.Kind != brakeman.ItemProposal || it.Proposal == nil {
			continue
		}
		p := it.Proposal
		j := slices.IndexFunc(ops, func(o Operation) bool { return o.Name == p.Op })
		if j < 0 {
			return &brakeman.Invalid{Code: brakeman.CodeInvalidBrief, Field: fmt.Sprintf("items[%d].op", i), Why: "no operation " + p.Op + " in the guide: name one its operations section lists"}
		}
		if ops[j].Revision == "" {
			continue
		}
		if n, err := strconv.ParseInt(p.ExpectedRevision, 10, 64); err != nil || n <= 0 {
			return &brakeman.Invalid{Code: brakeman.CodeInvalidBrief, Field: fmt.Sprintf("items[%d].expectedRevision", i), Why: p.Op + " checks the " + ops[j].Revision + "'s revision: give the one you read, above 0"}
		}
	}
	return nil
}
