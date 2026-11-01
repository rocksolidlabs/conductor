package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// A brief's evidence resolved (docs/plans/brakeman.md, Resolution; PR-3b):
// each distinct ref of a brief looked up in the record it names, checked
// against what the request may see, and kept as two snapshots, what the
// turn read of it (seen, from the request's read log) and what it is as
// the brief is kept (now). The brief route of PR-3c is the first caller;
// nothing here answers a request or writes a record.

// The states a resolution gives beside a record's own.
const (
	// refUnobserved is a seen snapshot's state for a ref the turn never
	// read: its now is read here.
	refUnobserved = "unobserved"
	// refDeleted is a record the turn read that is gone since, or a
	// Dispatch tombstone: shown so, never dropped.
	refDeleted = "deleted"
	// refUnavailable is what the server holds only in a live window (a
	// session's question or touched file) and no longer holds: the
	// session left the registry, or the window moved past it.
	refUnavailable = "unavailable"
	// A question's states as the server can tell them: shown (or set
	// aside, or held over by a held turn), answered, or gone without an
	// answer. A claim on a question (CLAIMED) lasts as long as a write and
	// is not told apart from open here.
	questionOpen      = "open"
	questionResolved  = "resolved"
	questionWithdrawn = "withdrawn"
)

// refGitWait bounds each git command a commit: ref's resolution runs.
const refGitWait = 10 * time.Second

// refGrant is what a request may see: the snapshot its turn began under
// intersected with the configuration now (a narrowing takes effect at
// once, a widening at the next request), so an item is admitted, a class
// visible and an earlier scope covered only when both say so.
type refGrant struct {
	scopes []brakeman.Scope
	vis    []brakeman.Visibility
}

// grantOf is the grant of a request begun under snap, read under the
// configuration now.
func grantOf(snap brakeman.Snapshot, now brakeman.Config) refGrant {
	return refGrant{scopes: []brakeman.Scope{snap.Scope, now.Scope}, vis: []brakeman.Visibility{snap.Visibility, now.Visibility}}
}

func (g refGrant) admits(it brakeman.ScopeItem) bool {
	for _, s := range g.scopes {
		if !s.Admits(it) {
			return false
		}
	}
	return true
}

func (g refGrant) sees(class string) bool {
	for _, v := range g.vis {
		if !v.Has(class) {
			return false
		}
	}
	return true
}

func (g refGrant) covers(earlier brakeman.Scope) bool {
	for _, s := range g.scopes {
		if !s.Covers(earlier) {
			return false
		}
	}
	return true
}

// badRef is a ref a brief cannot be kept with, and why, in words the
// agent fixes its brief by.
type badRef struct {
	Ref string `json:"ref"`
	Why string `json:"why"`
}

// badRefsError is a brief refused for its refs (PR-3c answers it 422
// bad_refs), naming each one.
type badRefsError struct {
	Refs []badRef
}

func (e *badRefsError) Error() string {
	parts := make([]string, 0, len(e.Refs))
	for _, b := range e.Refs {
		parts = append(parts, b.Ref+": "+b.Why)
	}
	return "bad_refs: " + strings.Join(parts, "; ")
}

// refClass is the visibility class a ref's kind reads: a check's receipt
// (exit, subject, log hash) is the runs' class, its log's text the logs'
// (a read, not a ref); an earlier brief carries its footprint's classes
// (resolveBrief).
func refClass(kind string) string {
	switch kind {
	case brakeman.RefSession, brakeman.RefQuestion:
		return brakeman.ClassSessions
	case brakeman.RefTicket, brakeman.RefComment, brakeman.RefOp:
		return brakeman.ClassDispatch
	case brakeman.RefFile:
		return brakeman.ClassTouched
	case brakeman.RefBrief:
		return ""
	}
	return brakeman.ClassRuns
}

