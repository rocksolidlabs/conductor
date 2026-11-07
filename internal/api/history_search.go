package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// The search routes (docs/plans/history.md, "The search routes", PR-4):
// GET /api/history, /api/history/entry, /api/history/similar and
// /api/history/command, each the admin's (requireAdmin), as Dispatch's
// search is: the query in the query string, never logged (the request
// line names the route's pattern, logPath). Each reads only its query
// string, every parameter named and each once (readQuery), and bounds
// what it answers. A search's cost is bounded per request (the index's
// own bounds of a page, MaxScanned and MaxRanked, and a deadline) and per
// client (at most maxHistoryReadsPerClient of them in flight, and
// maxHistoryReads in all).

// The routes' bounds.
const (
	// maxHistoryReply bounds a search's reply: rows past it are cut and
	// the reply says so (truncated).
	maxHistoryReply = 2 << 20
	// maxHistoryAfter bounds a cursor (history.Page.Next).
	maxHistoryAfter = 2 << 10
	// maxHistoryQueryString bounds a route's whole query string, before
	// any of it is read: a line (512 bytes), a cursor (2 KiB), escaped.
	maxHistoryQueryString = 12 << 10
	// maxHistoryActor bounds an actor (Dispatch's MaxActor, the ledger's).
	maxHistoryActor = 60
	// maxHistoryReads and maxHistoryReadsPerClient bound the reads of the
	// index in flight: in all, and from one client.
	maxHistoryReads          = 4
	maxHistoryReadsPerClient = 2
	// historyReadTimeout bounds one read of the index, and
	// historyWriteTimeout the write of its reply: a client that stops
	// reading holds the handler no longer (its slot is let go before).
	historyReadTimeout  = 15 * time.Second
	historyWriteTimeout = 15 * time.Second
	// maxEntryLinks bounds an entry's links each way (/api/history/entry).
	maxEntryLinks = history.MaxLinks
)

// MaxHistoryReply is maxHistoryReply, the bound conductor history reads a
// search's reply under.
const MaxHistoryReply = maxHistoryReply

// historyReplyLimit is maxHistoryReply; a test lowers it.
var historyReplyLimit = maxHistoryReply

// historyWriteLimit is historyWriteTimeout; a test shortens it.
var historyWriteLimit = historyWriteTimeout

// historyGate counts the index's reads in flight, in all and by client.
type historyGate struct {
	mu  sync.Mutex
	all int
	by  map[string]int
}

func (g *historyGate) enter(client string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.all >= maxHistoryReads || g.by[client] >= maxHistoryReadsPerClient {
		return false
	}
	if g.by == nil {
		g.by = map[string]int{}
	}
	g.all++
	g.by[client]++
	return true
}

func (g *historyGate) leave(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.all--
	if g.by[client]--; g.by[client] <= 0 {
		delete(g.by, client)
	}
}

// inFlight is the reads in flight, in all.
func (g *historyGate) inFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.all
}

// historyReq is one read of the index for a request: its query string,
// the index, the read's context (its deadline) and its slot in the gate.
// The slot is let go on every way out of the handler (release, deferred)
// and before the reply is made (reply), so a client that stops reading
// holds none; the write itself, to its last byte on the connection, has
// its own deadline (historyRoute).
type historyReq struct {
	s      *Server
	w      http.ResponseWriter
	q      dispatchQuery
	ix     *history.Index
	ctx    context.Context
	cancel context.CancelFunc
	client string
	held   bool
}

// release lets go of the read's slot and its context, once.
func (h *historyReq) release() {
	if h.held {
		h.held = false
		h.cancel()
		h.s.historyReads.leave(h.client)
	}
}

// reply answers status and v: v encoded first, the slot let go, then
// written (historyRoute sends it).
func (h *historyReq) reply(status int, v any) {
	var b bytes.Buffer
	_ = json.NewEncoder(&b).Encode(v)
	h.release()
	h.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	h.w.WriteHeader(status)
	_, _ = h.w.Write(b.Bytes())
}

