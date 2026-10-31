package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os/exec"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/pty"
)

// A ticket's plan revisions over HTTP (Program item DS2,
// docs/plans/dispatch-evidence.md, PR-6): the record between what a
// ticket asks and the run that does it. A proposal takes a saved crew with
// tasks as it is now, the agents its members name as the catalog resolves
// them, the commit the work starts from and the source references at that
// commit, so the operator's approval covers exactly what a launch will
// run; the store keeps the revision immutable (internal/dispatch,
// plans.go). The reads say beside the store's own staleness whether the
// saved crew was edited since (crewChanged) and whether an agent resolves
// otherwise now (agentsChanged), both cured by a new revision, never by
// approving again; the approval refuses a revision whose agents resolve
// otherwise (stale_plan). The routes are Dispatch's: the admin's, a
// switchyard's 403, every body decoded with DisallowUnknownFields and
// carrying {opId, expectedRev?, byName?}.

const (
	// MaxPlanReply bounds a plan revision's read: its summary beside its
	// body as the bytes it is kept as (at most dispatch.MaxPlanBody, 2
	// MiB). The largest, a body at exactly 2 MiB with the summary's texts
	// at their escaped worst, is under it (dispatch_plans_test.go).
	// Exported for the CLI.
	MaxPlanReply = dispatch.MaxPlanReply
	// MaxPlansReply bounds a ticket's revisions' list: at most
	// dispatch.MaxPlans summaries, never a body. The largest, every
	// summary's texts (the approval's note, the stale words, the crew's
	// name, the approver, the withdrawer and the author) at their escaped
	// worst, is under it (dispatch_plans_test.go). Exported for the CLI.
	MaxPlansReply = 1 << 20
	// maxPlanBase bounds the base a proposal names, in bytes: a branch, a
	// tag, HEAD or a commit id, which rev-parse reads.
	maxPlanBase = 256
	// planManifestFile is the file-form project manifest's name, relative
	// to the crew's working directory (internal/crew's manifestFile).
	planManifestFile = "conductor.manifest.json"
	// maxPlanFactsCached bounds how many revisions' facts the server keeps
	// (planFactsCache): ten tickets' lists at their bound.
	maxPlanFactsCached = 10 * dispatch.MaxPlans
)

// The bodies of the revisions' writes: the operation and the fields its
// intent takes. A proposal's fields are the request as the store
// fingerprints it (dispatch.PlanRequest), so a resend is checked against
// what was sent.
type (
	planProposeBody struct {
		dispatchOp
		dispatch.PlanRequest
	}
	planApproveBody struct {
		dispatchOp
		TicketRev int64  `json:"ticketRev,omitempty"`
		Note      string `json:"note,omitempty"`
	}
)

// planApproveArgs is plan.approve's content as the store fingerprints it
// (internal/dispatch's planApproveArgs, field for field), for the replay
// the approve route makes before it resolves the agents.
type planApproveArgs struct {
	ID        string `json:"id"`
	TicketRev int64  `json:"ticketRev,omitempty"`
	Note      string `json:"note,omitempty"`
}

// planReply is a plan revision as the routes answer it: the store's
// summary (with its number and its staleness), whether the saved crew it
// was taken from was edited or removed since (CrewChanged), whether an
// agent of its body resolves otherwise in the catalog now
// (AgentsChanged), how many checks it expects (the crew's tasks' checks
// and the project manifest's verify steps, the crew's own or the file
// form's), and, on a revision's own read alone, its body as the bytes it
// is kept as.
type planReply struct {
	dispatch.PlanSummary
	CrewChanged    bool            `json:"crewChanged"`
	AgentsChanged  bool            `json:"agentsChanged"`
	ExpectedChecks int             `json:"expectedChecks"`
	Body           json.RawMessage `json:"body,omitempty"`
}

// planFacts is what the routes take of a revision's body: its crew's yolo
// choice, its agents as they resolved, and how many checks it expects
// (its tasks' checks and its manifest's verify steps, the crew's own or
// the file form's). A body never changes, so neither do its facts.
type planFacts struct {
	Yolo   *bool
	Agents []dispatch.PlanAgent
	Checks int
}

// planBodyFacts is the part of a revision's body its facts are read from.
type planBodyFacts struct {
	Crew struct {
		Yolo  *bool `json:"yolo"`
		Tasks []struct {
			Checks []json.RawMessage `json:"checks"`
		} `json:"tasks"`
		Manifest *struct {
			Verify []json.RawMessage `json:"verify"`
		} `json:"manifest"`
	} `json:"crew"`
	Agents       []dispatch.PlanAgent `json:"agents"`
	ManifestFile *struct {
		Manifest struct {
			Verify []json.RawMessage `json:"verify"`
		} `json:"manifest"`
	} `json:"manifestFile"`
}