// resolveBriefRefs resolves every distinct ref of b, in the order the
// brief first names them, for the request req (begun under req.Config)
// under the configuration now, with what its turn read (log): a ref that
// names nothing, lies outside the scope or the visibility, or is an
// earlier brief made under a wider grant, and a decision's or proposal's
// revision that is not its target's, refuse the brief (*badRefsError,
// every bad ref named); a store that cannot be read is an error of its
// own. desk holds the requests a brief: ref names (nil: none resolves).
func (s *Server) resolveBriefRefs(ctx context.Context, desk *brakeman.Store, req brakeman.Request, b brakeman.Brief, log brakeman.ReadLog, now brakeman.Config) ([]brakeman.Resolution, error) {
	rr := &refResolver{s: s, desk: desk, grant: grantOf(req.Config, now), at: time.Now().UTC(),
		seen: map[brakeman.Ref]brakeman.Read{}, runs: map[string]*refRun{}, tops: map[string]string{}}
	for _, rd := range log.Reads {
		// The newest read of a ref stands: the log is oldest first.
		rr.seen[rd.Ref] = rd
	}
	var (
		out  []brakeman.Resolution
		bad  []badRef
		done = map[brakeman.Ref]int{}
	)
	for _, it := range b.Items {
		for _, ref := range it.Refs() {
			if _, ok := done[ref]; ok {
				continue
			}
			res, why, err := rr.resolve(ctx, ref)
			if err != nil {
				return nil, err
			}
			done[ref] = len(out)
			if why != "" {
				bad = append(bad, badRef{Ref: ref.String(), Why: why})
				out = append(out, brakeman.Resolution{Ref: ref})
				continue
			}
			out = append(out, res)
		}
	}
	// What a decision and a proposal advise was read at one revision of
	// their target: the revision they name must be that one.
	for i, it := range b.Items {
		var target brakeman.Ref
		var rev string
		switch it.Kind {
		case brakeman.ItemDecision:
			target, rev = it.Decision.About, it.Decision.SubjectRevision
		case brakeman.ItemProposal:
			target, rev = it.Proposal.Target, it.Proposal.ExpectedRevision
		default:
			continue
		}
		j, ok := done[target]
		if rev == "" || !ok || slices.ContainsFunc(bad, func(b badRef) bool { return b.Ref == target.String() }) {
			continue
		}
		if why := revisionWhy(out[j], rev); why != "" {
			bad = append(bad, badRef{Ref: target.String(), Why: fmt.Sprintf("items[%d]: %s", i, why)})
		}
	}
	if len(bad) > 0 {
		return nil, &badRefsError{Refs: bad}
	}
	if out == nil {
		out = []brakeman.Resolution{}
	}
	return out, nil
}

// revisionWhy is why rev is not the revision a decision or proposal was
// read at: the turn's read of the target (seen), or, for a target the
// turn never read, the target as it is.
func revisionWhy(res brakeman.Resolution, rev string) string {
	if res.Seen.State != refUnobserved {
		switch {
		case res.Seen.Rev == "":
			return fmt.Sprintf("names revision %s, and what you read of %s had none", rev, res.Ref)
		case res.Seen.Rev != rev:
			return fmt.Sprintf("you read revision %s, and the item names revision %s", res.Seen.Rev, rev)
		}
		return ""
	}
	switch {
	case res.Now.Rev == "":
		return fmt.Sprintf("names revision %s, and %s has none", rev, res.Ref)
	case res.Now.Rev != rev:
		return fmt.Sprintf("%s is at revision %s, and the item names revision %s: read it again", res.Ref, res.Now.Rev, rev)
	}
	return ""
}

// refResolver resolves one brief's refs: the grant, what the turn read,
// and what one resolution has looked up already (a run, a repository's
// top), so a brief of many refs to one run reads it once.
type refResolver struct {
	s     *Server
	desk  *brakeman.Store
	grant refGrant
	at    time.Time
	seen  map[brakeman.Ref]brakeman.Read
	runs  map[string]*refRun
	tops  map[string]string
}

// refFound is what a ref names as it is now: found, or a tombstone
// (deleted), or not held any longer (unavailable); its snapshot; the
// scope item it is admitted as.
type refFound struct {
	found, deleted, unavailable bool
	now                         brakeman.RefState
	item                        brakeman.ScopeItem
}

// resolve resolves one ref: its resolution, or why it is refused.
func (rr *refResolver) resolve(ctx context.Context, ref brakeman.Ref) (brakeman.Resolution, string, error) {
	if ref.Kind == brakeman.RefBrief {
		return rr.resolveBrief(ref)
	}
	class := refClass(ref.Kind)
	if !rr.grant.sees(class) {
		return brakeman.Resolution{}, "not visible: Brakeman may not see " + class, nil
	}
	f, err := rr.lookup(ctx, ref)
	if err != nil {
		return brakeman.Resolution{}, "", err
	}
	read, observed := rr.seen[ref]
	switch {
	case !f.found && !f.deleted && !f.unavailable && !observed:
		return brakeman.Resolution{}, "resolves to nothing", nil
	case !f.found && !f.deleted && !f.unavailable:
		// Read by the turn, gone since: a record is deleted, a live
		// window's entry unavailable.
		if liveWindow(ref.Kind) {
			f.unavailable = true
		} else {
			f.deleted = true
		}
	}
	if !rr.grant.admits(f.item) {
		return brakeman.Resolution{}, "outside the request's scope", nil
	}
	u := refURL(ref)
	now := f.now
	switch {
	case f.unavailable:
		now = brakeman.RefState{State: refUnavailable}
	case f.deleted:
		now.State, now.Rev, now.SHA256 = refDeleted, "", ""
	}
	if now.URL == "" {
		now.URL = u
	}
	if now.At.IsZero() {
		now.At = rr.at
	}
	seen := brakeman.RefState{State: refUnobserved}
	if observed {
		seen = brakeman.RefState{URL: u, State: read.State, Rev: read.Rev, At: read.At}
	}
	return brakeman.Resolution{Ref: ref, Seen: fitState(seen), Now: fitState(now)}, "", nil
}

