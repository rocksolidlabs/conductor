package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
)

// A comparison's Brakeman draft (docs/plans/compare-crew.md, PR-5): when
// a comparison's block asks for one (Compare.Brakeman), its run's
// compare.settled event, once committed, queues one Brakeman request of
// kind compare about the run, under an op id made of the run and the
// event's sequence, so a second wake, or a restart, finds the request
// and makes no second. It is read off the engine's call (OnRunChange
// only notes the run), from the ledger, so only a committed event is
// ever seen. An event committed before this server started asks nothing:
// a request lost in a crash is asked again by hand (Ask Brakeman to
// compare), which the selection never waits for.

// compareOpPrefix begins every op id the drafts make; a person's ask may
// not take it.
const compareOpPrefix = "cmp-"

// eventCompareSettled is the run event a comparison's lanes settling
// writes (internal/crew, compare_step.go).
const eventCompareSettled = "compare.settled"

// The drafts' bounds: runs noted and not yet read, runs whose facts are
// kept, chains whose newest request is kept.
const (
	maxDraftPending = 1024
	maxDraftRuns    = 1024
	maxDraftChains  = 4096
	// draftEvents is a page of a run's events as the drafts read them.
	draftEvents = 500
)

// compareOpID is the op id of the request a compare.settled event of the
// run at seq queues: cmp- and the first 24 hex digits of the SHA-256 of
// the run's id, a NUL and the sequence in decimal (28 characters, within
// brakeman's opIDPattern whatever the run's id).
func compareOpID(runID string, seq int64) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + strconv.FormatInt(seq, 10)))
	return compareOpPrefix + hex.EncodeToString(sum[:])[:24]
}

// compareDrafts is the server's: what it noted of runs and of the
// comparisons' requests. Its zero value, with since set, is ready.
type compareDrafts struct {
	// since is when the server started: an event before it asks nothing.
	since time.Time

	mu       sync.Mutex
	pending  map[string]bool
	draining bool
	runs     map[string]*draftRun
	// newest is the newest compare request about each run, by the run's
	// id, as this server made or found them (asked, scanNewest); scanned
	// says the store's newest page was read for them once.
	newest  map[string]string
	scanned bool

	// madeSeam, when set, is called with each request the drafts kept
	// (tests).
	madeSeam func(brakeman.Request)
}

// draftRun is what the drafts know of a run: whether it is a comparison
// whose block asks Brakeman for a draft, and the last event read of it.
type draftRun struct {
	ask   bool
	after int64
}

// runChanged notes a run that changed: the engine's OnRunChange, under
// its lock, so nothing here reads or waits; the notes are read by one
// drain at a time, off the call.
func (d *compareDrafts) runChanged(s *Server, runID string) {
	if s.ledger == nil || s.brakeman == nil {
		return
	}
	d.mu.Lock()
	if d.pending == nil {
		d.pending = map[string]bool{}
	}
	if len(d.pending) < maxDraftPending {
		d.pending[runID] = true
	}
	start := !d.draining
	d.draining = true
	d.mu.Unlock()
	if start {
		s.track(func() { d.drain(s) })
	}
}