// historyRoute is the one way out of the history's read routes, every
// reply passing through it: a read's, and every refusal and error (the
// admin check's, a query string refused, history_busy, a read's own). The
// handler writes into memory (a reply is at most historyReplyLimit), and
// nothing reaches the connection before the reply is finished: then the
// write deadline is set, the reply is written, its length said, and
// flushed under it, so net/http has nothing left to write after the
// handler, and only then is the deadline cleared (the connection's next
// request has none of its own). A client that stops reading fails the
// write or the flush at the deadline, and its connection is closed.
func historyRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := &heldReply{header: w.Header()}
		next(b, r)
		b.send(w)
	}
}

// heldReply is a reply held in memory until its handler returns.
type heldReply struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *heldReply) Header() http.Header { return b.header }

func (b *heldReply) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *heldReply) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

// send writes the reply to w under the write deadline, set before its
// first byte and cleared once it is flushed.
func (b *heldReply) send(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(historyWriteLimit))
	b.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Length", strconv.Itoa(b.body.Len()))
	w.WriteHeader(b.status)
	_, err := w.Write(b.body.Bytes())
	if err == nil {
		err = rc.Flush()
	}
	// A write that failed (its deadline passed) leaves the deadline set:
	// net/http closes the connection. A writer that cannot flush leaves
	// nothing better to do than clear it.
	if err == nil || errors.Is(err, http.ErrNotSupported) {
		_ = rc.SetWriteDeadline(time.Time{})
	}
}

// refuse answers an error in words, as writeError does.
func (h *historyReq) refuse(status int, code, message string) {
	h.reply(status, map[string]apiError{"error": {Code: code, Message: message}})
}

// historyRead opens a read of the index for a request: its query string
// within its bound and of the names given, a slot in the gate, a deadline
// and the index. ok false has answered.
func (s *Server) historyRead(w http.ResponseWriter, r *http.Request, names ...string) (*historyReq, bool) {
	if s.historyUnavailable(w) {
		return nil, false
	}
	if len(r.URL.RawQuery) > maxHistoryQueryString {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("the query string is %d bytes, more than %d", len(r.URL.RawQuery), maxHistoryQueryString))
		return nil, false
	}
	q, err := readQuery(r, names...)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil, false
	}
	client := clientKey(r)
	if !s.historyReads.enter(client) {
		writeError(w, http.StatusTooManyRequests, "history_busy", "too many history reads in flight; try again")
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.historyTimeout())
	h := &historyReq{s: s, w: w, q: q, ctx: ctx, cancel: cancel, client: client, held: true}
	if s.historyHold != nil {
		s.historyHold(ctx, r)
	}
	if s.history != nil {
		h.ix = s.history.index()
	}
	return h, true
}

// historyTimeout is a read's deadline: historyReadTimeout, or a test's.
func (s *Server) historyTimeout() time.Duration {
	if s.historyDeadline > 0 {
		return s.historyDeadline
	}
	return historyReadTimeout
}

// historyIndexing is the reply's indexing: the indexer's state, the
// index's (other_version, checking), and how far back it holds.
func (s *Server) historyIndexing(ctx context.Context, ix *history.Index, pageState string) map[string]any {
	out := map[string]any{"state": indexOpening}
	if s.history != nil {
		out = s.history.indexing()
	}
	if pageState != "" {
		out["state"] = pageState
	}
	if ix != nil {
		if st, err := ix.Stats(ctx); err == nil {
			if !st.Oldest.IsZero() {
				out["oldest"] = st.Oldest
			}
			if st.Dropped > 0 {
				out["dropped"] = st.Dropped
			}
		}
	}
	return out
}

