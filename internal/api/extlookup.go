package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// A lookup (docs/plans/context-seams.md, CT-2): an attempt's agent asks a
// context provider while it works, POST /api/sessions/{id}/context/lookup,
// with the session's own token alone. The agent names the op and its
// words; which run, task, attempt, session and commits the question is
// about are the server's, from the session's own attempt and its
// worktree's HEAD. The provider is asked through the guard under a kind
// of its own (extKindLookup), so lookups held by a slow provider never
// take the slots an attempt's start asks its blocks in. A session has at
// most maxLookupsInFlight lookups in flight, each counted until the
// provider's call returns. What the provider answers is checked
// (ext.LookupAnswer.Valid) and handed back as reference material; a late
// pack's blocks only once they are recorded against the attempt
// (crew.RecordLookupContext). Nothing a lookup asks or answers is logged.

const (
	// extKindLookup is the guard's kind of a provider's lookups: its slots
	// and its failure log apart from the attempt start's (ext.KindContext).
	extKindLookup = ext.KindContext + ".lookup"
	// maxLookupBody bounds the route's body.
	maxLookupBody = 4 << 10
	// maxLookupsInFlight bounds a session's lookups in flight.
	maxLookupsInFlight = 2
	// lookupHeadWait bounds the read of the worktree's HEAD.
	lookupHeadWait = 2 * time.Second
	// defaultLookupLimit is a lookup's limit when the body names none.
	defaultLookupLimit = 20
	// lookupUnavailable is the words of a lookup no provider answered.
	lookupUnavailable = "context is unavailable now; carry on without it"
)

