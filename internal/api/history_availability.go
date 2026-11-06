package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

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

// answer is an availability or an eligibility answer: yes, no, or
// unknown when what would tell could not be read now. Every answer the
// history's routes give of whether something is there or may be done is
// one, made by answerOf, so that no failed read is ever said as no.
type answer string

const (
	answerYes     answer = "yes"
	answerNo      answer = "no"
	answerUnknown answer = "unknown"
)

// answerOf is the answer of a look that found the thing (found) or failed
// with err. A failure that says the thing is not there (absent) is no; any
// other failure (a permission, an I/O error, a store that could not be
// read or decoded, a system that cannot look) is unknown, never no.
func answerOf(found bool, err error) answer {
	switch {
	case err == nil && found:
		return answerYes
	case err == nil || absent(err):
		return answerNo
	}
	return answerUnknown
}

// absent says err is a look's finding that the thing is not there: no such
// file or record, a path that is not a directory, a folder outside the
// allowed roots or a worktree not where its run puts it (refused alike, so
// nothing of the path is learned), a folder that is no repository, a
// commit git does not find (git's exit 1 with nothing said).
func absent(err error) bool {
	var exit *exec.ExitError
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, errNotADirectory) ||
		errors.Is(err, errOutsideRoots) || errors.Is(err, errNotItsPlace) || errors.Is(err, journal.ErrNotFound) ||
		errors.Is(err, brakeman.ErrNotFound) || errors.Is(err, gitcli.ErrNotRepo) || (errors.As(err, &exit) && exit.ExitCode() == 1)
}

// errNotItsPlace is a worktree a row names anywhere but where its run puts
// it, or a step of that place that is a link or no directory: never looked
// at further, said as a worktree not kept.
var errNotItsPlace = errors.New("not where its run puts it")

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

