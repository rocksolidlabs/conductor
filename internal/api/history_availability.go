package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/brakeman"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/history"
	"github.com/rocksolidlabs/conductor/internal/journal"
	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Availability (docs/plans/history.md, "Availability", PR-4): whether
// what an entry names can still be opened, computed when a page is
// answered from what the server holds now, never stored. A page's is
// cheap: the registry, the stores' own reads (the ledger's rows without
// their logs, read once an attempt for the page; a session's record; a
// brief's request) and, for an attempt's worktree, a look through its
// run's folder's root (openCwd); no git. Nothing is looked at by a path
// of its own: a path a row names is never opened again. /api/history/entry
// adds the rest: whether a session can be resumed (by the resume routes'
// own rules, resumeCheck), whether a result commit is in its repository,
// the worktree's branch, and a log's size and hash.

// The availability states. A row whose session record the journal no
// longer holds is never served (forgottenSources), so no row is ever said
// to be forgotten.
const (
	availLive         = "live"          // a session listed and running
	availListed       = "listed"        // ended, still listed: its page shows its last screen
	availRecord       = "record"        // only its record
	availLost         = "lost"          // only its record, which the server stopped observing while it ran
	availHostAway     = "host_away"     // a hosted session whose host is not connected now
	availWorktree     = "worktree"      // an attempt's worktree is there
	availWorktreeGone = "worktree_gone" // it is not, or it is not where its run puts it
	availElsewhere    = "elsewhere"     // an attempt that ran in another Runtime: not looked at from here
	availLog          = "log"           // a receipt whose log the ledger holds
	availLogDropped   = "log_dropped"   // the run's bound dropped its log: its hash and tail kept
	availPruned       = "pruned"        // the run's rows are gone, or the brief's request
	availDeleted      = "deleted"       // a ticket's or a comment's tombstone
	availKept         = "kept"          // the store holds it
	availUnknown      = "unknown"       // the store could not be read now: neither there nor gone
)

// worktreeGone is an attempt's worktree not there, said alike whatever
// the cause (removed, a link put in its place, a path its run does not
// make), so nothing of what is at that path is learned from it.
var worktreeGone = availabilityOf{State: availWorktreeGone, Words: "worktree not kept"}

// availabilityOf is an entry's availability as served: its state and its
// words; for a session's record entry on /api/history/entry, whether it
// can be resumed; and what else the entry route reads.
type availabilityOf struct {
	State  string        `json:"state"`
	Words  string        `json:"words"`
	Resume *resumeOffer  `json:"resume,omitempty"`
	Commit *commitInRepo `json:"commit,omitempty"`
	Branch string        `json:"branch,omitempty"`
	Log    *logKept      `json:"log,omitempty"`
}