// liveWindow reports a ref kind the server holds only while it is live:
// a session, its question and its touched file.
func liveWindow(kind string) bool {
	return kind == brakeman.RefQuestion || kind == brakeman.RefFile
}

// resolveBrief resolves a brief: ref: the earlier request's kept brief,
// given only when its footprint is within the grant (every class its
// turn's reads used is visible, and the scope covers the one it ran
// under); one that is not is withheld, and a ref to it refused as outside
// the scope.
func (rr *refResolver) resolveBrief(ref brakeman.Ref) (brakeman.Resolution, string, error) {
	if rr.desk == nil {
		return brakeman.Resolution{}, "resolves to nothing", nil
	}
	req, err := rr.desk.Get(ref.Request)
	switch {
	case errors.Is(err, brakeman.ErrNotFound), errors.Is(err, brakeman.ErrDamaged), errors.Is(err, brakeman.ErrLaterSchema):
		return brakeman.Resolution{}, "resolves to nothing", nil
	case err != nil:
		return brakeman.Resolution{}, "", err
	case !req.HasBrief:
		return brakeman.Resolution{}, "resolves to nothing: " + ref.Request + " has no brief", nil
	}
	classes := brakeman.Classes
	if req.Footprint != nil {
		classes = req.Footprint.Classes
	}
	for _, c := range classes {
		// A footprint that is not the earlier request's own (none kept)
		// counts every class its snapshot had on.
		if (req.Footprint != nil || req.Config.Visibility.Has(c)) && !rr.grant.sees(c) {
			return brakeman.Resolution{}, "outside the request's scope: " + ref.Request + " was made under a wider scope or visibility", nil
		}
	}
	if !rr.grant.covers(req.Config.Scope) {
		return brakeman.Resolution{}, "outside the request's scope: " + ref.Request + " was made under a wider scope or visibility", nil
	}
	b, _, err := rr.desk.Brief(ref.Request)
	switch {
	case errors.Is(err, brakeman.ErrNotFound):
		return brakeman.Resolution{}, "resolves to nothing: " + ref.Request + "'s brief is gone", nil
	case err != nil:
		return brakeman.Resolution{}, "", err
	case ref.Item >= len(b.Items):
		return brakeman.Resolution{}, fmt.Sprintf("resolves to nothing: %s's brief has %d items", ref.Request, len(b.Items)), nil
	}
	enc, err := store.Encode(b.Items[ref.Item])
	if err != nil {
		return brakeman.Resolution{}, "", err
	}
	sum := sha256.Sum256(enc)
	now := brakeman.RefState{URL: refURL(ref), State: req.State, SHA256: hex.EncodeToString(sum[:]), At: req.BriefAt}
	seen := brakeman.RefState{State: refUnobserved}
	if read, ok := rr.seen[ref]; ok {
		seen = brakeman.RefState{URL: now.URL, State: read.State, Rev: read.Rev, At: read.At}
	}
	return brakeman.Resolution{Ref: ref, Seen: fitState(seen), Now: fitState(now)}, "", nil
}

// lookup reads what the ref names now from its record.
func (rr *refResolver) lookup(ctx context.Context, ref brakeman.Ref) (refFound, error) {
	switch ref.Kind {
	case brakeman.RefRun, brakeman.RefTask, brakeman.RefAttempt, brakeman.RefCheck, brakeman.RefCommand,
		brakeman.RefEvent, brakeman.RefMessage, brakeman.RefCommit:
		return rr.lookupRunPart(ctx, ref)
	case brakeman.RefSession, brakeman.RefQuestion, brakeman.RefFile:
		return rr.lookupSessionPart(ctx, ref)
	case brakeman.RefTicket, brakeman.RefComment, brakeman.RefOp:
		return rr.lookupDispatch(ctx, ref)
	}
	return refFound{}, nil
}

