package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
)

// A ticket's runs (Program item DS2, docs/plans/dispatch-evidence.md,
// PR-8, "A ticket's runs"): every launch the ticket made, every run DS1
// linked to it by hand, and every other run of a chain the ticket holds
// (the runs a launched or linked run continues and that continue it), in
// pages, newest first. Evidence is read at each request from the engine,
// the run records and the ledger, never copied into Dispatch: the order
// is computed from the launches, the links and the runs' continuation
// marks, which are small, and only a page's entries are summarized.

const (
	// MaxTicketRunsReply bounds a page of a ticket's runs: ten entries,
	// each a launch with its summary (its why at 2,000 characters, its
	// agents and label at their bounds), the run's summary (twelve
	// members, twenty blockers with their reasons cut to 200 characters)
	// and what changed since, every text at its escaped worst, encoded
	// whole, is under it (dispatch_evidence_test.go). Exported for the CLI.
	MaxTicketRunsReply = 1 << 20
	// ticketRunsPage is a page's entries.
	ticketRunsPage = 10
	// maxSummaryMembers bounds a run summary's members: a crew's bound.
	maxSummaryMembers = 12
	// maxSummaryBlockers bounds a run summary's blockers, and
	// maxSummaryReason a blocker's reason there, in characters (the run's
	// own page has it whole).
	maxSummaryBlockers = 20
	maxSummaryReason   = 200
	// maxChainRuns bounds a walk of a run's chain: a hang's guard, far
	// past any chain a person continues by hand, never a cap a page meets.
	maxChainRuns = 10000
	// maxContestedKeys bounds the other holders a contested entry names.
	maxContestedKeys = 20
	// chainWalkTimeout bounds a walk the store asks for inside one of its
	// operations (SetRunChain), which holds the store's lock.
	chainWalkTimeout = 10 * time.Second
)

// The kinds of a ticket's run entries, which order them among entries of
// one time and id.
const (
	entryLaunch = "launch"
	entryLink   = "link"
	entryRun    = "run"
)

// runSummaryMember is a member of a run as a ticket's runs show it: its
// name, its session (the page joins it to the live sessions) and status.
type runSummaryMember struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId,omitempty"`
	Status    string `json:"status"`
}