// fail answers an error of the read in words, the read's context judged
// first: a client that left gets no reply and is no failure of the
// server's (nothing is logged), a read past its deadline is said as one;
// then a bad query or cursor, a cursor of an index rebuilt since, an index
// not ready.
func (h *historyReq) fail(err error) {
	words := func(err error, prefix error) string {
		return strings.TrimPrefix(strings.TrimPrefix(err.Error(), prefix.Error()), ": ")
	}
	switch cerr := h.ctx.Err(); {
	case errors.Is(cerr, context.Canceled) || (cerr == nil && errors.Is(err, context.Canceled)):
		h.release()
	case errors.Is(cerr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		h.refuse(http.StatusServiceUnavailable, "history_timeout", fmt.Sprintf("the history's read took longer than %s; narrow the search", h.s.historyTimeout()))
	case errors.Is(err, history.ErrQuery):
		h.refuse(http.StatusBadRequest, "bad_query", words(err, history.ErrQuery))
	case errors.Is(err, history.ErrCursor):
		h.refuse(http.StatusBadRequest, "bad_cursor", "the cursor is not one of this search's pages")
	case errors.Is(err, history.ErrStaleCursor):
		h.refuse(http.StatusConflict, "stale_cursor", "the history was rebuilt; search again")
	case errors.Is(err, history.ErrGone):
		h.refuse(http.StatusNotFound, "entry_gone", "this entry is no longer in the history")
	case errors.Is(err, history.ErrNotReady), errors.Is(err, history.ErrClosed):
		h.refuse(http.StatusServiceUnavailable, "history_not_ready", "the history is not ready; try again")
	default:
		h.s.log.Warn("a history read failed", "err", err)
		h.refuse(http.StatusInternalServerError, "internal", "the history could not be read")
	}
}

// handleHistorySearch answers GET /api/history?q=&limit=&after=: a page of
// the entries whose text holds the line's words and that pass its
// filters, {entries, next?, total, totalCapped?, cut?, indexing,
// readOnly?, truncated?, stale?}. Brakeman's own sessions and runs are
// left out unless the line names a crew or the kind brief.item. A
// forgotten session record is in none of it: its rows are gone from the
// index before its Forget answers, so the search reads the index alone.
func (s *Server) handleHistorySearch(w http.ResponseWriter, r *http.Request) {
	h, ok := s.historyRead(w, r, "q", "limit", "after")
	if !ok {
		return
	}
	defer h.release()
	q, ix, ctx := h.q, h.ix, h.ctx
	line, err := q.one("q")
	var after string
	if err == nil {
		after, err = q.one("after")
	}
	var limit int64
	if err == nil {
		limit, err = q.number("limit")
	}
	if err == nil && (limit < 0 || limit > history.MaxLimit) {
		err = fmt.Errorf("limit is 1 to %d, not %d", history.MaxLimit, limit)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	if len(after) > maxHistoryAfter {
		h.refuse(http.StatusBadRequest, "bad_cursor", fmt.Sprintf("the cursor is %d bytes, more than %d", len(after), maxHistoryAfter))
		return
	}
	hq, err := history.ParseQuery(line)
	if err != nil {
		h.fail(err)
		return
	}
	hq.Limit, hq.After = int(limit), after
	_, crews := hq.Filter("crew")
	if kinds, _ := hq.Filter("kind"); !crews && !slices.Contains(kinds, "brief.item") {
		hq.ExceptCrews = []string{crew.ClassBrakeman}
	}
	readOnly := s.sessionRecords != nil && func() bool { ro, _ := s.sessionRecords.ReadOnly(); return ro }()
	if ix == nil {
		out := map[string]any{"entries": []any{}, "total": 0, "indexing": s.historyIndexing(ctx, nil, "")}
		if readOnly {
			out["readOnly"] = true
		}
		h.reply(http.StatusOK, out)
		return
	}
	page, err := ix.Search(ctx, hq)
	if err != nil {
		h.fail(err)
		return
	}
	av := s.newAvailability(ctx, ix)
	defer av.close()
	entries, n := s.servePage(av, page.Entries)
	next := page.Next
	if n < len(page.Entries) {
		// The rows past the bound are the next page's: it reads on from
		// the last row served.
		next = page.After(n - 1)
	}
	out := map[string]any{"entries": entries, "total": page.Total, "indexing": s.historyIndexing(ctx, ix, page.State)}
	if next != "" {
		out["next"] = next
	}
	if page.TotalCapped {
		out["totalCapped"] = true
	}
	if page.Cut != "" {
		out["cut"] = page.Cut
	}
	if n < len(page.Entries) {
		out["truncated"] = true
	}
	if readOnly {
		out["readOnly"] = true
	}
	h.reply(http.StatusOK, out)
}

// servePage serves a page's hits, as many as fit the reply's bound in
// their order (n of them), the first always.
func (s *Server) servePage(av *availability, hits []history.Hit) (out []servedEntry, n int) {
	out = []servedEntry{}
	used := 1024 // the reply's own fields
	for _, h := range hits {
		e := s.serveEntry(av, h.Entry, h.Snippet, h.Repeats, h.Links, false)
		b, _ := json.Marshal(e)
		if len(out) > 0 && used+len(b)+1 > historyReplyLimit {
			break
		}
		used += len(b) + 1
		out = append(out, e)
	}
	return out, len(out)
}

// handleHistoryEntry answers GET /api/history/entry?key=: one entry by
// its key, its links each way (at most maxEntryLinks) and its full
// availability, within the reply's bound (linksCut past it); 404
// entry_gone for a key the index no longer holds, or whose session record
// the journal no longer holds (availability.entryRead).
func (s *Server) handleHistoryEntry(w http.ResponseWriter, r *http.Request) {
	h, ok := s.historyRead(w, r, "key")
	if !ok {
		return
	}
	defer h.release()
	q, ix, ctx := h.q, h.ix, h.ctx
	key, err := q.one("key")
	if err == nil && (key == "" || len(key) > history.MaxRef) {
		err = fmt.Errorf("key is an entry's key, 1 to %d bytes", history.MaxRef)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "bad_key", err.Error())
		return
	}
	if ix == nil {
		h.refuse(http.StatusServiceUnavailable, "history_not_ready", "the history is not ready; try again")
		return
	}
	av := s.newAvailability(ctx, ix)
	defer av.close()
	hit, err := ix.Entry(ctx, key)
	if err == nil {
		err = av.entryRead(hit.Entry)
	}
	if err != nil {
		h.fail(err)
		return
	}
	links := hit.Links
	if l, err := ix.Links(ctx, hit.Ref, maxEntryLinks); err == nil {
		links = l
	}
	e := s.serveEntry(av, hit.Entry, hit.Snippet, hit.Repeats, links, true)
	out := map[string]any{"entry": e, "indexing": s.historyIndexing(ctx, ix, "")}
	// Within the reply's bound: the links past it are cut, the last
	// first, and the reply says so.
	for b, _ := json.Marshal(out); len(b)+1 > historyReplyLimit && len(e.Links) > 0; b, _ = json.Marshal(out) {
		e.Links = e.Links[:len(e.Links)/2]
		out["entry"], out["linksCut"] = e, true
	}
	h.reply(http.StatusOK, out)
}

// handleHistorySimilar answers GET /api/history/similar?key=&scope=&limit=:
// the receipts of the same command elsewhere (history.Index.Similar), the
// entry's run chain left out, at most history.MaxSimilar; 404 entry_gone
// as the entry route answers it.
func (s *Server) handleHistorySimilar(w http.ResponseWriter, r *http.Request) {
	h, ok := s.historyRead(w, r, "key", "scope", "limit")
	if !ok {
		return
	}
	defer h.release()
	q, ix, ctx := h.q, h.ix, h.ctx
	key, err := q.one("key")
	var scope string
	var limit int64
	if err == nil {
		scope, err = q.one("scope")
	}
	if err == nil {
		limit, err = q.number("limit")
	}
	switch {
	case err != nil:
	case key == "" || len(key) > history.MaxRef:
		err = fmt.Errorf("key is an entry's key, 1 to %d bytes", history.MaxRef)
	case scope != "" && scope != history.ScopeRepo && scope != history.ScopeAll:
		err = fmt.Errorf("scope is %s or %s", history.ScopeRepo, history.ScopeAll)
	case limit < 0 || limit > history.MaxSimilar:
		err = fmt.Errorf("limit is 1 to %d, not %d", history.MaxSimilar, limit)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "bad_query", err.Error())
		return
	}
	if limit == 0 {
		limit = 10
	}
	if ix == nil {
		h.refuse(http.StatusServiceUnavailable, "history_not_ready", "the history is not ready; try again")
		return
	}
	av := s.newAvailability(ctx, ix)
	defer av.close()
	hit, err := ix.Entry(ctx, key)
	if err == nil {
		err = av.entryRead(hit.Entry)
	}
	if err != nil {
		h.fail(err)
		return
	}
	hits, err := ix.Similar(ctx, key, scope, runChain(ctx, ix, hit.Run), int(limit))
	if err != nil {
		h.fail(err)
		return
	}
	entries, n := s.servePage(av, hits)
	out := map[string]any{"entries": entries}
	if n < len(hits) {
		out["truncated"] = true
	}
	h.reply(http.StatusOK, out)
}