// refRun is a run as the resolver reads it: from the engine, its record
// and the ledger, whichever hold it.
type refRun struct {
	found    bool
	run      crew.Run // the engine's or the record's, when either holds it
	held     bool     // run is set
	row      ledger.RunRow
	managed  bool // the ledger holds it
	state    string
	rev      string
	crewID   string
	cwd      string
	branches []string
	commits  []string
	item     brakeman.ScopeItem
}

// run reads the run id once per resolution.
func (rr *refResolver) run(ctx context.Context, id string) (*refRun, error) {
	if r, ok := rr.runs[id]; ok {
		return r, nil
	}
	r := &refRun{}
	if run, ok := rr.s.runs.Get(id); ok {
		r.run, r.held = run, true
	} else if rr.s.records != nil {
		run, ok, err := rr.s.records.Get(id)
		if err != nil {
			return nil, fmt.Errorf("the record of %s: %w", id, err)
		}
		r.run, r.held = run, ok
	}
	if rr.s.ledger != nil {
		row, ok, err := rr.s.ledger.Run(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("the ledger's run %s: %w", id, err)
		}
		r.row, r.managed = row, ok
	}
	r.found = r.held || r.managed
	switch {
	case r.held:
		r.state, r.crewID, r.cwd = r.run.State, r.run.CrewID, r.run.Cwd
		if r.run.Managed != nil {
			r.rev = strconv.FormatInt(r.run.Managed.Revision, 10)
		}
		for _, m := range r.run.Members {
			r.branches = append(r.branches, m.Branch)
			r.commits = append(r.commits, m.Base)
		}
		for _, t := range r.run.Tasks {
			if a := t.Attempt; a != nil {
				r.branches = append(r.branches, a.Branch)
				r.commits = append(r.commits, a.Start, a.Result)
			}
		}
	case r.managed:
		r.state, r.crewID = r.row.State, r.row.CrewID
		var c struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal([]byte(r.row.Crew), &c) == nil {
			r.cwd = c.Cwd
		}
	}
	if r.managed {
		r.rev = strconv.FormatInt(r.row.Revision, 10)
		r.commits = append(r.commits, r.row.Base)
		attempts, err := rr.s.ledger.Attempts(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("the ledger's attempts of %s: %w", id, err)
		}
		for _, a := range attempts {
			r.branches = append(r.branches, a.Branch)
			r.commits = append(r.commits, a.StartCommit, a.ResultCommit)
		}
	}
	// A run read by the turn and gone since is admitted as what its id
	// says of it (its crew): under a scope of repositories or projects
	// nothing proves it in, so it is outside.
	r.item = brakeman.ScopeItem{Kind: brakeman.ItemRun, Run: id, Crew: crewOfRun(id)}
	if r.found {
		r.item.Crew = r.crewID
		r.item.Repo = rr.top(ctx, r.cwd)
		project, err := rr.runProject(ctx, id, r)
		if err != nil {
			return nil, err
		}
		r.item.Project = project
	}
	rr.runs[id] = r
	return r, nil
}

// runProject is the project of the ticket the run is linked to (Dispatch's
// live run link), else of the one it was launched from; "" for none.
func (rr *refResolver) runProject(ctx context.Context, id string, r *refRun) (string, error) {
	if ds := rr.s.dispatch; ds != nil {
		tk, ok, err := ds.RunTicket(ctx, id)
		if err != nil {
			return "", fmt.Errorf("the ticket of run %s: %w", id, err)
		}
		if ok && tk.DeletedAt.IsZero() {
			return projectOfKey(tk.Key), nil
		}
	}
	if r.held && r.run.Ticket != nil {
		return projectOfKey(r.run.Ticket.Key), nil
	}
	return "", nil
}

// top is the repository root of dir as git reports it, "" outside one (or
// for no folder), read once per folder.
func (rr *refResolver) top(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	if t, ok := rr.tops[dir]; ok {
		return t
	}
	gctx, cancel := context.WithTimeout(ctx, refGitWait)
	defer cancel()
	t, err := gitcli.TopOf(gctx, dir)
	if err != nil {
		t = ""
	}
	rr.tops[dir] = t
	return t
}