// runSummaryBlocker is what an attempt in flight says stops it.
type runSummaryBlocker struct {
	Task   string    `json:"task"`
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// runSummary is a run as a ticket's runs show it, from the engine, else
// its record (record true), else, for a run the ledger alone has (its
// record pruned), the ledger's row and its tasks' rows (record true).
// Tasks counts the run's tasks by state; Questions is how many of its
// members' sessions waited on a person when the reply was made.
type runSummary struct {
	ID          string              `json:"id"`
	Label       string              `json:"label"`
	State       string              `json:"state"`
	Outcome     string              `json:"outcome,omitempty"`
	Paused      bool                `json:"paused"`
	StartedAt   time.Time           `json:"startedAt"`
	StoppedAt   *time.Time          `json:"stoppedAt,omitempty"`
	Record      bool                `json:"record"`
	ResumedFrom string              `json:"resumedFrom,omitempty"`
	ResumedBy   string              `json:"resumedBy,omitempty"`
	Ticket      *crew.RunTicket     `json:"ticket,omitempty"`
	Members     []runSummaryMember  `json:"members"`
	Tasks       map[string]int      `json:"tasks"`
	Questions   int                 `json:"questions"`
	Blockers    []runSummaryBlocker `json:"blockers"`
}

// cutRunes is s cut to n characters, with an ellipsis when it was longer.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// summarizeRun is r as a ticket's runs show it.
func summarizeRun(r crew.Run, record bool) runSummary {
	out := runSummary{ID: r.ID, Label: r.Label, State: r.State, Outcome: r.Outcome, Paused: r.Paused, StartedAt: r.StartedAt, StoppedAt: r.StoppedAt,
		Record: record, ResumedFrom: r.ResumedFrom, ResumedBy: r.ResumedBy, Ticket: r.Ticket,
		Members: []runSummaryMember{}, Tasks: map[string]int{}, Questions: r.NeedsInput, Blockers: []runSummaryBlocker{}}
	for _, m := range r.Members {
		if len(out.Members) == maxSummaryMembers {
			break
		}
		out.Members = append(out.Members, runSummaryMember{Name: m.Name, SessionID: m.SessionID, Status: m.Status})
	}
	for _, t := range r.Tasks {
		out.Tasks[t.State]++
		if t.Blocker != nil && len(out.Blockers) < maxSummaryBlockers {
			out.Blockers = append(out.Blockers, runSummaryBlocker{Task: t.ID, Reason: cutRunes(t.Blocker.Reason, maxSummaryReason), By: t.Blocker.By, At: t.Blocker.At})
		}
	}
	return out
}

// runSummaryOf is the run with the id summarized, from the engine, else
// its record, else the ledger's rows; nil for a run found nowhere.
func (s *Server) runSummaryOf(ctx context.Context, id string) (*runSummary, error) {
	if r, ok := s.runs.Get(id); ok {
		sum := summarizeRun(r, false)
		return &sum, nil
	}
	if s.records != nil {
		r, ok, err := s.records.Get(id)
		if err != nil {
			s.log.Warn("a run's record could not be read for a ticket's runs", "run", id, "err", err)
		}
		if ok {
			sum := summarizeRun(r, true)
			return &sum, nil
		}
	}
	if s.ledger == nil {
		return nil, nil
	}
	p, ok, err := s.ledger.Pointer(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	tasks, err := s.ledger.Tasks(ctx, id)
	if err != nil {
		return nil, err
	}
	sum := runSummary{ID: p.ID, State: p.State, Outcome: p.Outcome, Paused: p.Paused, StartedAt: p.CreatedAt, Record: true,
		ResumedFrom: p.ContinuedFrom, ResumedBy: p.ContinuedBy, Members: []runSummaryMember{}, Tasks: map[string]int{}, Blockers: []runSummaryBlocker{}}
	if !p.ClosedAt.IsZero() {
		closed := p.ClosedAt
		sum.StoppedAt = &closed
	}
	for _, t := range tasks {
		sum.Tasks[t.State]++
	}
	return &sum, nil
}

// chainNode is a run of a chain: the run it continues (from), the run
// that continues it (by), and when it started.
type chainNode struct {
	id, from, by string
	at           time.Time
}

// runNode reads the run's place in its chain: the ledger's row of it
// (continued_from, continued_by), else the engine's run, else its record
// (resumedFrom, resumedBy); ok false for a run found nowhere.
func (s *Server) runNode(ctx context.Context, id string) (chainNode, bool, error) {
	if !crew.ValidRunID(id) {
		return chainNode{}, false, nil
	}
	if s.ledger != nil {
		p, ok, err := s.ledger.Pointer(ctx, id)
		if err != nil && !errors.Is(err, ledger.ErrUnavailable) && !errors.Is(err, ledger.ErrClosed) {
			return chainNode{}, false, err
		}
		if ok {
			return chainNode{id: id, from: p.ContinuedFrom, by: p.ContinuedBy, at: p.CreatedAt}, true, nil
		}
	}
	if r, ok := s.runs.Get(id); ok {
		return chainNode{id: id, from: r.ResumedFrom, by: r.ResumedBy, at: r.StartedAt}, true, nil
	}
	if s.records != nil {
		r, ok, err := s.records.Get(id)
		if err != nil {
			s.log.Warn("a run's record could not be read for its chain", "run", id, "err", err)
		}
		if ok {
			return chainNode{id: id, from: r.ResumedFrom, by: r.ResumedBy, at: r.StartedAt}, true, nil
		}
	}
	return chainNode{}, false, nil
}

// runChain is the chain of the run with the id: the run, every run it
// continues and every run that continues it, found by walking the marks
// both ways (runNode), oldest first by start, the anchor among them;
// before says which of them come before the anchor (the runs it
// continues, directly or not). Empty for a run found nowhere.
func (s *Server) runChain(ctx context.Context, anchor string) (nodes []chainNode, before map[string]bool, err error) {
	before = map[string]bool{}
	seen := map[string]bool{}
	byID := map[string]chainNode{}
	queue := []string{anchor}
	for len(queue) > 0 && len(seen) < maxChainRuns {
		id := queue[0]
		queue = queue[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		n, ok, err := s.runNode(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		byID[id] = n
		queue = append(queue, n.from, n.by)
	}
	if _, ok := byID[anchor]; !ok {
		return nil, before, nil
	}
	// The runs before the anchor: its from, theirs, and so on.
	for id := byID[anchor].from; id != "" && !before[id]; id = byID[id].from {
		if _, ok := byID[id]; !ok {
			break
		}
		before[id] = true
	}
	for _, n := range byID {
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, func(a, b chainNode) int {
		return cmp.Or(a.at.Compare(b.at), strings.Compare(a.id, b.id))
	})
	return nodes, before, nil
}

// chainRuns is the store's sight of a run's chain (SetRunChain): the runs
// of the run's chain, read from the ledger, the engine and the records,
// never from the store, inside whose operation it runs.
func (s *Server) chainRuns(run string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), chainWalkTimeout)
	defer cancel()
	nodes, _, err := s.runChain(ctx, run)
	if err != nil {
		s.log.Warn("a run's chain could not be read", "run", run, "err", err)
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.id)
	}
	return out
}

// contested is a hand link's chain held by other tickets too: their keys
// and the words saying how, with the remedy.
type contested struct {
	Keys  []string `json:"keys"`
	Words string   `json:"words"`
}

// ticketRunEntry is one entry of a ticket's runs: a launch (launch, its
// run once it has one, and what changed since), a run linked by hand
// (link, run, and contested when its chain is), or another run of a chain
// the ticket holds (run, the launch or the link that holds it, and the
// run it continues, absent for a run before the linked one).
type ticketRunEntry struct {
	Kind      string                  `json:"kind"`
	Launch    *dispatch.LaunchSummary `json:"launch,omitempty"`
	Link      *dispatch.Link          `json:"link,omitempty"`
	Run       *runSummary             `json:"run,omitempty"`
	Changed   *dispatch.LaunchChanges `json:"changed,omitempty"`
	Contested *contested              `json:"contested,omitempty"`
	Continues string                  `json:"continues,omitempty"`

	// The entry's place, and the run it summarizes ("" for a launch with
	// no run yet), set before the page is chosen.
	at    time.Time
	id    string
	runID string
}

// entryKey orders entries: newest first by time, then by id, then by kind.
func entryKey(a, b ticketRunEntry) int {
	return cmp.Or(b.at.Compare(a.at), strings.Compare(b.id, a.id), strings.Compare(b.Kind, a.Kind))
}

// runsCursor is a page's place: the last entry's time, kind and id, as
// the opaque next encodes it.
type runsCursor struct {
	at       time.Time
	kind, id string
}

// String encodes the cursor.
func (c runsCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.at.UnixNano(), 10) + "." + c.kind + "." + c.id))
}