// drain reads each run noted until none is left.
func (d *compareDrafts) drain(s *Server) {
	for {
		d.mu.Lock()
		runs := d.pending
		d.pending = nil
		if len(runs) == 0 {
			d.draining = false
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		for id := range runs {
			d.read(s, id)
		}
	}
}

// read reads the run's events since the last read and queues a request
// for each compare.settled among them, once the run is known to ask for
// one. A run the ledger does not hold yet is read again at its next wake.
func (d *compareDrafts) read(s *Server, runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.mu.Lock()
	dr := d.runs[runID]
	d.mu.Unlock()
	if dr == nil {
		row, ok, err := s.ledger.Run(ctx, runID)
		if err != nil || !ok {
			return
		}
		dr = &draftRun{ask: blockAsksBrakeman(row.Crew)}
		d.mu.Lock()
		if d.runs == nil || len(d.runs) >= maxDraftRuns {
			d.runs = map[string]*draftRun{}
		}
		if cur := d.runs[runID]; cur != nil {
			dr = cur
		} else {
			d.runs[runID] = dr
		}
		d.mu.Unlock()
	}
	if !dr.ask {
		return
	}
	// Only drain reads a run's events, one run at a time: after is the
	// drain's alone.
	for {
		events, err := s.ledger.Events(ctx, runID, dr.after, draftEvents)
		if err != nil {
			s.log.Warn("a comparison's events could not be read for its Brakeman draft", "run", runID, "err", err)
			return
		}
		for _, ev := range events {
			dr.after = ev.Seq
			if ev.Kind == eventCompareSettled && !ev.At.Before(d.since) {
				d.queue(ctx, s, runID, ev.Seq)
			}
		}
		if len(events) < draftEvents {
			return
		}
	}
}

// blockAsksBrakeman reports whether the crew as launched (JSON) is a
// comparison whose block asks Brakeman for a draft.
func blockAsksBrakeman(launched string) bool {
	var c struct {
		Compare *struct {
			Brakeman bool `json:"brakeman"`
		} `json:"compare"`
	}
	return json.Unmarshal([]byte(launched), &c) == nil && c.Compare != nil && c.Compare.Brakeman
}

// queue asks Brakeman for the comparison's draft, as the event at seq of
// the run settled it: nothing with Brakeman off or not seeing runs; past
// the day's budget it is queued, and its start records it over_budget
// (admitAsk, spare). Its by is the name the run's launch was made by.
func (d *compareDrafts) queue(ctx context.Context, s *Server, runID string, seq int64) {
	desk := s.brakeman
	cfg, err := desk.st.Config()
	if err != nil {
		s.log.Warn("Brakeman's configuration could not be read for a comparison's draft", "run", runID, "err", err)
		return
	}
	if !cfg.Visibility.Runs {
		s.log.Info("a comparison's Brakeman draft is not asked: Brakeman does not see runs", "run", runID)
		return
	}
	in := brakeman.Request{OpID: compareOpID(runID, seq), Kind: brakeman.KindCompare,
		Subjects: []brakeman.Ref{{Kind: brakeman.RefRun, Run: runID}}, By: s.launchBy(ctx, runID)}
	a := s.admitAsk(desk, in, true)
	switch {
	case errors.Is(a.err, brakeman.ErrPruned):
		// Asked once and pruned since: never again.
	case a.err != nil:
		s.log.Warn("a comparison's Brakeman draft could not be asked", "run", runID, "err", a.err)
	case a.code != "":
		s.log.Info("a comparison's Brakeman draft is not asked", "run", runID, "why", a.code)
	default:
		if f := d.madeSeam; f != nil {
			f(a.req)
		}
	}
}

// launchBy is the display name the run's launch was made by, as its
// package records it, cut to a request's bound; "" when it cannot be
// read.
func (s *Server) launchBy(ctx context.Context, runID string) string {
	row, ok, err := s.ledger.Run(ctx, runID)
	if err != nil || !ok || row.Package == "" {
		return ""
	}
	var pkg struct {
		Launch crew.PackageLaunch `json:"launch"`
	}
	if json.Unmarshal([]byte(row.Package), &pkg) != nil {
		return ""
	}
	return cutName(pkg.Launch.By, brakeman.MaxBy)
}

// cutName is a display name on one line, at most n bytes, cut where a
// character ends.
func cutName(name string, n int) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, name)
	for len(name) > n {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return strings.TrimSpace(name)
}

// asked notes a compare request just kept (admitAsk): the newest about
// its run.
func (d *compareDrafts) asked(req brakeman.Request) {
	if len(req.Subjects) != 1 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.newest == nil || len(d.newest) >= maxDraftChains {
		d.newest, d.scanned = map[string]string{}, false
	}
	d.newest[req.Subjects[0].Run] = newerRequest(d.newest[req.Subjects[0].Run], req.ID)
}

// newerRequest is the newer of two request ids ("" for none).
func newerRequest(a, b string) string {
	if requestN(b) > requestN(a) {
		return b
	}
	return a
}

// requestN is a request id's number, 0 for none.
func requestN(id string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "br-"), 10, 64)
	if err != nil || !strings.HasPrefix(id, "br-") {
		return 0
	}
	return n
}