// resumeOffer is whether Resume is offered (yes, no, or unknown when a
// check could not be made now: Resume is not offered and the words say
// why, but it is not a no), and why not or what it asks; Confirm says it
// needs the person's second yes (a lost record); URL is where the resume
// is instead (a crew member's run).
type resumeOffer struct {
	Offered answer `json:"offered"`
	Why     string `json:"why,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
	URL     string `json:"url,omitempty"`
}

// commitInRepo is whether a result commit is in its run's repository:
// yes, no, or unknown when git could not be asked in the folder opened.
type commitInRepo struct {
	ID      string `json:"id"`
	Present answer `json:"present"`
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
	// gone is the sources the reply leaves out (except), read once and
	// added to as the reply finds more (forgottenSources).
	gone     []string
	goneRead bool
	goneErr  error
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
// it by resolveCwd; err when it cannot be (answerOf tells a folder gone
// from one that could not be read).
type runFolder struct {
	cwd string
	f   *session.Folder
	err error
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
// journal no longer holds, each once: they are added to what the reply
// leaves out (except), and the indexer is asked to read each again (its
// pass removes it).
func (av *availability) forgottenSources(hits []history.Hit) []string {
	var out []string
	for _, h := range hits {
		id := recordOf(h.Entry)
		if id == "" || av.recordKept(id) {
			continue
		}
		if src := hSessionRef(id); !slices.Contains(out, src) {
			out = append(out, src)
			av.leaveOut(src)
		}
	}
	return out
}

// leaveOut adds the source to what the reply leaves out, once, and asks
// the indexer to read it again.
func (av *availability) leaveOut(src string) {
	if slices.Contains(av.gone, src) {
		return
	}
	av.gone = append(av.gone, src)
	if av.s.history != nil {
		av.s.history.mark(src)
	}
}

// maxSessionSourcePages bounds the pages of the index's session sources
// (history.MaxSourcesPage each) a reply reads to find those forgotten:
// several times the records the journal keeps (journal.MaxRecords).
const maxSessionSourcePages = 16

// errForgetting is a reply that cannot tell which records are forgotten:
// the journal's list could not be read, or the index holds more session
// sources than a reply reads. It is said as the history not ready.
var errForgetting = fmt.Errorf("%w: the session records could not all be compared with the history; try again", history.ErrNotReady)

// except is the sources the reply leaves out, so that a forgotten record
// counts in nothing it answers (a row, a total, a cursor, a repeat, a
// link, how far back the history holds): every session source the index
// holds whose record the journal no longer answers for (recordKept). It
// is read once, before the reply's first read of the index: the index's
// session sources (in the order of their keys, from session:) against the
// journal's own list of its records, each source the list lacks asked of
// the journal again (Get: a record begun since the list is kept). Every
// read of the index the reply makes takes it: a search's ExceptSources,
// Entry's, Links', Similar's and Stats' except. A record forgotten after
// it is read is found among the rows read (forgottenSources) and added.
func (av *availability) except() ([]string, error) {
	if av.goneRead || av.ix == nil {
		return av.gone, av.goneErr
	}
	av.goneRead = true
	held := map[string]bool{}
	if j := av.s.sessionRecords; j != nil {
		after := ""
		for range journal.MaxRecords {
			page, err := j.List(after, journal.MaxPage)
			if err != nil {
				av.goneErr = errForgetting
				return nil, av.goneErr
			}
			for _, r := range page.Records {
				held[r.ID] = true
			}
			if after = page.Next; after == "" {
				break
			}
		}
	}
	const prefix = "session:"
	after := prefix
	for range maxSessionSourcePages {
		srcs, err := av.ix.Sources(av.ctx, after, history.MaxSourcesPage)
		if err != nil {
			av.goneErr = err
			return nil, err
		}
		for _, src := range srcs {
			id, ok := strings.CutPrefix(src.Key, prefix)
			if !ok {
				return av.gone, nil
			}
			if !held[id] && !av.recordKept(id) {
				av.leaveOut(src.Key)
			}
		}
		if len(srcs) < history.MaxSourcesPage {
			return av.gone, nil
		}
		after = srcs[len(srcs)-1].Key
	}
	av.goneErr = errForgetting
	return nil, av.goneErr
}

// withoutForgotten is the hits whose session record, if any, the journal
// still holds.
func (av *availability) withoutForgotten(hits []history.Hit) []history.Hit {
	if len(av.forgottenSources(hits)) == 0 {
		return hits
	}
	return slices.DeleteFunc(slices.Clone(hits), func(h history.Hit) bool {
		id := recordOf(h.Entry)
		return id != "" && slices.Contains(av.gone, hSessionRef(id))
	})
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
		switch answerOf(err == nil && req.HasBrief, err) {
		case answerNo:
			out = availabilityOf{State: availPruned, Words: "pruned: Brakeman no longer keeps its brief"}
		case answerYes:
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
	var readErr error
	if av.s.records != nil {
		rec, ok, err := av.s.records.Get(run)
		if ok && err == nil {
			rf.cwd = rec.Cwd
		}
		readErr = err
	}
	if rf.cwd == "" && av.s.ledger != nil {
		row, ok, err := av.s.ledger.Run(av.ctx, run)
		if ok && err == nil {
			var c crew.Crew
			if err = json.Unmarshal([]byte(row.Crew), &c); err == nil {
				rf.cwd = c.Cwd
			}
		}
		readErr = cmpOrErr(err, readErr)
	}
	if rf.cwd == "" {
		// Not known: a read that failed, or neither the run's record nor
		// its row names it. Either way not a folder found gone.
		rf.err = fmt.Errorf("the run's folder is not known: %w", cmpOrErr(readErr, errFolderNotKnown))
		return rf
	}
	rf.f, rf.err = av.s.openCwd(rf.cwd)
	return rf
}

// errFolderNotKnown is a run whose folder neither its record nor its row
// names: unknown, never gone.
var errFolderNotKnown = errors.New("neither the run's record nor its row names it")

// cmpOrErr is the first error that is not nil.
func cmpOrErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// worktreeKept answers whether an attempt's worktree is where its run puts
// it (<run's folder>/.conductor/worktrees/<run>/<task>.<n>, crew's
// worktreesDir) and there: each step of that path a directory, never a
// link, looked at through the run's folder's root. A worktree its row
// names anywhere else is not looked at (no). A folder or a step that
// could not be looked at (its run's folder not known or not readable, a
// step's look failing for any reason but its absence) is unknown.
func (av *availability) worktreeKept(row ledger.AttemptRow) answer {
	name := fmt.Sprintf("%s.%d", row.TaskID, row.N)
	steps := []string{".conductor", "worktrees", row.RunID, name}
	rf := av.runFolder(row.RunID)
	switch {
	case rf.err != nil:
		return answerOf(false, rf.err)
	case row.Worktree != filepath.Join(append([]string{rf.cwd}, steps...)...):
		return answerOf(false, errNotItsPlace)
	}
	for i := range steps {
		fi, err := rf.f.Lstat(filepath.Join(steps[:i+1]...))
		if err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
			err = errNotItsPlace
		}
		if err != nil {
			return answerOf(false, err)
		}
	}
	return answerYes
}

// historyBeforeGit, when set (by a test), runs once an attempt's run
// folder is open and before git is asked in it, where a test changes the
// folder's path as another process could meanwhile.
var historyBeforeGit func(folder string)

// commitIn answers whether the commit id is in the repository of the
// run's folder, asked of git in the folder this reply opened (its
// directory itself, gitcli.RunIn), never at its path again: no when git
// does not find it there or the folder is no repository; unknown when the
// folder could not be opened or git asked in it (a system that cannot
// start git in an open directory among it).
func (av *availability) commitIn(run, id string) answer {
	rf := av.runFolder(run)
	if rf.err != nil {
		return answerOf(false, rf.err)
	}
	dir, err := rf.f.Dir()
	if err != nil {
		return answerUnknown
	}
	defer dir.Close()
	if historyBeforeGit != nil {
		historyBeforeGit(rf.f.Path())
	}
	ctx, cancel := context.WithTimeout(av.ctx, revisionTimeout)
	defer cancel()
	_, err = gitcli.RunIn(ctx, dir, "rev-parse", "--verify", "--quiet", id+"^{commit}")
	if errors.Is(err, fs.ErrNotExist) {
		// git could not start in the folder opened (removed since): that
		// says nothing of the commit.
		return answerUnknown
	}
	return answerOf(err == nil, err)
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
	var out availabilityOf
	switch av.worktreeKept(row) {
	case answerYes:
		out = availabilityOf{State: availWorktree, Words: "worktree kept"}
		if full {
			out.Branch = row.Branch
		}
	case answerNo:
		out = worktreeGone
	default:
		out = availabilityOf{State: availUnknown, Words: "its worktree could not be looked at; try again"}
	}
	if full && objectID(row.ResultCommit) {
		out.Commit = &commitInRepo{ID: row.ResultCommit, Present: av.commitIn(r.Run, row.ResultCommit)}
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
			} else {
				out.Words = "log kept (its size could not be read now)"
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
		return &resumeOffer{Offered: answerNo, Why: "this server keeps no history"}
	}
	rec, _, err := s.sessionRecords.Get(id)
	switch answerOf(err == nil, err) {
	case answerNo:
		return &resumeOffer{Offered: answerNo, Why: "its record is no longer kept"}
	case answerUnknown:
		return &resumeOffer{Offered: answerUnknown, Why: "its record could not be read now"}
	}
	return s.resumeOffered(rec)
}

// resumeOffered is resumeOfRecord's answer for the record rec: offered
// (with Confirm for a lost record), not offered with why (and URL where
// the resume is instead), or unknown when whether it is a crew member, or
// whether the folder it needs is there, could not be read.
func (s *Server) resumeOffered(rec journal.Record) *resumeOffer {
	id := rec.ID
	no := func(why string) *resumeOffer { return &resumeOffer{Offered: answerNo, Why: why} }
	if rec.Host.Hosted {
		return no(fmt.Sprintf("resume it on %s: `conductor host` resumes it there", cmpOr(rec.Host.Name, "its host")))
	}
	run, member, err := s.memberOf(rec)
	if err != nil {
		return &resumeOffer{Offered: answerUnknown, Why: "whether it was a crew member's session could not be read; try again"}
	}
	if member {
		switch {
		case rec.Crew != nil && rec.Crew.Brakeman != "":
			return no("a Brakeman turn is never resumed: ask Brakeman again")
		case rec.Crew != nil && rec.Crew.Task != "":
			return &resumeOffer{Offered: answerNo, Why: "a managed run continues from its run page", URL: "/runs/" + run}
		}
		return &resumeOffer{Offered: answerNo, Why: "resume it as the run's member, from its run", URL: "/runs/" + run}
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
		if plan.cwdErr != nil && answerOf(false, plan.cwdErr) == answerUnknown {
			return &resumeOffer{Offered: answerUnknown, Why: "whether the session's working directory is there could not be read; try again"}
		}
		return no(aerr.Message)
	}
	if plan.id == "" {
		if plan.agent.Session.Empty() {
			return no(plan.agent.Name + " has no session recipe: nothing of its conversation can be resumed")
		}
		return no("nothing to resume yet: the agent had no turn")
	}
	if rec.Ending == journal.EndingLost || (!rec.LostAt.IsZero() && rec.EndedAt.IsZero()) {
		return &resumeOffer{Offered: answerYes, Confirm: true, Why: fmt.Sprintf("the server stopped while this session ran on %s; its process may still be running", cmpOr(rec.Where, string(ext.WhereNative)))}
	}
	return &resumeOffer{Offered: answerYes}
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