// parseRunsCursor reads a cursor next gave.
func parseRunsCursor(s string) (runsCursor, error) {
	bad := errors.New("before is a cursor a page of the ticket's runs gave as next")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(s) > 200 {
		return runsCursor{}, bad
	}
	parts := strings.SplitN(string(b), ".", 3)
	if len(parts) != 3 || !slices.Contains([]string{entryLaunch, entryLink, entryRun}, parts[1]) || parts[2] == "" {
		return runsCursor{}, bad
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return runsCursor{}, bad
	}
	return runsCursor{at: time.Unix(0, ns).UTC(), kind: parts[1], id: parts[2]}, nil
}

// ticketRunEntries is every entry of the ticket's runs, newest first,
// none summarized: its launches, its live run links, and the other runs
// of the chains they hold, each run once. A link whose chain other live
// tickets hold too is contested: its run alone is an entry, marked with
// the other holders' keys, and the chain's other runs are entries of no
// ticket.
func (s *Server) ticketRunEntries(ctx context.Context, ds *dispatch.Store, key string, launches []dispatch.LaunchSummary, links []dispatch.Link) ([]ticketRunEntry, error) {
	var out []ticketRunEntry
	seen := map[string]bool{}
	type anchor struct {
		run    string
		launch *dispatch.LaunchSummary
		link   *dispatch.Link
	}
	var anchors []anchor
	for i := range launches {
		l := &launches[i]
		e := ticketRunEntry{Kind: entryLaunch, Launch: l, at: l.CreatedAt, id: l.ID}
		if l.State == dispatch.LaunchStarted {
			e.runID = l.RunID
			seen[l.RunID] = true
			anchors = append(anchors, anchor{run: l.RunID, launch: l})
		}
		out = append(out, e)
	}
	for i := range links {
		l := &links[i]
		if l.Kind != dispatch.LinkRun || seen[l.Ref] {
			continue
		}
		seen[l.Ref] = true
		e := ticketRunEntry{Kind: entryLink, Link: l, at: l.CreatedAt, id: l.ID, runID: l.Ref}
		holders, err := ds.ChainHolders(ctx, l.Ref)
		if err != nil {
			return nil, err
		}
		var others []dispatch.ChainHolder
		for _, h := range holders {
			if h.Key != key {
				others = append(others, h)
			}
		}
		if len(others) > 0 {
			e.Contested = s.contestedWords(ctx, l.Ref, others)
		} else {
			anchors = append(anchors, anchor{run: l.Ref, link: l})
		}
		out = append(out, e)
	}
	for _, a := range anchors {
		nodes, before, err := s.runChain(ctx, a.run)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if seen[n.id] {
				continue
			}
			seen[n.id] = true
			e := ticketRunEntry{Kind: entryRun, Launch: a.launch, Link: a.link, at: n.at, id: n.id, runID: n.id}
			if !before[n.id] {
				e.Continues = n.from
			}
			out = append(out, e)
		}
	}
	slices.SortFunc(out, entryKey)
	return out, nil
}