// newestAbout is the newest compare request about any of runs: read
// from what this server made or found, after one read of the store's
// newest page for the requests made before it started. ok false for
// none.
func (d *compareDrafts) newestAbout(desk *brakemanDesk, runs []string) (brakeman.Request, bool, error) {
	d.mu.Lock()
	scanned := d.scanned
	d.mu.Unlock()
	if !scanned {
		page, _, err := desk.st.List("", brakeman.MaxPage)
		if err != nil {
			return brakeman.Request{}, false, err
		}
		d.mu.Lock()
		if d.newest == nil || len(d.newest) >= maxDraftChains {
			d.newest = map[string]string{}
		}
		for _, r := range page {
			if r.Kind == brakeman.KindCompare && len(r.Subjects) == 1 {
				d.newest[r.Subjects[0].Run] = newerRequest(d.newest[r.Subjects[0].Run], r.ID)
			}
		}
		d.scanned = true
		d.mu.Unlock()
	}
	d.mu.Lock()
	id := ""
	for _, run := range runs {
		id = newerRequest(id, d.newest[run])
	}
	d.mu.Unlock()
	if id == "" {
		return brakeman.Request{}, false, nil
	}
	r, err := desk.st.Get(id)
	if errors.Is(err, brakeman.ErrNotFound) {
		return brakeman.Request{}, false, nil
	}
	return r, err == nil, err
}

// compareAskRefusal holds a compare request's subject to a run of a
// comparison, as the ledger holds it: an *Invalid naming the subject for
// any other run, nil for any other kind or a subject the request's own
// check refuses (brakeman's checkAsked).
func (s *Server) compareAskRefusal(ctx context.Context, in brakeman.Request) error {
	if in.Kind != brakeman.KindCompare || len(in.Subjects) != 1 || in.Subjects[0].Kind != brakeman.RefRun {
		return nil
	}
	run := in.Subjects[0].Run
	refuse := &brakeman.Invalid{Code: brakeman.CodeInvalidRequest, Field: "subjects[0]", Why: fmt.Sprintf("run:%s is not a comparison's run: a compare request names a run of a comparison", run)}
	if s.ledger == nil || !crew.ValidRunID(run) {
		return refuse
	}
	row, ok, err := s.ledger.Run(ctx, run)
	if err != nil {
		return fmt.Errorf("the ledger's run %s: %w", run, err)
	}
	if !ok || !rowIsComparison(row) {
		return refuse
	}
	return nil
}

// compareShape is what the brief answering the compare request req is
// held to: the candidates' task ids of its run's comparison in the
// block's order, read from the crew as launched; the runs of its chain,
// the only runs its refs may name; and every operation of
// the guide a proposal may not name in it (laneCommands). A run the
// ledger does not hold, or not a comparison's, gives no columns, which
// refuses every brief.
func (s *Server) compareShape(ctx context.Context, req brakeman.Request) (brakeman.CompareShape, error) {
	shape := brakeman.CompareShape{Commands: laneCommands()}
	if len(req.Subjects) != 1 || s.ledger == nil {
		return shape, nil
	}
	row, ok, err := s.ledger.Run(ctx, req.Subjects[0].Run)
	if err != nil {
		return shape, fmt.Errorf("the ledger's run %s: %w", req.Subjects[0].Run, err)
	}
	var c crew.Crew
	if !ok || json.Unmarshal([]byte(row.Crew), &c) != nil || c.Compare == nil {
		return shape, nil
	}
	for _, cand := range c.Compare.Candidates {
		shape.Columns = append(shape.Columns, cand.Task)
	}
	if shape.Runs, err = s.ledgerChain(ctx, req.Subjects[0].Run); err != nil {
		return brakeman.CompareShape{Commands: shape.Commands}, err
	}
	return shape, nil
}

// laneCommands is every operation of the guide that commands a task, a
// review or a decision: what a compare brief may not propose, since a
// lane and the selection are the person's.
func laneCommands() []string {
	var out []string
	for _, op := range Operations() {
		if op.Method != http.MethodPost {
			continue
		}
		if strings.HasPrefix(op.Path, "/api/runs/{run}/tasks/") || strings.HasPrefix(op.Path, "/api/decisions/") ||
			strings.HasSuffix(op.Path, "/reviews") || strings.HasPrefix(op.Path, "/api/dispatch/reviews/") {
			out = append(out, op.Name)
		}
	}
	return out
}