// lookupRunPart reads a run, or what is under it, from the run's records.
func (rr *refResolver) lookupRunPart(ctx context.Context, ref brakeman.Ref) (refFound, error) {
	r, err := rr.run(ctx, ref.Run)
	if err != nil {
		return refFound{}, err
	}
	f := refFound{item: r.item}
	if !r.found {
		return f, nil
	}
	ld := rr.s.ledger
	switch ref.Kind {
	case brakeman.RefRun:
		f.found, f.now = true, brakeman.RefState{State: r.state, Rev: r.rev}
	case brakeman.RefTask:
		if r.managed {
			tasks, err := ld.Tasks(ctx, ref.Run)
			if err != nil {
				return f, fmt.Errorf("the ledger's tasks of %s: %w", ref.Run, err)
			}
			if i := slices.IndexFunc(tasks, func(t ledger.TaskRow) bool { return t.ID == ref.Task }); i >= 0 {
				f.found, f.now = true, brakeman.RefState{State: tasks[i].State, Rev: r.rev}
			}
		} else if i := slices.IndexFunc(r.run.Tasks, func(t crew.TaskState) bool { return t.ID == ref.Task }); i >= 0 {
			f.found, f.now = true, brakeman.RefState{State: r.run.Tasks[i].State, Rev: r.rev}
		}
	case brakeman.RefAttempt:
		if !r.managed {
			break
		}
		attempts, err := ld.Attempts(ctx, ref.Run)
		if err != nil {
			return f, fmt.Errorf("the ledger's attempts of %s: %w", ref.Run, err)
		}
		if i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == ref.Task && a.N == ref.N }); i >= 0 {
			a := attempts[i]
			f.found, f.now = true, brakeman.RefState{State: a.State, Rev: a.ResultCommit, At: firstTime(a.ClosedAt, a.LaunchedAt, a.ReservedAt)}
		}
	case brakeman.RefCheck:
		if !r.managed {
			break
		}
		row, ok, err := ld.Check(ctx, ref.Run, ref.Task, ref.N, ref.Check, ref.K)
		if err != nil {
			return f, fmt.Errorf("the ledger's check %s: %w", ref, err)
		}
		if ok {
			f.found, f.now = true, brakeman.RefState{State: checkState(row), Rev: row.Subject, SHA256: row.LogSHA256, At: firstTime(row.FinishedAt, row.StartedAt)}
		}
	case brakeman.RefCommand:
		if !r.managed {
			break
		}
		cmds, err := ld.Commands(ctx, ref.Run)
		if err != nil {
			return f, fmt.Errorf("the ledger's commands of %s: %w", ref.Run, err)
		}
		// A command id is the client's, kept by its actor: the admin's
		// stands where two actors used one id, as its route reads it.
		var cmd *ledger.CommandRow
		for i := range cmds {
			if c := &cmds[i]; c.ID == ref.Command && (cmd == nil || c.Actor == adminActor) {
				cmd = c
			}
		}
		if cmd != nil {
			f.found, f.now = true, brakeman.RefState{State: cmd.Status, At: cmd.CreatedAt}
		}
	case brakeman.RefEvent:
		if !r.managed {
			break
		}
		events, err := ld.Events(ctx, ref.Run, ref.Seq-1, 1)
		if err != nil {
			return f, fmt.Errorf("the ledger's events of %s: %w", ref.Run, err)
		}
		if len(events) == 1 && events[0].Seq == ref.Seq {
			f.found, f.now = true, brakeman.RefState{State: events[0].Kind, At: events[0].At}
		}
	case brakeman.RefMessage:
		if !r.managed {
			break
		}
		msgs, err := ld.Messages(ctx, ref.Run)
		if err != nil {
			return f, fmt.Errorf("the ledger's messages of %s: %w", ref.Run, err)
		}
		if i := slices.IndexFunc(msgs, func(m ledger.MessageRow) bool { return m.Seq == ref.Seq }); i >= 0 {
			f.found, f.now = true, brakeman.RefState{State: messageState(msgs[i]), At: msgs[i].CreatedAt}
		}
	case brakeman.RefCommit:
		short, ok, err := rr.commitOn(ctx, r, ref.Commit)
		if err != nil {
			return f, err
		}
		if ok {
			f.found, f.now = true, brakeman.RefState{State: "present", Rev: short}
		}
	}
	return f, nil
}