// lookupBody is the route's body: the agent's alone.
type lookupBody struct {
	Op     string `json:"op"`
	Query  string `json:"query,omitempty"`
	Path   string `json:"path,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Source string `json:"source,omitempty"`
}

// lookupFlights counts each session's lookups in flight.
type lookupFlights struct {
	mu sync.Mutex
	n  map[string]int
}

// take counts one more lookup of the session, or answers false when it
// has maxLookupsInFlight already; release, once, gives it back.
func (f *lookupFlights) take(session string) (release func(), ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == nil {
		f.n = map[string]int{}
	}
	if f.n[session] >= maxLookupsInFlight {
		return nil, false
	}
	f.n[session]++
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.n[session]--; f.n[session] <= 0 {
				delete(f.n, session)
			}
		})
	}, true
}

// lookupProvider is a context provider that answers lookups: its guard
// (for its name and its Start's state) and its lookup.
func (g *guardContext) lookup() (ext.ContextLookup, bool) {
	l, ok := g.p.(ext.ContextLookup)
	return l, ok
}

// answersLookups reports whether a registered context provider answers
// lookups and its Start has succeeded (or it needs none): the guide lists
// context_lookup only then (needContextLookup).
func (s *Server) answersLookups() bool {
	for _, g := range s.exts.context {
		if _, ok := g.lookup(); ok && live(g.ready) {
			return true
		}
	}
	return false
}

// lookupSource is the provider a lookup asks: the one named source, else
// the first in registration order that answers lookups; nil when none does.
func (s *Server) lookupSource(source string) *guardContext {
	for _, g := range s.exts.context {
		if _, ok := g.lookup(); ok && (source == "" || g.name == source) {
			return g
		}
	}
	return nil
}

// extLookingUp, when a test sets it, is told the session of every lookup
// as the provider is about to be asked, in its slot; nil outside tests.
var extLookingUp atomic.Pointer[func(session string)]

// handleContextLookup answers POST /api/sessions/{id}/context/lookup:
// 200 {source, items, coverage, more, note?}; to every caller but the
// attempt's own agent 401, the workbench token 403 agent_only, before
// anything of the session is said (lookupCaller); to that agent 409
// not_running, 409 lane (a comparison's lane, which asks no context), 404
// no_context_lookup, 400 invalid_request, 429 lookup_busy and 503
// lookup_unavailable (docs/protocol.md, Context lookup).
func (s *Server) handleContextLookup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.lookupCaller(w, r)
	if !ok {
		return
	}
	var body lookupBody
	if err := decodeJSONLimit(w, r, &body, maxLookupBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := d.Info().ID
	scope, err := s.runs.LookupScope(id)
	switch {
	case errors.Is(err, crew.ErrNotAnAttempt):
		writeError(w, http.StatusNotFound, "not_an_attempt", err.Error())
		return
	case errors.Is(err, crew.ErrNotRunning):
		writeError(w, http.StatusConflict, "not_running", err.Error())
		return
	case err != nil:
		s.taskError(w, "lookup", id, err)
		return
	}
	g := s.lookupSource(body.Source)
	if g == nil {
		writeError(w, http.StatusNotFound, "no_context_lookup", "no context provider answers lookups here")
		return
	}
	limit := defaultLookupLimit
	if body.Limit != nil {
		limit = *body.Limit
	}
	req := ext.LookupRequest{Op: body.Op, Query: body.Query, Path: body.Path, Symbol: body.Symbol, Limit: limit,
		Run: scope.Run, Task: scope.Task, Attempt: scope.Attempt, Kind: scope.Kind, Session: id, Start: scope.Start, Base: scope.Base}
	if err := agentFieldsValid(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), "ext: "))
		return
	}
	req.Head = worktreeHead(r.Context(), scope.Worktree)
	if err := req.Valid(); err != nil {
		// The server's own fields: an attempt not yet of a shape a lookup
		// names (no start yet).
		writeError(w, http.StatusConflict, "not_running", "the attempt cannot be looked up for now")
		return
	}
	look, _ := g.lookup()
	if !live(g.ready) {
		writeError(w, http.StatusServiceUnavailable, "lookup_unavailable", lookupUnavailable)
		return
	}
	release, ok := s.contextLookups.take(id)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "lookup_busy", "two lookups of this session are in flight: wait for one")
		return
	}
	answer, sent, answered := extCallTold(r.Context(), s, extKindLookup, g.name, ext.LookupWait, ext.LookupAnswer{}, ext.LookupAnswer.Valid,
		func(ctx context.Context) (ext.LookupAnswer, error) {
			// Counted until the provider's call returns, not until the
			// route answers: a session that asks again after a late answer
			// holds no more than its two slots.
			defer release()
			if f := extLookingUp.Load(); f != nil {
				(*f)(id)
			}
			a, err := look.Lookup(ctx, req)
			return copyLookupAnswer(a), err
		})
	if !sent {
		release()
		writeError(w, http.StatusTooManyRequests, "lookup_busy", "the context provider has every lookup it takes in flight: wait for one")
		return
	}
	if !answered {
		writeError(w, http.StatusServiceUnavailable, "lookup_unavailable", lookupUnavailable)
		return
	}
	reply := lookupReply{Source: g.name, Items: answer.Items, Blocks: answer.Blocks, Coverage: answer.Coverage, More: answer.More, Note: answer.Note}
	if reply.Items == nil {
		reply.Items = []ext.LookupItem{}
	}
	if b, err := json.Marshal(reply); err != nil || len(b) > ext.MaxLookupAnswer {
		writeError(w, http.StatusServiceUnavailable, "lookup_unavailable", lookupUnavailable)
		return
	}
	// A late pack's blocks reach the agent only once they are recorded
	// against its attempt (crew.RecordLookupContext): when they cannot
	// be, the answer carries none, and says so in the server's words.
	if len(reply.Blocks) > 0 {
		if err := s.runs.RecordLookupContext(r.Context(), scope.Run, scope.Task, scope.Attempt, g.name, reply.Blocks); err != nil {
			reply.Blocks, reply.Withheld = nil, lookupWithheld
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// lookupWithheld is the server's words for a late pack it could not
// record, and so does not give.
const lookupWithheld = "this context could not be recorded, so it is not given"

// agentFieldsValid checks the fields of r the agent gave (its op and
// words), with the server's own set to stand-ins of their shapes, so a
// request the agent shaped wrongly is refused before anything is read and
// says what is wrong with it.
func agentFieldsValid(r ext.LookupRequest) error {
	zero := strings.Repeat("0", 40)
	r.Run, r.Task, r.Attempt, r.Kind, r.Session, r.Start, r.Base, r.Head = "x-00000000", "x", 1, "work", "x", zero, zero, ""
	return r.Valid()
}

// lookupCaller judges who asks before anything of the session is said.
// The session's own agent token is a caller the server knows: a
// comparison's lane's is answered 409 lane, as at every door closed to a
// lane and in every state of the lane (launching, held, its run gone:
// agentTokenOK), and an attempt's is served. Every other caller is
// refused as one with no right here, whatever the session is or whether
// it is there: the workbench token 403 agent_only (the route is an
// agent's, opAgentOnly), any other token, a session's own where that
// session is neither a lane's nor an attempt's, or none, 401; so no
// caller but the session's own agent learns from the answer whether the
// session exists, is an attempt or runs. It answers having written the
// refusal when it reports false.
func (s *Server) lookupCaller(w http.ResponseWriter, r *http.Request) (session.Driver, bool) {
	if d, found := s.registry.Get(r.PathValue("id")); found {
		switch s.agentTokenOK(r, d) {
		case agentTokenLaneClosed:
			writeLaneClosed(w)
			return nil, false
		case agentTokenServed:
			if _, _, _, attempt := s.runs.AttemptOf(d.Info().ID); attempt {
				return d, true
			}
		}
	}
	p, ok := s.authenticate(w, r, false)
	if !ok {
		return nil, false
	}
	if p.admin && opAgentOnly("context_lookup") {
		writeError(w, http.StatusForbidden, "agent_only", "a lookup is asked by the attempt's own session, with its token")
		return nil, false
	}
	if !s.limiter.allow(clientKey(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return nil, false
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "agent token required")
	return nil, false
}

// lookupReply is the route's answer.
type lookupReply struct {
	Source   string             `json:"source"`
	Items    []ext.LookupItem   `json:"items"`
	Blocks   []ext.ContextBlock `json:"blocks,omitempty"`
	Coverage ext.LookupCoverage `json:"coverage"`
	More     bool               `json:"more,omitempty"`
	Note     string             `json:"note,omitempty"`
	// Withheld is the server's own words when a late pack could not be
	// recorded and so is not given; never the provider's.
	Withheld string `json:"withheld,omitempty"`
}

// copyLookupAnswer is a with its slices copied: the guard's own, never
// the provider's.
func copyLookupAnswer(a ext.LookupAnswer) ext.LookupAnswer {
	a.Items = copyOrNil(a.Items)
	a.Blocks = copyOrNil(a.Blocks)
	return a
}

// worktreeHead is the commit the worktree's HEAD names, a full id, read
// within lookupHeadWait; "" when it cannot be read.
func worktreeHead(ctx context.Context, worktree string) string {
	ctx, cancel := context.WithTimeout(ctx, lookupHeadWait)
	defer cancel()
	out, err := gitcli.Run(ctx, worktree, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	head := strings.TrimSpace(out)
	if err != nil || !gitObjectID(head) {
		return ""
	}
	return head
}