// resumeOffer is whether Resume is offered, and why not or what it asks;
// Confirm says it needs the person's second yes (a lost record); URL is
// where the resume is instead (a crew member's run); Unknown says a check
// could not be made now (the run records could not be read), so Resume is
// not offered and the words say to try again: not a no.
type resumeOffer struct {
	Offered bool   `json:"offered"`
	Why     string `json:"why,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
	URL     string `json:"url,omitempty"`
	Unknown bool   `json:"unknown,omitempty"`
}

type commitInRepo struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
}

type logKept struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// availability answers availability for one reply, reading each store's
// rows once: a run's attempts and receipts, a run's folder, a session's
// record, a brief's request. A read that failed is kept as failed, said
// unknown, never as gone.
type availability struct {
	s        *Server
	ctx      context.Context
	ix       *history.Index
	attempts map[string]attemptsRead
	checks   map[attemptID]checksRead
	folders  map[string]*runFolder
	kept     map[string]bool
	briefs   map[string]availabilityOf
	launches map[string]string
}

type attemptsRead struct {
	rows []ledger.AttemptRow
	err  error
}

type checksRead struct {
	rows []ledger.CheckRow
	err  error
}

// runFolder is a run's folder as this server opens it: the run's folder
// as recorded (cwd), opened through its root by openCwd (f), which checks
// it by resolveCwd, at real; err when it cannot be.
type runFolder struct {
	cwd, real string
	f         *session.Folder
	err       error
}

func (s *Server) newAvailability(ctx context.Context, ix *history.Index) *availability {
	return &availability{s: s, ctx: ctx, ix: ix, attempts: map[string]attemptsRead{}, checks: map[attemptID]checksRead{},
		folders: map[string]*runFolder{}, kept: map[string]bool{}, briefs: map[string]availabilityOf{},
		launches: map[string]string{}}
}

// close closes the run folders the reply opened.
func (av *availability) close() {
	for _, rf := range av.folders {
		if rf.f != nil {
			rf.f.Close()
		}
	}
}

// listed says the server lists the session now.
func (av *availability) listed(id string) bool {
	_, ok := av.s.registry.Get(id)
	return ok
}

// recordOf is the session whose record an entry is of: its source's
// (session:<id>), else its anchor's; "" for an entry of no session record.
func recordOf(e history.Entry) string {
	if id, ok := strings.CutPrefix(e.Source, "session:"); ok {
		return id
	}
	if id, ok := strings.CutPrefix(e.Anchor, "session:"); ok && (e.Kind == "session" || strings.HasPrefix(e.Kind, "session.")) {
		return id
	}
	return ""
}

// refRecord is the session a ref names (session:<id>, question:<id>/<q>
// and an entry's key session:<id>#...); "" for any other.
func refRecord(ref string) string {
	kind, rest, _ := strings.Cut(ref, ":")
	switch kind {
	case "session", "question":
		id, _, _ := strings.Cut(rest, "/")
		id, _, _ = strings.Cut(id, "#")
		return id
	}
	return ""
}

// recordKept says the journal still answers for the session's record: its
// own read finds it (Get), or finds a file it cannot decode, which no
// Forget has removed. Its "not found", or no journal, or a read that
// failed, is not kept: what is not known to be kept is not served. Read
// once a session for the reply.
func (av *availability) recordKept(id string) bool {
	if k, ok := av.kept[id]; ok {
		return k
	}
	k := false
	if j := av.s.sessionRecords; j != nil {
		_, _, err := j.Get(id)
		var pe *store.ParseError
		k = err == nil || errors.As(err, &pe)
	}
	av.kept[id] = k
	return k
}

// forgottenSources is the sources of the hits whose session record the
// journal no longer holds, each once: they are left out of the reply, and
// the indexer is asked to read each again (its pass removes it).
func (av *availability) forgottenSources(hits []history.Hit) []string {
	var out []string
	for _, h := range hits {
		id := recordOf(h.Entry)
		if id == "" || av.recordKept(id) {
			continue
		}
		if src := hSessionRef(id); !slices.Contains(out, src) {
			out = append(out, src)
			if av.s.history != nil {
				av.s.history.mark(src)
			}
		}
	}
	return out
}

// withoutForgotten is the hits whose session record, if any, the journal
// still holds.
func (av *availability) withoutForgotten(hits []history.Hit) []history.Hit {
	gone := av.forgottenSources(hits)
	if len(gone) == 0 {
		return hits
	}
	return slices.DeleteFunc(slices.Clone(hits), func(h history.Hit) bool { return slices.Contains(gone, hSessionRef(recordOf(h.Entry))) })
}

// linkKept says a link's other end may be served: it names no session
// record the journal no longer holds, by its ref or its entry's key.
func (av *availability) linkKept(l history.LinkEnd) bool {
	if id := refRecord(l.Ref); id != "" && !av.recordKept(id) {
		return false
	}
	if l.Entry != nil {
		if id := refRecord(l.Entry.Key); id != "" && !av.recordKept(id) {
			return false
		}
	}
	return true
}

// of is an entry's availability; full adds what the entry route reads.
func (av *availability) of(e history.Entry, full bool) availabilityOf {
	r, err := brakeman.ParseRef(e.Ref)
	kind := e.Kind
	switch {
	case kind == "session" || strings.HasPrefix(kind, "session."):
		id := strings.TrimPrefix(e.Anchor, "session:")
		out := av.session(id, kind == "session", e)
		if full && kind == "session" {
			out.Resume = av.s.resumeOfRecord(id)
		}
		return out
	case kind == "attempt" && err == nil:
		return av.attempt(r, e, full)
	case (kind == "check" || kind == "verify" || kind == "step") && err == nil:
		return av.check(r, full)
	case kind == "ticket" || kind == "comment":
		if e.Outcome == availDeleted {
			return availabilityOf{State: availDeleted, Words: "deleted"}
		}
		return availabilityOf{State: availKept, Words: "in Dispatch"}
	case kind == "brief.item" && err == nil:
		return av.brief(r.Request)
	}
	return availabilityOf{State: availKept, Words: "kept"}
}

// session is a session's availability: listed (running or ended), else
// its record's (kept, lost while it ran, its host away). A row whose
// record the journal no longer holds is not served, so it is never asked.
func (av *availability) session(id string, record bool, e history.Entry) availabilityOf {
	if d, ok := av.s.registry.Get(id); ok {
		if !d.Info().Status.Ended() {
			return availabilityOf{State: availLive, Words: "live"}
		}
		return availabilityOf{State: availListed, Words: "ended, listed"}
	}
	switch {
	case record && e.Outcome == journal.EndingLost:
		return availabilityOf{State: availLost, Words: "record only: the server stopped while it ran"}
	case record && (e.Outcome == journal.EndingHostAway || (e.Hosted && e.Outcome == "running")):
		return availabilityOf{State: availHostAway, Words: "host away"}
	}
	return availabilityOf{State: availRecord, Words: "record only"}
}

// brief is a brief's: kept while Brakeman's store still holds its request
// with its brief, by the store's own read (no path of its own), pruned
// once it holds neither, unknown when the read failed.
func (av *availability) brief(id string) availabilityOf {
	if out, ok := av.briefs[id]; ok {
		return out
	}
	out := availabilityOf{State: availUnknown, Words: "Brakeman's store is not open here"}
	if av.s.brakeman != nil && av.s.brakeman.st != nil {
		req, err := av.s.brakeman.st.Get(id)
		switch {
		case errors.Is(err, brakeman.ErrNotFound) || (err == nil && !req.HasBrief):
			out = availabilityOf{State: availPruned, Words: "pruned: Brakeman no longer keeps its brief"}
		case err == nil:
			out = availabilityOf{State: availKept, Words: "kept by Brakeman"}
		default:
			out = availabilityOf{State: availUnknown, Words: "Brakeman's store could not be read; try again"}
		}
	}
	av.briefs[id] = out
	return out
}

// attemptRows is a run's attempts, read once for the reply.
func (av *availability) attemptRows(run string) ([]ledger.AttemptRow, error) {
	if a, ok := av.attempts[run]; ok {
		return a.rows, a.err
	}
	a := attemptsRead{err: errNoLedger}
	if ld := av.s.ledger; ld != nil {
		a.rows, a.err = ld.Attempts(av.ctx, run)
	}
	av.attempts[run] = a
	return a.rows, a.err
}

// errNoLedger is a read of the ledger in a process that has none open.
var errNoLedger = errors.New("the ledger is not open")

// runFolder is the run's folder, opened once for the reply: the folder its
// record names (else its crew's as the ledger keeps it), through
// resolveCwd and openCwd, as a launch opens its folder.
func (av *availability) runFolder(run string) *runFolder {
	if rf, ok := av.folders[run]; ok {
		return rf
	}
	rf := &runFolder{}
	av.folders[run] = rf
	if av.s.records != nil {
		if rec, ok, err := av.s.records.Get(run); err == nil && ok {
			rf.cwd = rec.Cwd
		}
	}
	if rf.cwd == "" && av.s.ledger != nil {
		if row, ok, err := av.s.ledger.Run(av.ctx, run); err == nil && ok {
			var c crew.Crew
			if json.Unmarshal([]byte(row.Crew), &c) == nil {
				rf.cwd = c.Cwd
			}
		}
	}
	if rf.cwd == "" {
		rf.err = errors.New("the run's folder is not known")
		return rf
	}
	if rf.f, rf.err = av.s.openCwd(rf.cwd); rf.err == nil {
		rf.real = rf.f.Path()
	}
	return rf
}

// worktreeKept says an attempt's worktree is where its run puts it
// (<run's folder>/.conductor/worktrees/<run>/<task>.<n>, crew's
// worktreesDir) and there: each step of that path a directory, never a
// link, looked at through the run's folder's root. A worktree its row
// names anywhere else is not looked at.
func (av *availability) worktreeKept(row ledger.AttemptRow) bool {
	rf := av.runFolder(row.RunID)
	if rf.err != nil {
		return false
	}
	name := fmt.Sprintf("%s.%d", row.TaskID, row.N)
	steps := []string{".conductor", "worktrees", row.RunID, name}
	if row.Worktree != filepath.Join(append([]string{rf.cwd}, steps...)...) {
		return false
	}
	for i := range steps {
		fi, err := rf.f.Lstat(filepath.Join(steps[:i+1]...))
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// attempt is an attempt's: its worktree there or not, looked at through
// its run's folder, when it ran natively; one that ran in another Runtime
// is said as its row has it and not looked at from here. Its branch is
// its row's; whether its result commit is in the repository is asked of
// git in the run's folder as resolveCwd checks it.
func (av *availability) attempt(r brakeman.Ref, e history.Entry, full bool) availabilityOf {
	if e.Where != "" && e.Where != string(ext.WhereNative) {
		return availabilityOf{State: availElsewhere, Words: "on " + e.Where + ": not looked at from here"}
	}
	rows, err := av.attemptRows(r.Run)
	if err != nil {
		return availabilityOf{State: availUnknown, Words: "the run's rows could not be read; try again"}
	}
	i := slices.IndexFunc(rows, func(a ledger.AttemptRow) bool { return a.TaskID == r.Task && a.N == r.N })
	if i < 0 {
		return availabilityOf{State: availPruned, Words: "pruned: the run's rows are gone"}
	}
	row := rows[i]
	out := worktreeGone
	if av.worktreeKept(row) {
		out = availabilityOf{State: availWorktree, Words: "worktree kept"}
		if full {
			out.Branch = row.Branch
		}
	}
	if full && objectID(row.ResultCommit) {
		if rf := av.runFolder(r.Run); rf.err == nil {
			ctx, cancel := context.WithTimeout(av.ctx, revisionTimeout)
			defer cancel()
			_, err := gitcli.Run(ctx, rf.real, "cat-file", "-e", row.ResultCommit+"^{commit}")
			out.Commit = &commitInRepo{ID: row.ResultCommit, Present: err == nil}
		}
	}
	return out
}

// check is a receipt's: its log held, dropped by the run's bound, or the
// run's rows gone; unknown when they could not be read.
func (av *availability) check(r brakeman.Ref, full bool) availabilityOf {
	ld := av.s.ledger
	if ld == nil {
		return availabilityOf{State: availUnknown, Words: "the ledger is not open"}
	}
	key := attemptID{r.Run, r.Task, r.N}
	read, ok := av.checks[key]
	if !ok {
		read.rows, read.err = ld.Checks(av.ctx, r.Run, r.Task, r.N, false)
		av.checks[key] = read
	}
	if read.err != nil {
		return availabilityOf{State: availUnknown, Words: "the run's rows could not be read; try again"}
	}
	for _, c := range read.rows {
		if c.ID != r.Check || c.K != r.K {
			continue
		}
		if c.LogDropped {
			return availabilityOf{State: availLogDropped, Words: "log dropped: its hash and tail kept"}
		}
		out := availabilityOf{State: availLog, Words: "log kept"}
		if full {
			if row, ok, err := ld.Check(av.ctx, r.Run, r.Task, r.N, r.Check, r.K); err == nil && ok {
				out.Log = &logKept{Size: int64(len(row.Log)), SHA256: row.LogSHA256}
				out.Words = fmt.Sprintf("log kept (%d bytes)", len(row.Log))
			}
		}
		return out
	}
	return availabilityOf{State: availPruned, Words: "pruned: the run's rows are gone"}
}

// resumeOfRecord is whether a session can be resumed from its record, by
// the resume routes' rules (resumeCheck) and the record's own: never a
// hosted session (its machine resumes it), a crew member (its run does),
// a managed attempt (its run continues) or a Brakeman turn; never a
// launch whose arguments were too long to keep; only in a Runtime this
// server still has; a lost record (its process's state unknown) only
// with the person's second yes.
func (s *Server) resumeOfRecord(id string) *resumeOffer {
	if s.sessionRecords == nil {
		return &resumeOffer{Why: "this server keeps no history"}
	}
	rec, _, err := s.sessionRecords.Get(id)
	if err != nil {
		return &resumeOffer{Why: "its record is no longer kept"}
	}
	return s.resumeOffered(rec)
}

// resumeOffered is resumeOfRecord's answer for the record rec: offered
// (with Confirm for a lost record), not offered with why (and URL where
// the resume is instead), or Unknown when whether it is a crew member
// could not be read.
func (s *Server) resumeOffered(rec journal.Record) *resumeOffer {
	id := rec.ID
	no := func(why string) *resumeOffer { return &resumeOffer{Why: why} }
	if rec.Host.Hosted {
		return no(fmt.Sprintf("resume it on %s: `conductor host` resumes it there", cmpOr(rec.Host.Name, "its host")))
	}
	run, member, err := s.memberOf(rec)
	if err != nil {
		return &resumeOffer{Unknown: true, Why: "whether it was a crew member's session could not be read; try again"}
	}
	if member {
		switch {
		case rec.Crew != nil && rec.Crew.Brakeman != "":
			return no("a Brakeman turn is never resumed: ask Brakeman again")
		case rec.Crew != nil && rec.Crew.Task != "":
			return &resumeOffer{Why: "a managed run continues from its run page", URL: "/runs/" + run}
		}
		return &resumeOffer{Why: "resume it as the run's member, from its run", URL: "/runs/" + run}
	}
	if rec.Launch == nil || !rec.Launch.ArgsKept {
		return no("its arguments were too long to keep")
	}
	if rec.Where != "" {
		if _, ok := s.runtimeAt(ext.Where(rec.Where)); !ok {
			return no(fmt.Sprintf("its Runtime %s is not on this server", rec.Where))
		}
	}
	if d, ok := s.registry.Get(id); ok && !d.Info().Status.Ended() {
		return no("the session is still running")
	}
	var as *session.AgentSession
	if rec.Agent.Session != nil {
		as = &session.AgentSession{ID: rec.Agent.Session.ID, Source: rec.Agent.Session.Source, Resumable: rec.Agent.Session.Resumable}
	}
	plan, aerr := s.resumeCheck(rec.Agent.ID, as, rec.Cwd)
	if aerr != nil {
		return no(aerr.Message)
	}
	if plan.id == "" {
		if plan.agent.Session.Empty() {
			return no(plan.agent.Name + " has no session recipe: nothing of its conversation can be resumed")
		}
		return no("nothing to resume yet: the agent had no turn")
	}
	if rec.Ending == journal.EndingLost || (!rec.LostAt.IsZero() && rec.EndedAt.IsZero()) {
		return &resumeOffer{Offered: true, Confirm: true, Why: fmt.Sprintf("the server stopped while this session ran on %s; its process may still be running", cmpOr(rec.Where, string(ext.WhereNative)))}
	}
	return &resumeOffer{Offered: true}
}

// memberOf is the run a session is a member of: by its record's crew, by
// the engine's runs, else by the run records, a member's state naming the
// session (a record whose adoption the journal had not written when the
// server stopped), read through crew.Records. err when the run records
// could not all be read: whether it is a member is then not known.
func (s *Server) memberOf(rec journal.Record) (string, bool, error) {
	if rec.Crew != nil && rec.Crew.RunID != "" {
		return rec.Crew.RunID, true, nil
	}
	if s.runs != nil {
		if run, _, ok := s.runs.MemberOf(rec.ID); ok {
			return run, true, nil
		}
	}
	if s.records == nil {
		return "", false, errors.New("this server keeps no run records")
	}
	run, _, err := s.records.MemberOf(rec.ID)
	if err != nil {
		return "", false, err
	}
	return run, run != "", nil
}