// readPlanFacts reads a revision's facts from its body: the checks it
// expects are each task's checks and the manifest's verify steps, the
// crew's own or the file form's.
func readPlanFacts(body json.RawMessage) (planFacts, error) {
	var b planBodyFacts
	if err := json.Unmarshal(body, &b); err != nil {
		return planFacts{}, fmt.Errorf("a plan revision's body: %w", err)
	}
	f := planFacts{Yolo: b.Crew.Yolo, Agents: b.Agents}
	for _, t := range b.Crew.Tasks {
		f.Checks += len(t.Checks)
	}
	if b.Crew.Manifest != nil {
		f.Checks += len(b.Crew.Manifest.Verify)
	}
	if b.ManifestFile != nil {
		f.Checks += len(b.ManifestFile.Manifest.Verify)
	}
	return f, nil
}

// yolo is the yolo choice the revision fixed, the server's default for a
// body that holds none (none is made so).
func (f planFacts) yolo(def bool) bool {
	if f.Yolo != nil {
		return *f.Yolo
	}
	return def
}

// planFactsCache keeps revisions' facts by their bodies' digests, at most
// maxPlanFactsCached of them, the oldest kept dropped first: a list of a
// ticket's revisions reads and decodes a body (up to dispatch.MaxPlanBody
// each, on the store's one connection) only for a revision whose facts it
// has not kept, so the ticket page's reads on every plan event cost the
// summaries alone. A digest is the SHA-256 of a body's bytes, so a fact
// kept never goes stale; what the facts are compared with (the catalog,
// the saved crews) is read again on every request. Its zero value is
// ready; it is safe to use from many goroutines.
type planFactsCache struct {
	mu    sync.Mutex
	facts map[string]planFacts
	order []string // the digests kept, oldest first
	loads int      // bodies read and decoded, for the tests
}

// get is the facts of the body whose digest is digest, read with load and
// kept when they are not.
func (c *planFactsCache) get(digest string, load func() (json.RawMessage, error)) (planFacts, error) {
	c.mu.Lock()
	f, ok := c.facts[digest]
	c.mu.Unlock()
	if ok {
		return f, nil
	}
	body, err := load()
	if err != nil {
		return planFacts{}, err
	}
	f, err = readPlanFacts(body)
	if err != nil {
		return planFacts{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	if c.facts == nil {
		c.facts = map[string]planFacts{}
	}
	if _, ok := c.facts[digest]; !ok {
		if len(c.order) >= maxPlanFactsCached {
			delete(c.facts, c.order[0])
			c.order = slices.Delete(c.order, 0, 1)
		}
		c.facts[digest] = f
		c.order = append(c.order, digest)
	}
	return f, nil
}

// loaded is how many bodies the cache has read and decoded.
func (c *planFactsCache) loaded() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads
}

// agentSelection is an agent's selection as a session launched from the
// catalog entry a with the yolo choice yolo gets it from the entry
// (createLocalSession): its command as the catalog gives it, its session
// recipe's start arguments ({id} unexpanded) when the recipe names the
// session at launch, its yolo recipe's arguments when yolo is on and the
// agent has a recipe (else none), and the names of the environment the
// entry gives the session, its env with the yolo recipe's overlaid when
// that applies and the server's variables its envPassthrough names as the
// launch passes them through (entryPassthrough), with the SHA-256 of those
// names and values together ("" for none), never a value. SHA256 is the
// SHA-256 of all of it as encoded with the entry's adapter, its signal
// (EffectiveSignal: the bell when it names none) and whether its yolo
// recipe applies (yolo on and a recipe that is not empty, as the launch's
// applied), which decide what the launch's adapter adds to its argv and
// environment (agents.InjectFor: Claude Code's hook --settings, of which
// only the last applies, or its yolo-only one; agents.TrustArgsFor:
// Codex's trust; Codex's notify), so a recipe made non-empty by an env
// that repeats the entry's own values changes it too: the selection's identity, which a
// revision records, its approval and its launch compare, and every
// attempt of a run launched from it is checked against. It is the one
// function for the proposal, the approval, the launch and every attempt.
func agentSelection(a catalog.Agent, yolo bool) dispatch.PlanAgent {
	p := dispatch.PlanAgent{ID: a.ID, Command: slices.Clone(a.Command), StartArgs: []string{}, YoloArgs: []string{}, EnvNames: []string{}}
	if p.Command == nil {
		p.Command = []string{}
	}
	if r := a.Session; !r.Empty() && len(r.StartArgs) > 0 {
		p.StartArgs = slices.Clone(r.StartArgs)
	}
	env := maps.Clone(a.Env)
	if yolo && !a.Yolo.Empty() {
		p.YoloArgs = append(p.YoloArgs, a.Yolo.Args...)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, a.Yolo.Env)
	}
	for k, v := range entryPassthrough(a, env) {
		if env == nil {
			env = map[string]string{}
		}
		env[k] = v
	}
	if len(env) > 0 {
		p.EnvNames = slices.Sorted(maps.Keys(env))
		pairs := make([][2]string, 0, len(env))
		for _, k := range p.EnvNames {
			pairs = append(pairs, [2]string{k, env[k]})
		}
		p.EnvSHA256 = sha256Of(pairs)
	}
	p.SHA256 = sha256Of(struct {
		ID        string         `json:"id"`
		Command   []string       `json:"command"`
		StartArgs []string       `json:"startArgs"`
		YoloArgs  []string       `json:"yoloArgs"`
		EnvNames  []string       `json:"envNames"`
		EnvSHA256 string         `json:"envSha256"`
		Adapter   string         `json:"adapter"`
		Signal    catalog.Signal `json:"signal"`
		Yolo      bool           `json:"yolo"`
	}{p.ID, p.Command, p.StartArgs, p.YoloArgs, p.EnvNames, p.EnvSHA256, a.Adapter, a.EffectiveSignal(), yolo && !a.Yolo.Empty()})
	return p
}

