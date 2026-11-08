package api

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/history"
)

// The indexer's part over Dispatch and Brakeman (docs/plans/history.md,
// "The indexer" and "Writes another process makes", PR-4). A ticket is a
// source (ticket:<KEY-n>) marked by its changes' last seq, read first:
// every change of a ticket, its comments', its plan revisions' and its
// launches' among them, is a row of Dispatch's changes log naming it, so
// a change after the mark is past it and leaves the ticket to be read
// again. Dispatch's hook names a changed ticket by its id, which the hook
// may not read the store to turn into a key, so it only asks for the
// change feed (Store.ChangesAfter), which names tickets by their keys and
// is read on the indexer's goroutine from the position kept in the index
// (feedSource). The same read is the reconcile's: Dispatch takes no owner
// lock, and another server's edit reaches no hook of this one's. A brief
// (brief:<br-n>) is marked by its request's brief time and op id; the
// reconcile looks at the briefs' files by size and modification time.

// briefIDPattern is a Brakeman request's id.
var briefIDPattern = regexp.MustCompile(`^br-[1-9][0-9]{0,17}$`)

// briefDocID is the request a file of Brakeman's requests' folder is of
// (br-<n>.json, br-<n>-brief.json, br-<n>-refs.json, ...): false for
// any other path.
func briefDocID(p string) (string, bool) {
	name, ok := docID(p, brakeman.Dir+"/"+brakeman.RequestsDir+"/")
	if !ok {
		return "", false
	}
	if briefIDPattern.MatchString(name) {
		return name, true
	}
	if i := strings.IndexByte(name[min(len(name), 3):], '-'); i >= 0 {
		if id := name[:3+i]; briefIDPattern.MatchString(id) {
			return id, true
		}
	}
	return "", false
}