// runChain is the runs a run continues and that continue it, as the
// index's continues links say, at most maxRunChain each way.
func runChain(ctx context.Context, ix *history.Index, run string) []string {
	if run == "" {
		return nil
	}
	chain := []string{run}
	seen := map[string]bool{run: true}
	for i := 0; i < len(chain) && len(chain) < 2*maxRunChain+1; i++ {
		ls, err := ix.Links(ctx, hRunRef(chain[i]), history.MaxLinks)
		if err != nil {
			break
		}
		for _, l := range append(slices.Clone(ls.Out), ls.In...) {
			id, ok := strings.CutPrefix(l.Ref, "run:")
			if l.Rel == relContinues && ok && !seen[id] {
				seen[id] = true
				chain = append(chain, id)
			}
		}
	}
	return chain
}

// handleHistoryCommand answers GET /api/history/command?run=&actor=&id=:
// the command receipt of that actor, the one an entry names, never the
// caller's (the run's command route finds the caller's own: actorOf).
func (s *Server) handleHistoryCommand(w http.ResponseWriter, r *http.Request) {
	h, ok := s.historyRead(w, r, "run", "actor", "id")
	if !ok {
		return
	}
	defer h.release()
	q, ctx := h.q, h.ctx
	run, err := q.one("run")
	var actor, id string
	if err == nil {
		actor, err = q.one("actor")
	}
	if err == nil {
		id, err = q.one("id")
	}
	if err == nil && (!crew.ValidRunID(run) || actor == "" || len(actor) > maxHistoryActor || !commandIDPattern.MatchString(id)) {
		err = fmt.Errorf("a run's id, an actor of 1 to %d bytes and a command id of 1 to 32 letters, digits, '.', '_', '~' or '-'", maxHistoryActor)
	}
	if err != nil {
		h.refuse(http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.ledger == nil {
		h.refuse(http.StatusServiceUnavailable, "ledger_unavailable", "the ledger could not be opened; see the server's log")
		return
	}
	c, found, err := s.ledger.Receipt(ctx, run, actor, id)
	if ctx.Err() != nil {
		h.fail(ctx.Err())
		return
	}
	if errors.Is(err, ledger.ErrUnavailable) || errors.Is(err, ledger.ErrClosed) {
		h.refuse(http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
		return
	}
	if err != nil {
		s.log.Warn("a command receipt could not be read for the history", "run", run, "command", id, "err", err)
		h.refuse(http.StatusInternalServerError, "internal", "the receipt could not be read")
		return
	}
	if !found {
		h.refuse(http.StatusNotFound, "not_found", "no such command")
		return
	}
	var receipt json.RawMessage
	if c.Receipt != "" {
		receipt = json.RawMessage(c.Receipt)
	}
	h.reply(http.StatusOK, map[string]any{"command": map[string]any{
		"id": c.ID, "actor": c.Actor, "intent": c.Intent, "status": c.Status, "receipt": receipt, "createdAt": c.CreatedAt,
	}})
}

// ---- an entry as served

// servedEntry is an entry as the routes serve it: what it is, its six
// fields (source, host, project, revision, when, availability), the page
// that shows its evidence, and its links. Never its body: a snippet of it.
type servedEntry struct {
	Key          string          `json:"key"`
	Kind         string          `json:"kind"`
	Class        string          `json:"class"`
	Outcome      string          `json:"outcome,omitempty"`
	Title        string          `json:"title"`
	Summary      string          `json:"summary,omitempty"`
	Snippet      string          `json:"snippet,omitempty"`
	At           time.Time       `json:"at"`
	Actor        string          `json:"actor,omitempty"`
	Source       servedSource    `json:"source"`
	Host         servedHost      `json:"host"`
	Project      servedProject   `json:"project"`
	Revision     servedRevision  `json:"revision"`
	Agent        *servedAgent    `json:"agent,omitempty"`
	Crew         string          `json:"crew,omitempty"`
	Run          string          `json:"run,omitempty"`
	Ticket       string          `json:"ticket,omitempty"`
	Anchor       servedAnchor    `json:"anchor"`
	URL          string          `json:"url"`
	Repeats      history.Repeats `json:"repeats"`
	Links        []servedLink    `json:"links"`
	Availability availabilityOf  `json:"availability"`
}

type servedSource struct {
	Store string `json:"store"`
	Label string `json:"label"`
	Ref   string `json:"ref"`
}

type servedHost struct {
	Name   string `json:"name,omitempty"`
	User   string `json:"user,omitempty"`
	Hosted bool   `json:"hosted,omitempty"`
	Where  string `json:"where,omitempty"`
}

type servedProject struct {
	Key  string `json:"key,omitempty"`
	Repo string `json:"repo,omitempty"`
	Tree string `json:"tree,omitempty"`
}

type servedRevision struct {
	Label  string `json:"label,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type servedAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Session string `json:"session,omitempty"`
}

type servedAnchor struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

// servedLink is a link seen from the entry: its relation, which way it
// goes (out from the entry, in to it), the other end's ref and, when the
// index holds an entry for it, that entry's class, outcome and title; and
// the page that shows it.
type servedLink struct {
	Rel     string `json:"rel"`
	Dir     string `json:"dir"`
	Ref     string `json:"ref"`
	Label   string `json:"label"`
	Key     string `json:"key,omitempty"`
	Class   string `json:"class,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	URL     string `json:"url,omitempty"`
}

// serveEntry is an entry as served; full adds what /api/history/entry
// computes beyond a page's (resume, a commit's presence, a log's size).
func (s *Server) serveEntry(av *availability, e history.Entry, snippet string, rep history.Repeats, links history.Links, full bool) servedEntry {
	out := servedEntry{
		Key: e.Key, Kind: e.Kind, Class: e.Class, Outcome: e.Outcome, Title: e.Title, Summary: e.Summary, Snippet: snippet,
		At: e.At, Actor: e.Actor, Crew: e.Crew, Run: e.Run, Ticket: e.Ticket, Repeats: rep, Links: []servedLink{},
		Source:   servedSource{Store: storeOfKind(e.Kind), Label: labelOfKind(e.Kind, s.catalogAgentName(e.Agent)), Ref: e.Ref},
		Host:     servedHost{Name: e.Host, User: e.HostUser, Hosted: e.Hosted, Where: e.Where},
		Project:  servedProject{Key: e.Project, Repo: e.Repo, Tree: e.Tree},
		Revision: servedRevision{Label: cmpOr(e.Revision, hShort(e.Commit)), Commit: e.Commit},
		URL:      historyURL(e.Kind, e.Key, e.Ref, e.Seq, e.Title, av.listed),
		Anchor:   servedAnchor{Ref: e.Anchor, Label: anchorLabel(e.Anchor), URL: historyURL("", e.Anchor, e.Anchor, 0, "", av.listed)},
	}
	if e.Kind == "launch" {
		out.URL = launchURL(e)
	}
	if e.Agent != "" {
		out.Agent = &servedAgent{ID: e.Agent, Name: s.catalogAgentName(e.Agent), Version: e.AgentVersion, Session: e.AgentSession}
	}
	for _, way := range []struct {
		dir  string
		ends []history.LinkEnd
	}{{"out", links.Out}, {"in", links.In}} {
		for _, l := range way.ends {
			sl := servedLink{Rel: l.Rel, Dir: way.dir, Ref: l.Ref, Label: anchorLabel(l.Ref)}
			kind, key, title := "", l.Ref, ""
			if l.Entry != nil {
				sl.Key, sl.Class, sl.Outcome, sl.Label = l.Entry.Key, l.Entry.Class, l.Entry.Outcome, l.Entry.Title
				kind, key, title = l.Entry.Kind, l.Entry.Key, l.Entry.Title
			}
			sl.URL = historyURL(kind, key, l.Ref, 0, title, av.listed)
			if strings.HasPrefix(l.Ref, "launch:") {
				sl.URL = av.launchURL(l.Ref)
			}
			out.Links = append(out.Links, sl)
		}
	}
	out.Availability = av.of(e, full)
	return out
}

// launchURL is a launch's page: its run's, its ticket's before it has one
// (refURL's is the API's).
func launchURL(e history.Entry) string {
	if e.Run != "" {
		return "/runs/" + url.PathEscape(e.Run)
	}
	return ticketURL(e.Ticket)
}

// launchURL is the page of the launch a link names: the launch's entry, as
// the index holds it, read once for the reply; "" when it holds none.
func (av *availability) launchURL(ref string) string {
	if u, ok := av.launches[ref]; ok {
		return u
	}
	u := ""
	if av.ix != nil {
		if h, err := av.ix.Entry(av.ctx, ref); err == nil && h.Kind == "launch" {
			u = launchURL(h.Entry)
		}
	}
	av.launches[ref] = u
	return u
}

// catalogAgentName is a catalog agent's name, its id when the catalog no longer
// holds it.
func (s *Server) catalogAgentName(id string) string {
	if id == "" {
		return ""
	}
	if a, ok := s.Catalog().Get(id); ok && a.Name != "" {
		return a.Name
	}
	return id
}

// storeOfKind is the store that holds an entry's evidence.
func storeOfKind(kind string) string {
	switch {
	case kind == "session" || strings.HasPrefix(kind, "session."):
		return "journal"
	case kind == "brief.item":
		return "brakeman"
	case slices.Contains([]string{"ticket", "comment", "plan", "launch", "delivery.verified"}, kind),
		strings.HasPrefix(kind, "plan."), strings.HasPrefix(kind, "ticket."):
		return "dispatch"
	}
	return "ledger"
}

// kindLabels are the words a row says what it is in.
var kindLabels = map[string]string{
	"session.typed": "typed into the session", "session.question": "question", "session.answer": "answer",
	"session.report": "agent's report", "session.file": "file touched", "session.chat": "session chat",
	"run": "run", "task": "task", "attempt": "attempt", "attempt.result": "submitted result", "attempt.checkpoint": "checkpoint",
	"attempt.blocker": "blocker", "review.verdict": "review verdict", "review.finding": "review finding", "message": "message",
	"command": "command receipt", "check": "check receipt", "verify": "verify receipt", "step": "failed step",
	"candidate.verified": "verified candidate", "ticket": "ticket", "comment": "comment", "plan": "plan revision",
	"plan.approved": "plan approval", "plan.withdrawn": "plan withdrawal", "plan.superseded": "plan superseded",
	"ticket.accepted": "acceptance", "ticket.unaccepted": "acceptance withdrawn", "ticket.closed": "ticket closed",
	"launch": "launch", "delivery.verified": "verified delivery", "brief.item": "Brakeman's brief",
}

// labelOfKind is a row's source in words: "Claude Code session" for a
// session's record.
func labelOfKind(kind, agent string) string {
	if kind == "session" {
		if agent != "" {
			return agent + " session"
		}
		return "session"
	}
	if l, ok := kindLabels[kind]; ok {
		return l
	}
	return kind
}

// anchorLabel is a ref in a few words: "build · todo-3f2a91c0" for a
// task, "CND-12" for a ticket.
func anchorLabel(ref string) string {
	kind, rest, _ := strings.Cut(ref, ":")
	parts := strings.Split(rest, "/")
	switch kind {
	case "task":
		if len(parts) == 2 {
			return parts[1] + " · " + parts[0]
		}
	case "attempt", "candidate":
		if len(parts) == 3 {
			return parts[1] + " #" + parts[2]
		}
	case "check":
		if len(parts) == 5 {
			return parts[3] + " on " + parts[1] + " #" + parts[2]
		}
	case "session":
		return "session " + rest
	case "ticket", "run", "launch", "brief":
		return rest
	case "plan":
		if len(parts) == 2 {
			return parts[1] + " of " + parts[0]
		}
	}
	return ref
}

// ---- where an entry's evidence opens

// historyURL is the page that shows an entry's evidence, from its kind,
// key, ref, place in its session and title (kind "" for a ref alone, a
// link's end or an anchor): for every ref Brakeman's refURL also links,
// the same page, but for four whose refURL is not a page that shows that
// evidence (brakeman_refs.go): a check receipt opens the run page at its
// step; a command receipt the run page at the receipt, by its actor; a
// comment its ticket's page at the comment; a launch its run's page (its
// ticket's before it has one). A session opens its page while the server
// lists it and its record's page after. No URL is an API route.
func historyURL(kind, key, ref string, seq int64, title string, listed func(string) bool) string {
	esc := url.PathEscape
	if run, rest, ok := strings.Cut(key, "#command/"); ok && strings.HasPrefix(run, "run:") {
		actor, id, _ := strings.Cut(rest, "/")
		return "/runs/" + esc(strings.TrimPrefix(run, "run:")) + "?command=" + url.QueryEscape(id) + "&actor=" + url.QueryEscape(actor)
	}
	r, err := brakeman.ParseRef(ref)
	if err != nil {
		return ""
	}
	switch r.Kind {
	case brakeman.RefCheck:
		phase := ledger.PhaseCheck
		switch {
		case kind == "verify":
			phase = ledger.PhaseVerify
		case kind == "step":
			phase, _, _ = strings.Cut(title, " ")
		}
		return "/runs/" + esc(r.Run) + "?view=tasks&task=" + url.QueryEscape(r.Task) + "&attempt=" + strconv.Itoa(r.N) +
			"&step=" + url.QueryEscape(phase+"/"+r.Check+"/"+strconv.Itoa(r.K))
	case brakeman.RefCommand:
		return "/runs/" + esc(r.Run) + "?command=" + url.QueryEscape(r.Command)
	case brakeman.RefComment:
		u := ticketURL(r.Ticket)
		if seq > 0 && u != "" {
			u += "?comment=" + strconv.FormatInt(seq, 10)
		}
		return u
	case brakeman.RefLaunch:
		return ""
	case brakeman.RefSession, brakeman.RefQuestion:
		if listed != nil && listed(r.Session) {
			return "/sessions/" + esc(r.Session)
		}
		u := "/history/sessions/" + esc(r.Session)
		if seq > 0 {
			u += "#e" + strconv.FormatInt(seq, 10)
		}
		return u
	}
	return refURL(r)
}