// entryPassthrough is the server's variables the entry's envPassthrough
// names as a session launched from it gets them (createLocalSession, through
// pty.BuildEnv): each one the server's environment has, never a blocked or
// a CONDUCTOR_ name, and none the entry's own env (set, with the yolo
// recipe's) sets, since that value wins. A name the entry adds to the list,
// or a value of the server's that changes, changes the selection.
func entryPassthrough(a catalog.Agent, set map[string]string) map[string]string {
	if len(a.EnvPassthrough) == 0 {
		return nil
	}
	got := map[string]string{}
	for _, kv := range pty.BuildEnv(pty.ParentEnv(), a.EnvPassthrough, nil, nil) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	out := map[string]string{}
	for _, k := range a.EnvPassthrough {
		if v, ok := got[k]; ok {
			if _, own := set[k]; !own {
				out[k] = v
			}
		}
	}
	return out
}

// sha256Of is the SHA-256, in hex, of v as JSON with HTML escaping off.
func sha256Of(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// agentChange says how the agent a revision recorded resolves otherwise
// in the catalog now, with its yolo choice, in words ("the agent claude's
// command"), or "" when it resolves the same: no longer in the catalog,
// its command, its session recipe's start arguments, its yolo recipe's
// arguments, its environment (names or values, the yolo recipe's
// included), or else its adapter, its signal or whether its yolo recipe
// applies (which the selection's identity alone records), the first that
// differs.
func agentChange(cat catalog.Catalog, rec dispatch.PlanAgent, yolo bool) string {
	a, ok := cat.Get(rec.ID)
	if !ok {
		return fmt.Sprintf("the agent %s is no longer in the catalog", rec.ID)
	}
	now := agentSelection(a, yolo)
	if now.SHA256 == rec.SHA256 {
		return ""
	}
	switch {
	case !slices.Equal(now.Command, rec.Command):
		return fmt.Sprintf("the agent %s's command changed", rec.ID)
	case !slices.Equal(now.StartArgs, rec.StartArgs):
		return fmt.Sprintf("the agent %s's session recipe's start arguments changed", rec.ID)
	case !slices.Equal(now.YoloArgs, rec.YoloArgs):
		return fmt.Sprintf("the agent %s's yolo recipe's arguments changed", rec.ID)
	case !slices.Equal(now.EnvNames, rec.EnvNames) || now.EnvSHA256 != rec.EnvSHA256:
		return fmt.Sprintf("the agent %s's environment changed", rec.ID)
	}
	return fmt.Sprintf("the agent %s's adapter, signal or yolo recipe changed", rec.ID)
}

// agentsChange is the first agent of a revision's body that resolves
// otherwise now (agentChange), "" when none does.
func agentsChange(cat catalog.Catalog, f planFacts, def bool) string {
	yolo := f.yolo(def)
	for _, a := range f.Agents {
		if words := agentChange(cat, a, yolo); words != "" {
			return words
		}
	}
	return ""
}

// planReplies makes the routes' replies of revisions read from the store:
// each one's facts from the server's planFacts (its body read only when
// they are not kept there), the saved crews read once each and the
// catalog once for all.
type planReplies struct {
	s     *Server
	ds    *dispatch.Store
	cat   catalog.Catalog
	crews map[string]time.Time // a saved crew's updatedAt; absent when gone
	read  map[string]bool
}

// planReplies starts the replies of one request.
func (s *Server) planReplies(ds *dispatch.Store) *planReplies {
	return &planReplies{s: s, ds: ds, cat: s.Catalog(), crews: map[string]time.Time{}, read: map[string]bool{}}
}

// crewChanged reports whether the saved crew with the id was edited since
// it was saved at updated, or is gone: its updatedAt now another, or no
// crew with the id. A crew whose file cannot be read counts as changed,
// since nothing says it is the same, and is logged.
func (pr *planReplies) crewChanged(id string, updated time.Time) bool {
	if !pr.read[id] {
		pr.read[id] = true
		if pr.s.crews != nil {
			c, err := pr.s.crews.Get(id)
			switch {
			case err == nil:
				pr.crews[id] = c.UpdatedAt
			case !errors.Is(err, crew.ErrNotFound):
				pr.s.log.Warn("a plan revision's crew could not be read", "crew", id, "err", err)
			}
		}
	}
	now, ok := pr.crews[id]
	return !ok || !now.Equal(updated)
}

// of is the reply of the revision whose summary is p: its body, when
// body is nil and its facts are not kept, read by its id; the body kept
// on the reply when withBody is set (body then the revision's).
func (pr *planReplies) of(ctx context.Context, p dispatch.PlanSummary, body json.RawMessage, withBody bool) (planReply, error) {
	f, err := pr.s.planFacts.get(p.Digest, func() (json.RawMessage, error) {
		if body != nil {
			return body, nil
		}
		v, err := pr.ds.Plan(ctx, p.ID)
		return v.Body, err
	})
	if err != nil {
		return planReply{}, err
	}
	out := planReply{
		PlanSummary:    p,
		CrewChanged:    pr.crewChanged(p.CrewID, p.CrewUpdatedAt),
		AgentsChanged:  agentsChange(pr.cat, f, pr.s.cfg.Yolo) != "",
		ExpectedChecks: f.Checks,
	}
	if withBody {
		out.Body = body
	}
	return out, nil
}

// byID is the reply of the revision with the id, read now.
func (pr *planReplies) byID(ctx context.Context, id string, withBody bool) (planReply, error) {
	v, err := pr.ds.Plan(ctx, id)
	if err != nil {
		return planReply{}, err
	}
	return pr.of(ctx, v.PlanSummary, v.Body, withBody)
}

// planAfter is what a revision's operation answers beside its receipt:
// the revision as read after it, without its body, by the id the route
// names, else the one the receipt names (a proposal's); nothing when
// there is neither.
func (s *Server) planAfter(ctx context.Context, ds *dispatch.Store, id string) func(dispatch.Receipt) (any, error) {
	return func(rc dispatch.Receipt) (any, error) {
		rid := cmp.Or(id, rc.ID)
		if rid == "" {
			return nil, nil
		}
		return s.planReplies(ds).byID(ctx, rid, false)
	}
}

// handleDispatchPlans answers a ticket's plan revisions, deleted or not:
// {plans}, every revision's summary, newest first, with its number n, its
// state, its approval (approvedAt, approvedBy, approvedNote, approvedRev),
// stale with its words and remedy for an approved one whose approval no
// longer holds, crewChanged, agentsChanged and expectedChecks; never a
// body; at most dispatch.MaxPlans of them, under MaxPlansReply; 404
// not_found; 400 invalid_request for any parameter. A revision's body is
// read only when its facts are not kept (planFactsCache).
func (s *Server) handleDispatchPlans(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	ctx := r.Context()
	summaries, err := ds.Plans(ctx, key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	pr := s.planReplies(ds)
	out := make([]planReply, 0, len(summaries))
	for _, p := range summaries {
		// The list's summary, read in one transaction with the others: its
		// number and its staleness as the list saw them.
		reply, err := pr.of(ctx, p, nil, false)
		if err != nil {
			s.dispatchError(w, "plan revision", err)
			return
		}
		out = append(out, reply)
	}
	writeDispatch(w, http.StatusOK, map[string]any{"plans": out})
}

// handleDispatchPlan answers a plan revision whole: {plan}, its summary as
// the list gives it and its body as the bytes it is kept as, under
// MaxPlanReply; 404 not_found; 400 invalid_request for an id that is not
// one and for any parameter.
func (s *Server) handleDispatchPlan(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	v, err := s.planReplies(ds).byID(r.Context(), id, true)
	if err != nil {
		s.dispatchError(w, "plan revision", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"plan": v})
}

// handleDispatchProposePlan makes a plan revision of a live ticket
// (plan.propose): body {opId, expectedRev?, byName?, crew, base?, basedOn?,
// references?, attachments?, comments?, constraints?, decisions?, note?},
// at most maxTicketBody; 201 {receipt, plan}, the revision's summary as
// the list gives it, its body read by GET /api/dispatch/plans/{id}. The
// operation is replayed first, so a resend is answered from its receipt
// whatever the crew or HEAD became since (op_conflict for another request
// under the id); only a new one goes on to read the crew and run git
// (planBody), then the store's checks and bounds (ProposePlan), whose
// refusals keep their receipts. 404 not_found for a crew that is not
// there; 409 invalid for a crew without tasks, one that is not
// launchable, an agent the catalog lacks, a working directory that is not
// a repository, a base that names no commit, a reference that leaves the
// directory or whose path the base has not, and a file-form manifest that
// is not one; 409 bound for more references than a revision holds.
func (s *Server) handleDispatchProposePlan(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	var body planProposeBody
	if !readOp(w, r, &body, &body.dispatchOp, maxTicketBody) {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	op := body.op()
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentPlanPropose, dispatch.PlanProposeArgs{Key: key, PlanRequest: body.PlanRequest})
	if err != nil || found {
		s.writeOp(w, "plan revision", rc, err, true, "plan", s.planAfter(ctx, ds, ""))
		return
	}
	pb, ok := s.planBody(ctx, w, ds, key, body.PlanRequest)
	if !ok {
		return
	}
	rc, err = ds.ProposePlan(ctx, op, key, body.PlanRequest, pb)
	s.writeOp(w, "plan revision", rc, err, true, "plan", s.planAfter(ctx, ds, ""))
}

// planBody builds a proposal's body from its request (docs/plans/
// dispatch-evidence.md, "Proposing"), in this order: the saved crew,
// refused unless it has tasks, is launchable and its members' agents are
// in the catalog; its working directory, the ticket's project's folder
// when the project names one, else the crew's own, else the server's
// default, through resolveCwd, and a repository (crew.CheckRepo); the
// base, the one given or HEAD, made a full commit there; the crew's prefix
// in its repository; each reference cleaned, refused when it leaves the
// directory, and resolved at the base to its object and kind; a file-form
// project manifest's blob and manifest at the base (crew.ResolveManifest,
// as a launch reads it); the yolo choice, the crew's, else the server's
// default now, fixed on the crew with its working directory; and each
// agent its members name as the catalog resolves it (agentSelection).
// Every git command is an argv through internal/gitcli, in the C locale.
// The attachments and comments are handed to the store as named, which
// checks them and sets each comment's digest from its row. When it
// reports false it has answered.
func (s *Server) planBody(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, key string, req dispatch.PlanRequest) (dispatch.PlanBody, bool) {
	fail := func(status int, code, format string, args ...any) (dispatch.PlanBody, bool) {
		writeError(w, status, code, fmt.Sprintf(format, args...))
		return dispatch.PlanBody{}, false
	}
	if s.crews == nil {
		return fail(http.StatusServiceUnavailable, "store_unavailable", "no data directory is configured")
	}
	if req.Crew == "" {
		return fail(http.StatusBadRequest, "invalid_request", "crew is the id of a saved crew")
	}
	c, err := s.crews.Get(req.Crew)
	switch {
	case errors.Is(err, crew.ErrNotFound), errors.Is(err, crew.ErrInvalid):
		return fail(http.StatusNotFound, "not_found", "no crew %s", cut(req.Crew, 64))
	case errors.Is(err, crew.ErrUnreadable):
		return fail(http.StatusConflict, "crew_unreadable", "%s", err.Error())
	case err != nil:
		s.log.Error("crew read failed", "crew", req.Crew, "err", err)
		return fail(http.StatusInternalServerError, "store_failed", "could not read the crews")
	}
	if len(c.Tasks) == 0 {
		return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew %s has no tasks: a plan revision plans a crew with tasks", c.ID)
	}
	if err := c.Launchable(); err != nil {
		return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew %s cannot be launched: %s", c.ID, err.Error())
	}
	cat := s.Catalog()
	if err := c.CheckAgents(cat); err != nil {
		return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew %s: %s", c.ID, err.Error())
	}
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	if err != nil {
		if errors.Is(err, dispatch.ErrNotFound) {
			return fail(http.StatusNotFound, "not_found", "no ticket %s", key)
		}
		s.dispatchError(w, "ticket", err)
		return dispatch.PlanBody{}, false
	}
	cwd, err := s.resolveCwd(cmp.Or(pv.Project.Cwd, c.Cwd, s.cfg.DefaultCwd))
	if err != nil {
		return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew's working directory: %s", err.Error())
	}
	if err := crew.CheckRepo(ctx, cwd); err != nil {
		if errors.Is(err, crew.ErrNotRepo) || errors.Is(err, crew.ErrNoGit) {
			return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew's working directory %s: %s", cwd, err.Error())
		}
		return s.gitFailed(w, "the working directory's repository", err)
	}
	base, words, err := planBase(ctx, cwd, req.Base)
	switch {
	case err != nil:
		return s.gitFailed(w, "the base", err)
	case words != "":
		return fail(http.StatusConflict, dispatch.RefusalInvalid, "%s", words)
	}
	prefix, err := gitcli.Run(ctx, cwd, "rev-parse", "--show-prefix")
	if err != nil {
		return s.gitFailed(w, "the crew's place in its repository", err)
	}
	prefix = strings.TrimSuffix(prefix, "\n")
	if n := len(req.References); n > dispatch.MaxPlanReferences {
		return fail(http.StatusConflict, dispatch.RefusalBound, "at most %d references in a revision; this would make %d", dispatch.MaxPlanReferences, n)
	}
	refs := make([]dispatch.PlanReference, 0, len(req.References))
	for _, rr := range req.References {
		ref, words, err := planReference(ctx, cwd, base, prefix, rr)
		switch {
		case err != nil:
			return s.gitFailed(w, "a reference at the base", err)
		case words != "":
			return fail(http.StatusConflict, dispatch.RefusalInvalid, "%s", words)
		}
		refs = append(refs, ref)
	}
	var manifest *dispatch.PlanManifestFile
	if c.Manifest != nil && c.Manifest.File {
		m, blob, err := crew.ResolveManifest(ctx, cwd, base, c.Tasks)
		switch {
		case errors.Is(err, crew.ErrInvalid):
			return fail(http.StatusConflict, dispatch.RefusalInvalid, "the crew's project manifest: %s", err.Error())
		case err != nil:
			return s.gitFailed(w, "the project manifest at the base", err)
		}
		manifest = &dispatch.PlanManifestFile{Path: planManifestFile, Blob: blob, Manifest: encodeRaw(m)}
	}
	yolo := s.cfg.Yolo
	if c.Yolo != nil {
		yolo = *c.Yolo
	}
	c.Cwd, c.Yolo = cwd, &yolo
	var agents []dispatch.PlanAgent
	for _, m := range c.Members {
		if slices.ContainsFunc(agents, func(a dispatch.PlanAgent) bool { return a.ID == m.AgentID }) {
			continue
		}
		a, _ := cat.Get(m.AgentID)
		agents = append(agents, agentSelection(a, yolo))
	}
	comments := make([]dispatch.PlanComment, 0, len(req.Comments))
	for _, id := range req.Comments {
		comments = append(comments, dispatch.PlanComment{ID: id})
	}
	return dispatch.PlanBody{
		Base: base, Crew: encodeRaw(c), Agents: agents, ManifestFile: manifest, References: refs,
		Attachments: req.Attachments, Comments: comments, Constraints: req.Constraints, Decisions: req.Decisions, Note: req.Note,
	}, true
}

// gitFailed answers a failure of git itself while a proposal is built:
// 500 git_failed, logged with why; never a refusal of the request's.
func (s *Server) gitFailed(w http.ResponseWriter, what string, err error) (dispatch.PlanBody, bool) {
	s.log.Warn("git failed while a plan revision was proposed", "what", what, "err", err)
	writeError(w, http.StatusInternalServerError, "git_failed", "could not read "+what+" with git")
	return dispatch.PlanBody{}, false
}

// encodeRaw is v as JSON with HTML escaping off, as the store encodes its
// values.
func encodeRaw(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return json.RawMessage(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// planBase makes the base a proposal names, HEAD when it names none, a
// full commit id of the repository cwd is in: git rev-parse --verify
// <ref>^{commit}. A ref is at most maxPlanBase bytes with no space or
// control character and never begins with '-', so git never reads it as
// an option. The words say why it names no commit; "" when it does. The
// error is git's own failure (gitUnresolved), never the request's.
func planBase(ctx context.Context, cwd, ref string) (string, string, error) {
	ref = cmp.Or(ref, "HEAD")
	switch {
	case len(ref) > maxPlanBase:
		return "", fmt.Sprintf("the base is more than %d bytes", maxPlanBase), nil
	case strings.HasPrefix(ref, "-") || strings.ContainsFunc(ref, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return "", fmt.Sprintf("the base %q is not a branch, a tag or a commit", ref), nil
	}
	out, err := gitcli.Run(ctx, cwd, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil && !gitUnresolved(err) {
		return "", "", err
	}
	commit := strings.TrimSuffix(out, "\n")
	if err != nil || !isFullCommit(commit) {
		return "", fmt.Sprintf("the base %q names no commit of the crew's working directory", ref), nil
	}
	return commit, "", nil
}

// gitUnresolved reports whether err is git's answer that what it was asked
// for is not there: git ran to its end and exited 1 or 128 with nothing on
// its standard error (git rev-parse --verify --quiet of a name that
// resolves to nothing, HEAD@{n} past the log included; git cat-file -e of
// an object the repository has not), which gitcli.Run hands back as git's
// exit status itself (gitError wraps it only when git said nothing).
// Anything else is git's own failure: a context that ended (the request's
// deadline), a git that could not start or was killed, an object it
// could not read ("error: unable to open loose object"), a repository it
// refuses.
func gitUnresolved(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 128)
}

// isFullCommit reports whether s is a full object id, SHA-1 or SHA-256,
// in lower-case hex.
func isFullCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

// planReference resolves a reference a proposal names at the base: its
// path cleaned (slash-separated, relative to the crew's working
// directory), refused when it is absolute or leaves the directory, then
// the object the base holds at <prefix><path> (git rev-parse --verify
// <base>:<prefix><path>, the directory itself for ".") and its kind (git
// cat-file -t), a file or a directory: a path the base has not, a
// submodule and anything else are refused. The words say why; "" when it
// is taken. The error is git's own failure (gitUnresolved): an object the
// base names that git cannot read, or that the repository has not, is
// one; a submodule's commit the repository has not is the request's,
// told by its tree entry's mode (gitlinkAt), never by the object's
// absence alone, which a tracked file's missing blob shares.
func planReference(ctx context.Context, cwd, base, prefix string, rr dispatch.PlanReferenceRequest) (dispatch.PlanReference, string, error) {
	p := rr.Path
	switch {
	case p == "":
		return dispatch.PlanReference{}, "a reference names no path", nil
	case strings.ContainsRune(p, '\\') || strings.ContainsFunc(p, unicode.IsControl):
		return dispatch.PlanReference{}, fmt.Sprintf("the reference %q is not a slash-separated path", cut(p, 200)), nil
	case path.IsAbs(p):
		return dispatch.PlanReference{}, fmt.Sprintf("the reference %q is absolute: a reference is relative to the crew's working directory", cut(p, 200)), nil
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return dispatch.PlanReference{}, fmt.Sprintf("the reference %q leaves the crew's working directory", cut(rr.Path, 200)), nil
	}
	spec := base + ":" + prefix + p
	if p == "." {
		spec = base + ":" + prefix
	}
	out, err := gitcli.Run(ctx, cwd, "rev-parse", "--verify", "--quiet", spec)
	if err != nil && !gitUnresolved(err) {
		return dispatch.PlanReference{}, "", err
	}
	object := strings.TrimSuffix(out, "\n")
	if err != nil || !isFullCommit(object) {
		return dispatch.PlanReference{}, fmt.Sprintf("the reference %s is not at the base %s", cut(p, 200), base[:12]), nil
	}
	notKind := fmt.Sprintf("the reference %s at the base %s is neither a file nor a directory", cut(p, 200), base[:12])
	kind, err := gitcli.Run(ctx, cwd, "cat-file", "-t", object)
	if err != nil {
		// cat-file -t fails alike for a submodule's commit the repository
		// has not and for a file's or a directory's object it has not or
		// cannot read; the entry's mode tells them apart.
		if p == "." {
			return dispatch.PlanReference{}, "", err
		}
		switch gitlink, lerr := gitlinkAt(ctx, cwd, base, prefix+p); {
		case lerr != nil:
			return dispatch.PlanReference{}, "", lerr
		case gitlink:
			return dispatch.PlanReference{}, notKind, nil
		}
		return dispatch.PlanReference{}, "", err
	}
	kind = strings.TrimSuffix(kind, "\n")
	if kind != dispatch.ObjectBlob && kind != dispatch.ObjectTree {
		return dispatch.PlanReference{}, notKind, nil
	}
	return dispatch.PlanReference{Path: p, Start: rr.Start, End: rr.End, Note: rr.Note, Object: object, Kind: kind}, "", nil
}

// gitlinkAt reports whether the base's tree entry at name, a path from the
// repository's top, is a submodule's (mode 160000): git ls-tree -z
// --full-tree <base> -- <name>, the name a literal path
// (GIT_LITERAL_PATHSPECS), which reads the trees down to the entry and
// never the entry's own object. The error is git's failure, or an entry
// the base has not (rev-parse had just found it there).
func gitlinkAt(ctx context.Context, cwd, base, name string) (bool, error) {
	out, err := gitcli.RunEnv(ctx, cwd, map[string]string{"GIT_LITERAL_PATHSPECS": "1"}, "ls-tree", "-z", "--full-tree", base, "--", name)
	if err != nil {
		return false, err
	}
	entry, _, _ := strings.Cut(out, "\x00")
	meta, got, ok := strings.Cut(entry, "\t")
	if !ok || got != name {
		return false, fmt.Errorf("git ls-tree: the base has no entry %q", name)
	}
	mode, _, _ := strings.Cut(meta, " ")
	return mode == "160000", nil
}

// handleDispatchApprovePlan approves a plan revision (plan.approve), the
// operator's: body {opId, expectedRev?, ticketRev?, note?, byName?},
// expectedRev the revision's and ticketRev the ticket's; {receipt, plan}.
// The operation is replayed first, so a resend is answered from its
// receipt whatever the catalog became since; a new one has the
// revision's agents resolved as a launch resolves them, and one that
// resolves otherwise now refuses it 409 stale_plan with words naming the
// agent and what changed ("revise it": only a new revision can cover the
// new selection), keeping nothing; then the store's rules (a withdrawn
// revision invalid, a handed-over comment edited or gone stale_plan,
// stale_revision for either revision, 404 not_found).
func (s *Server) handleDispatchApprovePlan(w http.ResponseWriter, r *http.Request) {
	var body planApproveBody
	ds, _, id, ok := s.opAt(w, r, nil, &body, &body.dispatchOp)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	op := body.op()
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentPlanApprove, planApproveArgs{ID: id, TicketRev: body.TicketRev, Note: body.Note})
	if err != nil || found {
		s.writeOp(w, "plan revision", rc, err, false, "plan", s.planAfter(ctx, ds, id))
		return
	}
	switch v, err := ds.Plan(ctx, id); {
	case errors.Is(err, dispatch.ErrNotFound):
		// The store refuses it not_found, its receipt kept.
	case err != nil:
		s.dispatchError(w, "plan revision", err)
		return
	default:
		f, err := s.planFacts.get(v.Digest, func() (json.RawMessage, error) { return v.Body, nil })
		if err != nil {
			s.dispatchError(w, "plan revision", err)
			return
		}
		if words := agentsChange(s.Catalog(), f, s.cfg.Yolo); words != "" {
			writeError(w, http.StatusConflict, dispatch.RefusalStalePlan, fmt.Sprintf("%s since revision %d was made: revise it", words, v.N))
			return
		}
	}
	rc, err = ds.ApprovePlan(ctx, op, id, body.TicketRev, body.Note)
	s.writeOp(w, "plan revision", rc, err, false, "plan", s.planAfter(ctx, ds, id))
}

// handleDispatchWithdrawPlan withdraws a plan revision (plan.withdraw):
// body {opId, expectedRev?, byName?}, expectedRev the revision's;
// {receipt, plan}; 409 invalid for one withdrawn already; 404 not_found.
// The launches made from it keep naming it.
func (s *Server) handleDispatchWithdrawPlan(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, _, id, ok := s.opAt(w, r, nil, &body, &body)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.WithdrawPlan(ctx, body.op(), id)
	s.writeOp(w, "plan revision", rc, err, false, "plan", s.planAfter(ctx, ds, id))
}