// indexedSource says a source key is one the stores hold (a catch-up
// compares and removes it): a session, a run, a ticket or a brief.
func indexedSource(k string) bool {
	for _, p := range []string{"session:", "run:", "ticket:", "brief:", "decision:"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// brakemanStore is Brakeman's store, nil when this process has none.
func (h *historyIndexer) brakemanStore() *brakeman.Store {
	if h.brakeman == nil {
		return nil
	}
	return h.brakeman()
}

// dispatchSources lists the tickets Dispatch holds, deleted ones among
// them, and the requests of Brakeman's that keep a brief; head is
// Dispatch's feed position read before the tickets were listed (nil with
// no Dispatch).
func (h *historyIndexer) dispatchSources(ctx context.Context) (tickets, briefs []string, head *int64, err error) {
	if ds := h.dispatch; ds != nil {
		n, err := ds.Head(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		head = &n
		for cursor := ""; ; {
			page, err := ds.List(ctx, dispatch.Query{Filter: dispatch.Filter{Deleted: true}, Limit: dispatch.MaxListLimit, Cursor: cursor})
			if err != nil {
				return nil, nil, nil, err
			}
			for _, t := range page.Tickets {
				tickets = append(tickets, hTicketRef(t.Key))
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	if bs := h.brakemanStore(); bs != nil {
		// The reconcile's first look, taken before the requests are
		// listed: a brief kept after it changes its files past it, so the
		// next reconcile reads what this listing could not.
		h.lookAtBriefs(false)
		for after := ""; ; {
			page, next, err := bs.List(after, brakeman.MaxPage)
			if err != nil {
				return nil, nil, nil, err
			}
			for _, r := range page {
				if r.HasBrief {
					briefs = append(briefs, hBriefRef(r.ID))
				}
			}
			if next == "" {
				break
			}
			after = next
		}
		if h.briefsListed != nil {
			h.briefsListed()
		}
	}
	return tickets, briefs, head, nil
}

// ticketFields are the fields of a ticket's changes its source reads: the
// acts it records (ticketEntries), nothing else of the history.
var ticketFields = map[string]bool{"approved_at": true, "approved_note": true, "withdrawn_at": true, "state": true,
	"accepted_at": true, "accepted_note": true, "status": true}

// indexTicket reads a ticket: its mark Q<seq> (its changes' last, read
// first), then the ticket, its comments, its plan revisions and its
// launches, each through Dispatch's own read. A ticket Dispatch no longer
// holds is removed.
func (h *historyIndexer) indexTicket(ctx context.Context, ix *history.Index, key string) error {
	ds := h.dispatch
	if ds == nil {
		return nil
	}
	src := hTicketRef(key)
	var r ticketRead
	var last int64
	for after := int64(0); ; {
		page, err := ds.History(ctx, key, after, dispatch.MaxHistoryPage)
		if errors.Is(err, dispatch.ErrNotFound) {
			return ix.Remove(ctx, src)
		}
		if err != nil {
			return err
		}
		for _, c := range page.Changes {
			last = max(last, c.Seq)
			if (c.Entity == dispatch.EntityTicket || c.Entity == dispatch.EntityPlan) && ticketFields[c.Field] {
				r.changes = append(r.changes, c)
			}
		}
		if page.Next == 0 {
			break
		}
		after = page.Next
	}
	mark := "Q" + strconv.FormatInt(last, 10)
	if old, ok, err := ix.Mark(ctx, src); err != nil {
		return err
	} else if ok && old == mark {
		return nil
	}
	h.afterMark(src)
	var err error
	if r.v, err = ds.Ticket(ctx, key); errors.Is(err, dispatch.ErrNotFound) {
		return ix.Remove(ctx, src)
	} else if err != nil {
		return err
	}
	for after := 0; ; {
		page, err := ds.Comments(ctx, key, after, dispatch.MaxCommentPage)
		if err != nil {
			return err
		}
		r.comments = append(r.comments, page...)
		if len(page) < dispatch.MaxCommentPage {
			break
		}
		after = page[len(page)-1].N
	}
	if r.plans, err = ds.Plans(ctx, key); err != nil {
		return err
	}
	if r.launches, err = ds.Launches(ctx, key, dispatch.MaxLaunches); err != nil {
		return err
	}
	entries, links := ticketEntries(r)
	return ix.Replace(ctx, history.Source{Key: src, Mark: mark, Generation: h.gen}, entries, links)
}

// indexBrief reads a request's kept brief: its mark B<brief time>/<op id>
// (a brief once kept never changes), then the brief. A request gone, or
// one that keeps none, is removed.
func (h *historyIndexer) indexBrief(ctx context.Context, ix *history.Index, id string) error {
	bs := h.brakemanStore()
	if bs == nil {
		return nil
	}
	src := hBriefRef(id)
	req, err := bs.Get(id)
	if gone := errors.Is(err, brakeman.ErrNotFound) || errors.Is(err, brakeman.ErrDamaged) || errors.Is(err, brakeman.ErrLaterSchema); gone || (err == nil && !req.HasBrief) {
		return ix.Remove(ctx, src)
	}
	if err != nil {
		return err
	}
	mark := "B" + req.BriefAt.UTC().Format(time.RFC3339Nano) + "/" + req.BriefOpID
	if old, ok, err := ix.Mark(ctx, src); err != nil {
		return err
	} else if ok && old == mark {
		return nil
	}
	h.afterMark(src)
	b, _, err := bs.Brief(id)
	if errors.Is(err, brakeman.ErrNotFound) {
		return ix.Remove(ctx, src)
	}
	if err != nil {
		return err
	}
	entries, links := briefEntries(req, b)
	return ix.Replace(ctx, history.Source{Key: src, Mark: mark, Generation: h.gen}, entries, links)
}

// readFeed reads Dispatch's change feed after the position the index
// keeps, marks every ticket it names dirty (a project's change reads the
// session sources again), and keeps the new position. An index that keeps
// none (never caught up over Dispatch), or a feed behind its position (a
// store restored from an older copy), asks a full catch-up instead.
func (h *historyIndexer) readFeed(ctx context.Context) {
	ix, ds := h.index(), h.dispatch
	if ix == nil || ds == nil {
		return
	}
	m, ok, err := ix.Mark(ctx, feedSource)
	if err != nil {
		h.log.Warn("the history could not read Dispatch's feed position", "err", err)
		return
	}
	pos, perr := strconv.ParseInt(m, 10, 64)
	if !ok || perr != nil {
		h.askCatchUp()
		return
	}
	start := pos
	for {
		cs, err := ds.ChangesAfter(ctx, pos, dispatch.MaxFeedPage)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Warn("Dispatch's change feed could not be read for the history", "err", err)
			}
			return
		}
		if len(cs) == 0 && pos == start {
			if head, err := ds.Head(ctx); err == nil && head < pos {
				h.askCatchUp()
				return
			}
		}
		for _, c := range cs {
			if c.Ticket != "" {
				h.mark(hTicketRef(c.Ticket))
			}
			if c.Entity == dispatch.EntityProject {
				h.mu.Lock()
				h.projects, h.placed = true, true
				h.mu.Unlock()
			}
			pos = c.Seq
		}
		if len(cs) < dispatch.MaxFeedPage {
			break
		}
	}
	if pos != start {
		if err := ix.Replace(ctx, history.Source{Key: feedSource, Mark: strconv.FormatInt(pos, 10)}, nil, nil); err != nil && ctx.Err() == nil {
			h.log.Warn("the history could not keep Dispatch's feed position", "err", err)
		}
	}
}

// askCatchUp has the goroutine compare every source with the stores.
func (h *historyIndexer) askCatchUp() {
	h.mu.Lock()
	h.overflow = true
	h.mu.Unlock()
	h.signal()
}

// reconcileBriefs looks at Brakeman's requests' files: a request whose
// request or brief file changed in size or modification time, or that is
// new or gone, marks its brief dirty (its pass reads it, or removes it).
func (h *historyIndexer) reconcileBriefs() { h.lookAtBriefs(true) }

// briefStat is a request's two files as a look saw them: the request
// (br-<n>.json) and its kept brief (br-<n>-brief.json, zero while none).
// Brakeman writes the brief's file before the request that says it keeps
// one: a look between the two sees the brief and a request that keeps
// none, and only the request's change after it says to read it again.
type briefStat struct {
	req, brief fileStat
}

// lookAtBriefs notes each request's files' sizes and modification times,
// marking dirty the briefs whose files changed when mark is set.
func (h *historyIndexer) lookAtBriefs(mark bool) {
	if h.brakemanStore() == nil {
		return
	}
	if h.briefDir == nil {
		b, err := h.data.Sub(brakeman.Dir)
		if err != nil {
			return
		}
		h.briefDir = b
	}
	docs, err := h.briefDir.ListDocs(brakeman.RequestsDir)
	if err != nil {
		return
	}
	now := map[string]briefStat{}
	for _, d := range docs {
		st := fileStat{size: d.Size, mod: d.ModTime}
		if id, ok := strings.CutSuffix(d.Name, "-brief.json"); ok && briefIDPattern.MatchString(id) {
			b := now[hBriefRef(id)]
			b.brief = st
			now[hBriefRef(id)] = b
		} else if id, ok := strings.CutSuffix(d.Name, ".json"); ok && briefIDPattern.MatchString(id) {
			b := now[hBriefRef(id)]
			b.req = st
			now[hBriefRef(id)] = b
		}
	}
	for key, st := range now {
		if old, ok := h.briefStats[key]; !ok || old != st {
			h.briefStats[key] = st
			if mark {
				h.mark(key)
			}
		}
	}
	for key := range h.briefStats {
		if _, ok := now[key]; !ok {
			delete(h.briefStats, key)
			if mark {
				h.mark(key)
			}
		}
	}
}