// contestedWords is a contested entry of the run linked by hand: the
// other holders' keys (at most maxContestedKeys) and the words, how the
// first one's run stands to it and the remedy.
func (s *Server) contestedWords(ctx context.Context, run string, others []dispatch.ChainHolder) *contested {
	c := &contested{Keys: []string{}}
	for _, h := range others {
		if len(c.Keys) == maxContestedKeys {
			break
		}
		c.Keys = append(c.Keys, h.Key)
	}
	h := others[0]
	_, before, err := s.runChain(ctx, run)
	switch {
	case err != nil || h.Run == run:
		c.Words = fmt.Sprintf("run %s is linked to %s too: remove one of the links", run, h.Key)
	case before[h.Run]:
		c.Words = fmt.Sprintf("run %s continues run %s, which %s links: remove one of the links", run, h.Run, h.Key)
	default:
		c.Words = fmt.Sprintf("run %s, which %s links, continues run %s: remove one of the links", h.Run, h.Key, run)
	}
	return c
}

// handleDispatchTicketRuns answers a page of the ticket's runs: 200
// {entries, next?}, at most ticketRunsPage entries, newest first, each a
// launch, a run linked by hand or another run of a chain the ticket
// holds, next the cursor of the page after it (?before=next) when there
// is one, so every launch, run and continuation is on some page; under
// MaxTicketRunsReply. 404 not_found for no such ticket; 400
// invalid_request for a cursor next did not give, or any other parameter.
func (s *Server) handleDispatchTicketRuns(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	q, err := readQuery(r, "before")
	var raw string
	if err == nil {
		raw, err = q.one("before")
	}
	var cur *runsCursor
	if err == nil && raw != "" {
		var c runsCursor
		c, err = parseRunsCursor(raw)
		cur = &c
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	launches, err := ds.Launches(ctx, key, dispatch.MaxLaunches)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	links, err := ds.Links(ctx, key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	all, err := s.ticketRunEntries(ctx, ds, key, launches, links)
	if err != nil {
		s.ticketRunsError(w, err)
		return
	}
	from := 0
	if cur != nil {
		mark := ticketRunEntry{Kind: cur.kind, at: cur.at, id: cur.id}
		from, _ = slices.BinarySearchFunc(all, mark, entryKey)
		if from < len(all) && entryKey(all[from], mark) == 0 {
			from++
		}
	}
	page := all[from:min(from+ticketRunsPage, len(all))]
	for i := range page {
		e := &page[i]
		if e.runID != "" {
			if e.Run, err = s.runSummaryOf(ctx, e.runID); err != nil {
				s.ticketRunsError(w, err)
				return
			}
		}
		if e.Kind == entryLaunch {
			changed, err := ds.ChangedSince(ctx, e.Launch.ID)
			switch {
			case errors.Is(err, dispatch.ErrNotFound):
			case err != nil:
				s.dispatchError(w, "launch", err)
				return
			default:
				e.Changed = &changed
			}
		}
	}
	reply := map[string]any{"entries": page}
	if from+len(page) < len(all) {
		last := page[len(page)-1]
		reply["next"] = runsCursor{at: last.at, kind: last.Kind, id: last.id}.String()
	}
	writeDispatch(w, http.StatusOK, reply)
}

// ticketRunsError answers a ledger or Dispatch read of a ticket's runs
// that failed.
func (s *Server) ticketRunsError(w http.ResponseWriter, err error) {
	if errors.Is(err, ledger.ErrUnavailable) || errors.Is(err, ledger.ErrClosed) {
		writeError(w, http.StatusServiceUnavailable, "ledger_unavailable", err.Error())
		return
	}
	s.dispatchError(w, "ticket's runs", err)
}