// commitOn reports whether sha is a commit of the run's: its base, an
// attempt's start or result, or a commit one of its branches holds (any
// branch of its folder's repository, for a run that made none), with its
// short id. git is run through internal/gitcli, argv only.
func (rr *refResolver) commitOn(ctx context.Context, r *refRun, sha string) (string, bool, error) {
	if r.cwd == "" {
		return "", false, nil
	}
	gctx, cancel := context.WithTimeout(ctx, refGitWait)
	defer cancel()
	full, err := gitcli.Run(gctx, r.cwd, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil || strings.TrimSpace(full) != sha {
		// Not a commit of the run's repository (or no repository): it
		// names nothing there.
		return "", false, nil
	}
	on := slices.Contains(r.commits, sha)
	if !on {
		out, err := gitcli.Run(gctx, r.cwd, "branch", "--contains", sha, "--format=%(refname:short)")
		if err != nil {
			return "", false, nil
		}
		ours := slices.DeleteFunc(slices.Clone(r.branches), func(b string) bool { return b == "" })
		for line := range strings.Lines(out) {
			b := strings.TrimSpace(line)
			if b != "" && (len(ours) == 0 || slices.Contains(ours, b)) {
				on = true
				break
			}
		}
	}
	if !on {
		return "", false, nil
	}
	short, err := gitcli.Run(gctx, r.cwd, "rev-parse", "--short", sha)
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(short), true, nil
}

// checkState is a check receipt's state in at most 32 bytes: how a
// finished check ended (its exit, a timeout, a kill, a signal), else the
// row's state.
func checkState(row ledger.CheckRow) string {
	if row.State != ledger.CheckFinished {
		return row.State
	}
	switch {
	case row.TimedOut:
		return "timed_out"
	case row.Killed:
		return "killed"
	case row.Signal != "":
		return "signal:" + row.Signal
	}
	return "exit:" + strconv.Itoa(row.Exit)
}

// messageState is a members' message's delivery: acknowledged, delivered,
// being typed, or waiting.
func messageState(m ledger.MessageRow) string {
	switch {
	case !m.AckedAt.IsZero():
		return "acked"
	case !m.DeliveredAt.IsZero():
		return "delivered"
	case !m.TypingAt.IsZero():
		return "typing"
	}
	return "queued"
}

// chatter is a session driver whose chat the server keeps (a server
// session's): a question's history in its window.
type chatter interface {
	ChatHistory() []session.ChatMessage
}

// lookupSessionPart reads a session, its question or its touched file from
// what the server holds of it: the registry, the session's attention and
// its chat window (a hosted session's attention only), and its touched
// index.
func (rr *refResolver) lookupSessionPart(ctx context.Context, ref brakeman.Ref) (refFound, error) {
	f := refFound{item: brakeman.ScopeItem{Kind: brakeman.ItemSession, Session: ref.Session}}
	d, ok := rr.s.registry.Get(ref.Session)
	if !ok {
		// Gone from the registry: admitted as the run it was a member of,
		// while the engine still knows that.
		if runID, _, ok := rr.s.runs.MemberOf(ref.Session); ok {
			if err := rr.memberItem(ctx, &f.item, runID); err != nil {
				return f, err
			}
		}
		if ref.Kind == brakeman.RefSession {
			return f, nil
		}
		f.unavailable = rr.observed(ref)
		return f, nil
	}
	info := d.Info()
	if info.Kind != session.KindHosted {
		// A hosted session's folder is on its host: no repository of the
		// server's names it.
		f.item.Repo = rr.top(ctx, info.Cwd)
	}
	if info.Crew != nil {
		if err := rr.memberItem(ctx, &f.item, info.Crew.RunID); err != nil {
			return f, err
		}
	}
	switch ref.Kind {
	case brakeman.RefSession:
		state := string(info.Status)
		if info.Attention.State != session.AttentionNone {
			state += ":" + string(info.Attention.State)
		}
		f.found, f.now = true, brakeman.RefState{State: state}
	case brakeman.RefQuestion:
		state, at := questionState(d, info, ref.Question)
		switch {
		case state != "":
			f.found, f.now = true, brakeman.RefState{State: state, At: at}
		default:
			// Not shown and not in the window: the window moved past it.
			f.unavailable = rr.observed(ref)
		}
	case brakeman.RefFile:
		t, ok := d.(toucher)
		if !ok {
			break
		}
		files := t.TouchedFiles()
		if i := slices.IndexFunc(files, func(tf proto.TouchedFile) bool { return tf.Path == ref.Path }); i >= 0 {
			at, _ := time.Parse(time.RFC3339Nano, files[i].Last)
			f.found, f.now = true, brakeman.RefState{State: files[i].Op, Rev: strconv.Itoa(files[i].Count), At: at}
		} else {
			f.unavailable = rr.observed(ref)
		}
	}
	return f, nil
}

// observed reports whether the turn read the ref.
func (rr *refResolver) observed(ref brakeman.Ref) bool {
	_, ok := rr.seen[ref]
	return ok
}

// memberItem fills a member session's scope item from its run's.
func (rr *refResolver) memberItem(ctx context.Context, it *brakeman.ScopeItem, runID string) error {
	r, err := rr.run(ctx, runID)
	if err != nil {
		return err
	}
	it.Run, it.Crew, it.Project = runID, r.item.Crew, r.item.Project
	return nil
}

// questionState is the state of the session's question id: open while it
// shows or is kept (set aside, held over), else answered or withdrawn by
// its chat window's lines; "" when neither holds it (a hosted session's
// chat is its host's).
func questionState(d session.Driver, info session.Info, id string) (string, time.Time) {
	if a := info.Attention; a.ID == id && a.State == session.AttentionNeedsInput {
		var at time.Time
		if a.Since != nil {
			at = *a.Since
		}
		return questionOpen, at
	}
	if slices.Contains(info.Kept, id) {
		return questionOpen, time.Time{}
	}
	c, ok := d.(chatter)
	if !ok {
		return "", time.Time{}
	}
	state, at := "", time.Time{}
	for _, m := range c.ChatHistory() {
		switch {
		case m.ID == id && m.Kind == proto.ChatKindQuestion:
			state, at = questionWithdrawn, m.At
		case state != "" && m.Ref == id && m.Event == "answered":
			return questionResolved, m.At
		case state != "" && m.Ref == id && m.Event == "withdrawn":
			state, at = questionWithdrawn, m.At
		}
	}
	return state, at
}

// lookupDispatch reads a ticket, a comment or a receipt from Dispatch's
// store; a tombstone is deleted, never dropped.
func (rr *refResolver) lookupDispatch(ctx context.Context, ref brakeman.Ref) (refFound, error) {
	ds := rr.s.dispatch
	f := refFound{item: brakeman.ScopeItem{Kind: brakeman.ItemTicket}}
	key := ref.Ticket
	if ref.Kind == brakeman.RefOp {
		if ds == nil {
			return f, nil
		}
		rc, ok, err := ds.Receipt(ctx, ref.Actor, ref.Op)
		if err != nil {
			return f, fmt.Errorf("the receipt %s: %w", ref, err)
		}
		key = rc.Key
		if key == "" {
			if key, _, err = ds.OpTicket(ctx, ref.Actor, ref.Op); err != nil {
				return f, fmt.Errorf("the ticket of %s: %w", ref, err)
			}
		}
		switch {
		case ok:
			rev := ""
			if rc.Rev > 0 {
				rev = strconv.FormatInt(rc.Rev, 10)
			}
			f.found, f.now = true, brakeman.RefState{State: rc.Status, Rev: rev}
		case key != "":
			// The receipt is pruned; the change log still names its
			// ticket.
			f.found, f.now = true, brakeman.RefState{State: "pruned"}
		}
		if key != "" {
			f.now.URL = ticketURL(key, "")
		} else if ref.Actor == adminActor {
			f.now.URL = "/api/dispatch/ops/" + url.PathEscape(ref.Op)
		}
		f.item.Project = projectOfKey(key)
		if key != "" {
			links, err := rr.ticketLinks(ctx, key)
			if err != nil {
				return f, err
			}
			f.item.Links = links
		}
		return f, nil
	}
	f.item.Project = projectOfKey(key)
	if ds == nil {
		return f, nil
	}
	switch ref.Kind {
	case brakeman.RefTicket:
		tk, err := ds.Ticket(ctx, key)
		switch {
		case errors.Is(err, dispatch.ErrNotFound):
			return f, nil
		case err != nil:
			return f, fmt.Errorf("the ticket %s: %w", key, err)
		}
		f.now = brakeman.RefState{State: tk.Status, Rev: strconv.FormatInt(tk.Rev, 10), At: tk.UpdatedAt}
		f.found, f.deleted = tk.DeletedAt.IsZero(), !tk.DeletedAt.IsZero()
	case brakeman.RefComment:
		c, err := ds.Comment(ctx, key, ref.Comment)
		switch {
		case errors.Is(err, dispatch.ErrNotFound):
			return f, nil
		case err != nil:
			return f, fmt.Errorf("the comment %s: %w", ref, err)
		}
		state := "posted"
		if !c.EditedAt.IsZero() {
			state = "edited"
		}
		f.now = brakeman.RefState{State: state, Rev: strconv.FormatInt(c.Rev, 10), At: firstTime(c.EditedAt, c.CreatedAt)}
		f.found, f.deleted = c.DeletedAt.IsZero(), !c.DeletedAt.IsZero()
	}
	links, err := rr.ticketLinks(ctx, key)
	if err != nil {
		return f, err
	}
	f.item.Links = links
	return f, nil
}

// ticketLinks is the runs a ticket's live run links name, each with its
// crew (the run's id says it); none for a ticket Dispatch does not hold.
func (rr *refResolver) ticketLinks(ctx context.Context, key string) ([]brakeman.RunLink, error) {
	links, err := rr.s.dispatch.Links(ctx, key)
	switch {
	case errors.Is(err, dispatch.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("the links of %s: %w", key, err)
	}
	var out []brakeman.RunLink
	for _, l := range links {
		if l.Kind == dispatch.LinkRun && l.DeletedAt.IsZero() {
			out = append(out, brakeman.RunLink{Run: l.Ref, Crew: crewOfRun(l.Ref)})
		}
	}
	return out, nil
}

// crewOfRun is the crew a run's id names: the id less its dash and eight
// hex digits.
func crewOfRun(id string) string {
	if !crew.ValidRunID(id) {
		return ""
	}
	return id[:strings.LastIndexByte(id, '-')]
}

// projectOfKey is the project of a ticket's key, "" for none.
func projectOfKey(key string) string {
	p, _, ok := dispatch.ParseKey(key)
	if !ok {
		return ""
	}
	return p
}

// refURL is where a person follows the ref: its page in the app, or the
// route that serves it.
func refURL(ref brakeman.Ref) string {
	esc := url.PathEscape
	run := "/runs/" + esc(ref.Run)
	switch ref.Kind {
	case brakeman.RefRun, brakeman.RefEvent, brakeman.RefMessage, brakeman.RefCommit:
		return run
	case brakeman.RefTask:
		return run + "?task=" + url.QueryEscape(ref.Task)
	case brakeman.RefAttempt:
		return run + "?task=" + url.QueryEscape(ref.Task) + "&attempt=" + strconv.Itoa(ref.N)
	case brakeman.RefCheck:
		return "/api/runs/" + esc(ref.Run) + "/tasks/" + esc(ref.Task) + "/attempts/" + strconv.Itoa(ref.N) + "/steps/" + esc(ref.Check) + "/" + strconv.Itoa(ref.K) + "/log"
	case brakeman.RefCommand:
		return "/api/runs/" + esc(ref.Run) + "/commands/" + esc(ref.Command)
	case brakeman.RefSession, brakeman.RefQuestion:
		return "/sessions/" + esc(ref.Session)
	case brakeman.RefFile:
		u := "/sessions/" + esc(ref.Session) + "?tab=files&open=" + url.QueryEscape(ref.Path)
		if len(u) > brakeman.MaxURL {
			return "/sessions/" + esc(ref.Session) + "?tab=files"
		}
		return u
	case brakeman.RefTicket:
		return ticketURL(ref.Ticket, "")
	case brakeman.RefComment:
		return ticketURL(ref.Ticket, ref.Comment)
	case brakeman.RefBrief:
		return "/brakeman?request=" + url.QueryEscape(ref.Request) + "#item-" + strconv.Itoa(ref.Item)
	}
	return ""
}

// ticketURL is a ticket's page, at the comment id when one is given.
func ticketURL(key, comment string) string {
	p, n, ok := dispatch.ParseKey(key)
	if !ok {
		return ""
	}
	u := "/dispatch/" + url.PathEscape(p) + "/" + strconv.FormatInt(n, 10)
	if comment != "" {
		u += "#comment-" + url.PathEscape(comment)
	}
	return u
}

// firstTime is the first of ts that is not zero.
func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}

// fitState holds a snapshot to the bounds of br-<n>-refs.json (url 1 KiB,
// state 32 bytes, rev 64, sha256 64 hex digits): a field past its bound is
// cut where a character ends (a URL is dropped: a cut one leads nowhere),
// a hash that is not one dropped.
func fitState(st brakeman.RefState) brakeman.RefState {
	if len(st.URL) > brakeman.MaxURL {
		st.URL = ""
	}
	st.State = cutBytes(st.State, brakeman.MaxRefState)
	st.Rev = cutBytes(st.Rev, brakeman.MaxRevision)
	if len(st.SHA256) != 64 || strings.Trim(st.SHA256, "0123456789abcdef") != "" {
		st.SHA256 = ""
	}
	if !st.At.IsZero() {
		st.At = st.At.UTC()
	}
	return st
}

// cutBytes cuts s to at most n bytes where a character ends.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
