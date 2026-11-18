package crew

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
)

// A comparison measured (docs/plans/compare-crew.md, PR-2): what the
// ledger holds of a comparison's chain, read as of one revision, and what
// git says of the commits those rows name. Nothing an agent said of its
// own work is read here: the measured part is git and the ledger alone.

// MaxComparison bounds a comparison's reply as encoded.
const MaxComparison = 1 << 20

// maxCompareChain is how many runs of one chain a comparison walks: a
// comparison is continued at most maxCompareChain-1 times.
const maxCompareChain = 256

// ErrRunChanging is the answer when the chain moved through every read of
// its comparison: the route answers 503 run_changing and asks again.
var ErrRunChanging = errors.New("the run changed through every read of its comparison; ask again")

// errCompareChain refuses a comparison whose chain is longer than a
// comparison walks.
var errCompareChain = fmt.Errorf("%w: the comparison's chain is longer than %d runs", ErrChainTooLong, maxCompareChain)

// The comparison's bounds (the plan's table), each measured as encoded.
const (
	// compareReadTries bounds the reads made again because a run of the
	// chain moved while they were made.
	compareReadTries = 8
	// compareBudget is the whole measure's time with git.
	compareBudget = 20 * time.Second
	// maxDiffOutput is the diff's output read; past it the size is not
	// measured.
	maxDiffOutput = 4 << 20
	// maxDiffList is a candidate's files listed, maxComparePath a path's
	// encoded bytes.
	maxDiffList    = 100
	maxComparePath = 300
	// maxCompareArgs is a candidate's member's args listed, maxCompareArg
	// one's encoded bytes.
	maxCompareArgs = 32
	maxCompareArg  = 200
	// maxDepFiles and maxDepModules are a candidate's dependency files and
	// modules listed; maxDepVersion a version's encoded bytes; maxDepBlob a
	// dependency file read at a commit.
	maxDepFiles   = 20
	maxDepModules = 50
	maxDepVersion = 64
	maxDepBlob    = 1 << 20
	// maxVerifyFiles is a candidate's test and build files listed.
	maxVerifyFiles = 20
	// maxSharedCount is the commits rev-list reads on each side; past it
	// the count is not given. maxSharedList is the commits listed.
	maxSharedCount = 10000
	maxSharedList  = 10
	// maxCherryCommits is each side's commits since the base past which
	// equivalent patches are not looked for.
	maxCherryCommits = 200
	// maxCompareRows bounds the check rows; maxArgvPreview a row's argv
	// as shown, cut at an entry's end; maxArgvPaths the argv entries looked
	// up as files.
	maxCompareRows = 48
	maxArgvPreview = 1 << 10
	maxArgvPaths   = 32
	// maxWhy is a row's reasons, maxWhyBytes one's encoded bytes.
	maxWhy      = 8
	maxWhyBytes = 300
	// compareCacheSize is the measures kept in memory.
	compareCacheSize = 32
)

// The words the comparison says.
const (
	notMeasuredDiff = "the diff passes 4 MiB: not measured"
	moreThanShared  = "more than 10,000"
	cherryTooMany   = "not checked: more than 200 commits"
	cherryWords     = "same patches: checked; a change rewritten by hand is not detected"
	measureLate     = "not measured: the measure passed its 20 s budget"
)

// Comparison is what the engine measures of a comparison's chain: each
// candidate's shown result, its size, its dependency changes, the commits
// it shares with another lane, and each check's cells with its verdict.
type Comparison struct {
	Run        string              `json:"run"` // the chain's launched run
	Base       string              `json:"base"`
	Objective  string              `json:"objective"`
	Criteria   []Criterion         `json:"criteria"`
	Candidates []ComparedCandidate `json:"candidates"` // the block's order
	Checks     []ComparedCheck     `json:"checks"`
	Settled    bool                `json:"settled"`
}

// ComparedCandidate is one lane's shown candidate.
type ComparedCandidate struct {
	Task           string   `json:"task"`
	Member         string   `json:"member"`
	Agent          string   `json:"agent"`
	Args           []string `json:"args"`
	Approach       string   `json:"approach"`
	State          string   `json:"state"` // running | ready | failed | cancelled
	Run            string   `json:"run"`   // the run its attempt ran in
	Attempt        int      `json:"attempt"`
	Commit         string   `json:"commit"`
	Start          string   `json:"start"`
	Selectable     bool     `json:"selectable"`
	Attempts       int      `json:"attempts"`
	AcceptedAnyway bool     `json:"acceptedAnyway"`

	Size            *DiffSize       `json:"size,omitempty"`
	Deps            *DepChanges     `json:"deps,omitempty"`
	VerifyFiles     *FileList       `json:"verifyFiles,omitempty"`
	Shared          []SharedCommits `json:"shared"`
	Equivalent      []SharedCommits `json:"equivalent"`
	ManifestChanged bool            `json:"manifestChanged"`
	Unavailable     string          `json:"unavailable,omitempty"`
}

// The states of a candidate's lane.
const (
	LaneRunning   = "running"
	LaneReady     = "ready"
	LaneFailed    = "failed"
	LaneCancelled = "cancelled"
)

// DiffSize is a candidate's change against the base.
type DiffSize struct {
	Files        int        `json:"files"`
	Added        int        `json:"added"`
	Removed      int        `json:"removed"`
	FilesAdded   int        `json:"filesAdded"`
	FilesDeleted int        `json:"filesDeleted"`
	List         []FileStat `json:"list"` // at most maxDiffList
	Total        int        `json:"total"`
	NotMeasured  string     `json:"notMeasured,omitempty"`
}

// FileStat is one changed file.
type FileStat struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A | D | M | T
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

// DepChanges are the dependency files a candidate changed and the modules
// it added, removed or moved: Total counts the files, ModulesTotal the
// modules.
type DepChanges struct {
	Files        []string       `json:"files"`
	Modules      []ModuleChange `json:"modules"`
	Total        int            `json:"total"`
	ModulesTotal int            `json:"modulesTotal"`
}

// ModuleChange is one module's change: From empty when it was added, To
// empty when it was removed.
type ModuleChange struct {
	File string `json:"file"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// FileList is the test and build files a candidate changed.
type FileList struct {
	Files []ListedFile `json:"files"`
	Total int          `json:"total"`
}

// ListedFile is one file of a FileList.
type ListedFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // test | build
}

// The kinds of a listed file.
const (
	FileTest  = "test"
	FileBuild = "build"
)

// SharedCommits are the commits a candidate has in common with another
// lane's.
type SharedCommits struct {
	With    string   `json:"with"` // the other lane's task id
	Count   int      `json:"count"`
	Commits []string `json:"commits"`
	Note    string   `json:"note,omitempty"`
}

// ComparedCheck is one expected check across the candidates.
type ComparedCheck struct {
	Phase          string      `json:"phase"` // check | verify
	ID             string      `json:"id"`
	Argv           []string    `json:"argv"`
	ArgvCut        bool        `json:"argvCut"`
	ArgvDigest     string      `json:"argvDigest"`
	TimeoutSeconds int         `json:"timeoutSeconds"`
	Cells          []CheckCell `json:"cells"` // one per candidate, in column order
	Verdict        string      `json:"verdict"`
	Why            []string    `json:"why"`
}

// The verdicts of a check row.
const (
	VerdictComparable    = "comparable"
	VerdictUnknown       = "unknown"
	VerdictNotComparable = "not_comparable"
)

// CheckCell is one candidate's run of one check.
type CheckCell struct {
	Task       string `json:"task"`
	Attempt    int    `json:"attempt"`
	K          int    `json:"k"`
	State      string `json:"state"`
	Exit       *int   `json:"exit"`
	Signal     string `json:"signal"`
	TimedOut   bool   `json:"timedOut"`
	DurationMS int64  `json:"durationMs"`
	Where      string `json:"where"`
	Manifest   string `json:"manifest"`
	Stable     bool   `json:"stable"`
	Receipt    string `json:"receipt"`
	LogSHA256  string `json:"logSha256,omitempty"`
	NotRun     string `json:"notRun,omitempty"`
}

// A cell's state when the candidate has no receipt of the check, the
// state of a receipt whose run a stop ended, and why a cell was not run.
const (
	CellNotRun          = "not_run"
	CellCancelled       = "cancelled"
	NotRunEarlier       = "earlier_failed"
	NotRunNoSuchStep    = "no_such_step"
	NotRunPending       = "pending"
	NotRunEnded         = "ended"
	notRunEndedText     = "not run: its attempt ended before this check"
	notRunNoReceiptText = "not run: it has no receipt of this check"
	notRunEarlierText   = "not run: an earlier check failed"
	notRunNoStepText    = "not run: its manifest has no such step"
	notRunPendingText   = "not run yet: its checks are under way"
)

// SettledCandidate is one lane's shown candidate when every lane ended.
type SettledCandidate struct {
	Task    string `json:"task"`
	Run     string `json:"run"`
	Attempt int    `json:"attempt"`
	Commit  string `json:"commit"`
}

// eventCompareSettled is the run event the compare step writes.
const eventCompareSettled = "compare.settled"

// compareLedger is the ledger's reads a comparison makes: ledger.Ledger's.
type compareLedger interface {
	RunRow(ctx context.Context, id string) (ledger.RunRow, bool, error)
	Pointer(ctx context.Context, id string) (ledger.RunPointer, bool, error)
	Tasks(ctx context.Context, runID string) ([]ledger.TaskRow, error)
	Attempts(ctx context.Context, runID string) ([]ledger.AttemptRow, error)
	Checks(ctx context.Context, runID, taskID string, n int, withLog bool) ([]ledger.CheckRow, error)
	Trees(ctx context.Context, runID string) ([]ledger.TreeRow, error)
	NewestEvent(ctx context.Context, runID, kind string) (ledger.EventRow, bool, error)
}

func (s sqlLedger) Pointer(ctx context.Context, id string) (ledger.RunPointer, bool, error) {
	return s.l.Pointer(ctx, id)
}

func (s sqlLedger) Tasks(ctx context.Context, runID string) ([]ledger.TaskRow, error) {
	return s.l.Tasks(ctx, runID)
}

func (s sqlLedger) Attempts(ctx context.Context, runID string) ([]ledger.AttemptRow, error) {
	return s.l.Attempts(ctx, runID)
}

func (s sqlLedger) Checks(ctx context.Context, runID, taskID string, n int, withLog bool) ([]ledger.CheckRow, error) {
	return s.l.Checks(ctx, runID, taskID, n, withLog)
}

func (s sqlLedger) Trees(ctx context.Context, runID string) ([]ledger.TreeRow, error) {
	return s.l.Trees(ctx, runID)
}

func (s sqlLedger) NewestEvent(ctx context.Context, runID, kind string) (ledger.EventRow, bool, error) {
	return s.l.NewestEvent(ctx, runID, kind)
}

// compareMeasure is the engine's seams and cache of the measure: git, the
// clock its budget is read on, the hook between the ledger's reads, and
// the measures kept.
type compareMeasure struct {
	// git runs git for the measure; nil is gitcli.RunBounded.
	git func(ctx context.Context, dir string, limit int, args ...string) (string, error)
	// now is the budget's clock; nil is time.Now.
	now func() time.Time
	// afterRead, when set, runs between the ledger's first reads and the
	// revisions read again, on each try.
	afterRead func(runID string)
	// stopping, when set, runs once a run's stop has begun and before it
	// ends anything: a test ends a lane there (compare_step_test.go).
	stopping func(runID string)

	mu    sync.Mutex
	cache map[string]*measured
	order []string
}

// measured is what git said of a comparison's commits: each candidate's
// part, by task, and the files at the base an argv names, by its digest.
type measured struct {
	cands map[string]candidateMeasure
	named map[string][]string
}

// candidateMeasure is one candidate's measure; unavailable when a call of
// it failed (never kept in the cache then). paths are every path it
// changed, for the verdicts.
type candidateMeasure struct {
	size            *DiffSize
	deps            *DepChanges
	verify          *FileList
	paths           map[string]bool
	manifestChanged bool
	manifest        *Manifest // its file-form manifest, when it changed and could be read
	shared          []SharedCommits
	equivalent      []SharedCommits
	unavailable     string
}

// chainRead is what the ledger holds of a comparison's chain, read as of
// one revision.
type chainRead struct {
	chain    []ledger.RunPointer // launched first
	revs     map[string]int64
	launched ledger.RunRow
	crew     Crew
	tasks    []ledger.TaskRow // the newest run's
	attempts map[string][]ledger.AttemptRow
	checks   map[string][]ledger.CheckRow // by run/task/n
	trees    map[string][]ledger.TreeRow  // by run
	settled  []SettledCandidate           // the chain's newest compare.settled, nil for none
	// selected is the chain's applied selection, nil for none: the
	// selected lane shows the commit it names, whatever came after it.
	selected *AppliedSelection
}

// Comparison measures the comparison of the chain runID belongs to: any
// run of it, the launched one or a continuation, answers the same. A run
// the ledger has not, or one of no comparison, is ErrRunNotFound; a chain
// that moved through every read, ErrRunChanging. The ledger is read first,
// as of one revision of every run of the chain; then git, on the commits
// those rows name, under a budget of its own: a call that fails, is late
// or passes its bound leaves that candidate Unavailable and the rest whole.
func (e *Engine) Comparison(ctx context.Context, runID string) (Comparison, error) {
	c, _, err := e.comparisonAt(ctx, runID, nil)
	return c, err
}

// comparisonAt is Comparison with the revision of each run of the chain
// that its rows were read at; adjust, when given, changes the rows read
// before they are measured (measureAhead: what a change about to be made
// leaves of them).
func (e *Engine) comparisonAt(ctx context.Context, runID string, adjust func(*chainRead)) (Comparison, map[string]int64, error) {
	if !ValidRunID(runID) {
		return Comparison{}, nil, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	if e.ledger == nil {
		return Comparison{}, nil, ErrNoLedger
	}
	cl, ok := e.ledger.(compareLedger)
	if !ok {
		return Comparison{}, nil, fmt.Errorf("%w: this ledger cannot read a comparison", ErrNoLedger)
	}
	var cr chainRead
	for try := 1; ; try++ {
		var err error
		if cr, err = e.readChain(ctx, cl, runID); err != nil {
			return Comparison{}, nil, err
		}
		moved, err := cr.moved(ctx, cl)
		if err != nil {
			return Comparison{}, nil, err
		}
		if !moved {
			break
		}
		if try == compareReadTries {
			return Comparison{}, nil, ErrRunChanging
		}
	}
	revs := cr.revs
	if adjust != nil {
		adjust(&cr)
	}
	return e.measureComparison(ctx, cr), revs, nil
}

// readChain reads the chain runID is in: its runs' pointers, the launched
// run's row and crew, the newest run's tasks, every run's attempts, the
// shown candidates' receipts and trees, and the newest compare.settled.
func (e *Engine) readChain(ctx context.Context, cl compareLedger, runID string) (chainRead, error) {
	cr := chainRead{revs: map[string]int64{}, attempts: map[string][]ledger.AttemptRow{}, checks: map[string][]ledger.CheckRow{}, trees: map[string][]ledger.TreeRow{}}
	p, ok, err := cl.Pointer(ctx, runID)
	if err != nil {
		return cr, err
	}
	if !ok {
		return cr, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	chain := []ledger.RunPointer{p}
	seen := map[string]bool{p.ID: true}
	for prev := p.ContinuedFrom; prev != ""; {
		if len(chain) >= maxCompareChain || seen[prev] {
			return cr, errCompareChain
		}
		q, ok, err := cl.Pointer(ctx, prev)
		if err != nil {
			return cr, err
		}
		if !ok {
			break
		}
		seen[prev] = true
		chain = append(chain, q)
		prev = q.ContinuedFrom
	}
	slices.Reverse(chain)
	for next := p.ContinuedBy; next != ""; {
		if len(chain) >= maxCompareChain || seen[next] {
			return cr, errCompareChain
		}
		q, ok, err := cl.Pointer(ctx, next)
		if err != nil {
			return cr, err
		}
		if !ok {
			break
		}
		seen[next] = true
		chain = append(chain, q)
		next = q.ContinuedBy
	}
	cr.chain = chain
	for _, q := range chain {
		cr.revs[q.ID] = q.Revision
	}
	row, ok, err := cl.RunRow(ctx, chain[0].ID)
	if err != nil {
		return cr, err
	}
	if !ok {
		return cr, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	cr.launched = row
	if err := json.Unmarshal([]byte(row.Crew), &cr.crew); err != nil || cr.crew.Compare == nil {
		return cr, fmt.Errorf("%w: %s is of no comparison", ErrRunNotFound, runID)
	}
	newest := chain[len(chain)-1].ID
	if cr.tasks, err = cl.Tasks(ctx, newest); err != nil {
		return cr, err
	}
	for _, q := range chain {
		if cr.attempts[q.ID], err = cl.Attempts(ctx, q.ID); err != nil {
			return cr, err
		}
	}
	if cr.selected, err = readSelected(ctx, e.ledger, chain); err != nil {
		return cr, err
	}
	for _, cd := range cr.crew.Compare.Candidates {
		sh, ok := cr.shown(cd.Task)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s/%s/%d", sh.run, cd.Task, sh.attempt.N)
		if cr.checks[key], err = cl.Checks(ctx, sh.run, cd.Task, sh.attempt.N, false); err != nil {
			return cr, err
		}
		if _, read := cr.trees[sh.run]; !read {
			if cr.trees[sh.run], err = cl.Trees(ctx, sh.run); err != nil {
				return cr, err
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		ev, ok, err := cl.NewestEvent(ctx, chain[i].ID, eventCompareSettled)
		if err != nil {
			return cr, err
		}
		if ok {
			cr.settled = settledOf(ev.Payload)
			break
		}
	}
	if f := e.cmpMeasure.afterRead; f != nil {
		f(runID)
	}
	return cr, nil
}

// settledOf is a compare.settled event's set; an empty set for one that
// cannot be read (it was written, so the lanes settled).
func settledOf(payload string) []SettledCandidate {
	var p struct {
		Candidates []SettledCandidate `json:"candidates"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || p.Candidates == nil {
		return []SettledCandidate{}
	}
	return p.Candidates
}

// moved reports whether a run of the chain moved since it was read, or the
// chain grew.
func (cr chainRead) moved(ctx context.Context, cl compareLedger) (bool, error) {
	for i, q := range cr.chain {
		p, ok, err := cl.Pointer(ctx, q.ID)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		if p.Revision != cr.revs[q.ID] || (i == len(cr.chain)-1 && p.ContinuedBy != "") {
			return true, nil
		}
	}
	return false, nil
}

// shownAttempt is a lane's shown candidate: its attempt's row and the run
// that holds it, standing when it is the task's result.
type shownAttempt struct {
	run      string
	attempt  ledger.AttemptRow
	standing bool
}

// task is the newest run's row of the task id.
func (cr chainRead) task(id string) (ledger.TaskRow, bool) {
	i := slices.IndexFunc(cr.tasks, func(t ledger.TaskRow) bool { return t.ID == id })
	if i < 0 {
		return ledger.TaskRow{}, false
	}
	return cr.tasks[i], true
}

// attemptIn is the attempt n of the task in the run, from the rows read.
func (cr chainRead) attemptIn(run, task string, n int) (ledger.AttemptRow, bool) {
	for _, a := range cr.attempts[run] {
		if a.TaskID == task && a.N == n {
			return a, true
		}
	}
	return ledger.AttemptRow{}, false
}

// shown is the lane's shown candidate: its standing result along the chain
// (an imported one in its source run), else its last submitted commit; ok
// false for a lane that has submitted none.
func (cr chainRead) shown(task string) (shownAttempt, bool) {
	tr, ok := cr.task(task)
	if !ok {
		return shownAttempt{}, false
	}
	// Once a selection is applied, the selected lane shows the commit it
	// named: a later candidate of the task (a Retry, a repair) is of the
	// approach, and RV1's Review view and the candidates list show it.
	if sel := cr.selected; sel != nil && !sel.None && sel.Task == task {
		if a, ok := cr.attemptIn(sel.Run, task, sel.Attempt); ok && submittedCommit(a) == sel.Commit {
			return shownAttempt{run: sel.Run, attempt: a, standing: true}, true
		}
	}
	if tr.State == ledger.TaskSucceeded && tr.ResultAttempt > 0 && tr.ResultAttempt > tr.Withdrawn {
		run := cr.chain[len(cr.chain)-1].ID
		if src, tid, n, ok := parseImport(tr.ImportedFrom); ok && tid == task && n == tr.ResultAttempt {
			run = src
		}
		if a, ok := cr.attemptIn(run, task, tr.ResultAttempt); ok {
			return shownAttempt{run: run, attempt: a, standing: true}, true
		}
		for i := len(cr.chain) - 1; i >= 0; i-- {
			if a, ok := cr.attemptIn(cr.chain[i].ID, task, tr.ResultAttempt); ok {
				return shownAttempt{run: cr.chain[i].ID, attempt: a, standing: true}, true
			}
		}
	}
	var best shownAttempt
	for _, q := range cr.chain {
		for _, a := range cr.attempts[q.ID] {
			if a.TaskID != task || a.N <= tr.Withdrawn || submittedCommit(a) == "" || a.N <= best.attempt.N {
				continue
			}
			best = shownAttempt{run: q.ID, attempt: a}
		}
	}
	return best, best.attempt.N > 0
}

// submittedCommit is the commit an attempt submitted, "" for none.
func submittedCommit(a ledger.AttemptRow) string {
	if a.ResultCommit != "" {
		return a.ResultCommit
	}
	if a.Result == "" {
		return ""
	}
	var res Result
	if json.Unmarshal([]byte(a.Result), &res) != nil {
		return ""
	}
	return res.Commit
}

// laneState is a candidate's lane as its task's row says.
func laneState(tr ledger.TaskRow) string {
	switch tr.State {
	case ledger.TaskSucceeded:
		return LaneReady
	case ledger.TaskFailed:
		return LaneFailed
	case ledger.TaskCancelled:
		return LaneCancelled
	}
	return LaneRunning
}

// measureComparison is the reply of cr: the candidates' columns from the
// rows, then git's measure of them, then the check rows and their
// verdicts.
func (e *Engine) measureComparison(ctx context.Context, cr chainRead) Comparison {
	c, cmp := cr.crew, cr.crew.Compare
	out := Comparison{Run: cr.launched.ID, Base: cr.launched.Base, Objective: c.Goal, Criteria: slices.Clone(cmp.Criteria),
		Candidates: make([]ComparedCandidate, 0, len(cmp.Candidates)), Checks: []ComparedCheck{}}
	if out.Criteria == nil {
		out.Criteria = []Criterion{}
	}
	shown := map[string]shownAttempt{}
	ended := true
	for _, cd := range cmp.Candidates {
		cc := ComparedCandidate{Task: cd.Task, Approach: cd.Approach, Args: []string{}, Shared: []SharedCommits{}, Equivalent: []SharedCommits{}, State: LaneRunning}
		if t := taskOfCrew(c, cd.Task); t != nil {
			cc.Member = t.Member
			if m := memberOfCrew(c, t.Member); m != nil {
				cc.Agent = cutEncoded(m.AgentID, maxCompareArg)
				for _, a := range m.Args {
					if len(cc.Args) == maxCompareArgs {
						break
					}
					cc.Args = append(cc.Args, cutEncoded(a, maxCompareArg))
				}
			}
		}
		if tr, ok := cr.task(cd.Task); ok {
			cc.State, cc.Attempts = laneState(tr), tr.Attempts
		}
		if !cr.laneEnded(cd.Task) {
			ended = false
		}
		if sh, ok := cr.shown(cd.Task); ok {
			shown[cd.Task] = sh
			cc.Run, cc.Attempt, cc.Commit, cc.Start = sh.run, sh.attempt.N, submittedCommit(sh.attempt), sh.attempt.StartCommit
			cc.Selectable = sh.standing
			cc.AcceptedAnyway = sh.standing && sh.attempt.State != ledger.AttemptSucceeded
		}
		out.Candidates = append(out.Candidates, cc)
	}
	out.Settled = ended && cr.settled != nil && slices.Equal(cr.settled, cr.settledSet())
	cm := &e.cmpMeasure
	git := budgeted{git: cm.runGit, now: cm.clock, deadline: cm.clock().Add(compareBudget)}.run
	rows := expectedRows(ctx, c, cr.launched.Base, git)
	rows = addReceiptRows(rows, out.Candidates, cr)
	m := e.measureGit(ctx, git, c, cr.launched.Base, out.Candidates, rows)
	for i := range out.Candidates {
		cc := &out.Candidates[i]
		if cc.Commit == "" {
			continue
		}
		cm := m.cands[cc.Task]
		cc.Size, cc.Deps, cc.VerifyFiles, cc.ManifestChanged, cc.Unavailable = cm.size, cm.deps, cm.verify, cm.manifestChanged, cm.unavailable
		if cm.shared != nil {
			cc.Shared = cm.shared
		}
		if cm.equivalent != nil {
			cc.Equivalent = cm.equivalent
		}
	}
	for _, rw := range rows {
		out.Checks = append(out.Checks, rw.compare(out.Candidates, shown, cr, m))
	}
	return out
}

// columnsOf is cr's candidates as columns: each lane's shown candidate.
func (e *Engine) columnsOf(cr chainRead) []ComparedCandidate {
	var out []ComparedCandidate
	for _, cd := range cr.crew.Compare.Candidates {
		cc := ComparedCandidate{Task: cd.Task}
		if sh, ok := cr.shown(cd.Task); ok {
			cc.Run, cc.Attempt, cc.Commit = sh.run, sh.attempt.N, submittedCommit(sh.attempt)
		}
		out = append(out, cc)
	}
	return out
}

// laneEnded reports, from the rows, whether the lane of the candidate
// task has ended: its task terminal, and every review of it terminal or
// waiting behind a need that failed or was cancelled.
func (cr chainRead) laneEnded(task string) bool {
	tr, ok := cr.task(task)
	if !ok || !terminalTask(tr.State) {
		return false
	}
	for _, t := range cr.crew.Tasks {
		if t.Kind != TaskReview || !slices.Contains(t.Needs, task) {
			continue
		}
		rv, ok := cr.task(t.ID)
		if !ok {
			return false
		}
		if terminalTask(rv.State) {
			continue
		}
		if rv.State == ledger.TaskBlocked && (tr.State == ledger.TaskFailed || tr.State == ledger.TaskCancelled) {
			continue
		}
		return false
	}
	return true
}

// settledSet is the shown candidates of the lanes with a standing result,
// in the block's order, as the compare step writes them.
func (cr chainRead) settledSet() []SettledCandidate {
	set := []SettledCandidate{}
	for _, cd := range cr.crew.Compare.Candidates {
		if sh, ok := cr.shown(cd.Task); ok && sh.standing {
			set = append(set, SettledCandidate{Task: cd.Task, Run: sh.run, Attempt: sh.attempt.N, Commit: submittedCommit(sh.attempt)})
		}
	}
	return set
}

// terminalTask reports whether a task's state is an end.
func terminalTask(state string) bool {
	return state == ledger.TaskSucceeded || state == ledger.TaskFailed || state == ledger.TaskCancelled
}

func taskOfCrew(c Crew, id string) *Task {
	for i := range c.Tasks {
		if c.Tasks[i].ID == id {
			return &c.Tasks[i]
		}
	}
	return nil
}

func memberOfCrew(c Crew, name string) *Member {
	for i := range c.Members {
		if c.Members[i].Name == name {
			return &c.Members[i]
		}
	}
	return nil
}

// runGit is the measure's git.
func (cm *compareMeasure) runGit(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	if cm.git != nil {
		return cm.git(ctx, dir, limit, args...)
	}
	return gitcli.RunBounded(ctx, dir, limit, args...)
}

func (cm *compareMeasure) clock() time.Time {
	if cm.now != nil {
		return cm.now()
	}
	return time.Now()
}

// gitFunc is how the measure runs git.
type gitFunc func(ctx context.Context, dir string, limit int, args ...string) (string, error)

// budgeted is a gitFunc held to a deadline on the measure's clock: a call
// made past it is refused as late, and each call runs under a context
// that ends at the deadline.
type budgeted struct {
	git      gitFunc
	now      func() time.Time
	deadline time.Time
}

// errLate is a call refused for the measure's budget.
var errLate = errors.New(measureLate)

func (b budgeted) run(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	left := b.deadline.Sub(b.now())
	if left <= 0 {
		return "", errLate
	}
	cctx, cancel := context.WithTimeoutCause(ctx, left, errLate)
	defer cancel()
	out, err := b.git(cctx, dir, limit, args...)
	if err != nil && errors.Is(context.Cause(cctx), errLate) && ctx.Err() == nil {
		return out, errLate
	}
	return out, err
}

// measureGit measures every candidate with a commit, from the cache when
// the same commits were measured before. A candidate whose measure failed
// is not kept, so the next read tries again.
func (e *Engine) measureGit(ctx context.Context, git gitFunc, c Crew, base string, cands []ComparedCandidate, rows []*checkRow) *measured {
	key := base
	for _, cc := range cands {
		key += "|" + cc.Task + "=" + cc.Commit
	}
	cm := &e.cmpMeasure
	cm.mu.Lock()
	if cm.cache == nil {
		cm.cache = map[string]*measured{}
	}
	kept := cm.cache[key]
	if kept != nil {
		cm.order = slices.DeleteFunc(cm.order, func(k string) bool { return k == key })
		cm.order = append(cm.order, key)
	}
	cm.mu.Unlock()
	if kept != nil && kept.covers(rows) {
		return kept
	}
	m := &measured{cands: map[string]candidateMeasure{}, named: map[string][]string{}}
	if kept != nil {
		m.cands = kept.cands
		for k, v := range kept.named {
			m.named[k] = v
		}
	} else {
		measureCandidates(ctx, git, c, base, cands, m)
	}
	whole := true
	for _, cc := range cands {
		if cc.Commit != "" && m.cands[cc.Task].unavailable != "" {
			whole = false
		}
	}
	for _, rw := range rows {
		if _, ok := m.named[rw.digest]; ok {
			continue
		}
		named, err := namedFiles(ctx, git, c.Cwd, base, rw.argv)
		if err != nil {
			whole = false
			continue
		}
		m.named[rw.digest] = named
	}
	if whole {
		cm.mu.Lock()
		if _, ok := cm.cache[key]; !ok {
			cm.order = append(cm.order, key)
		}
		cm.cache[key] = m
		for len(cm.order) > compareCacheSize {
			delete(cm.cache, cm.order[0])
			cm.order = cm.order[1:]
		}
		cm.mu.Unlock()
	}
	return m
}

// covers reports whether m has looked up every row's named files.
func (m *measured) covers(rows []*checkRow) bool {
	for _, rw := range rows {
		if _, ok := m.named[rw.digest]; !ok {
			return false
		}
	}
	return true
}

// measureCandidates measures each candidate with a commit into m: its
// diff, its dependency, test and build files, its manifest's blob, then
// the commits it shares with each other lane's.
func measureCandidates(ctx context.Context, git gitFunc, c Crew, base string, cands []ComparedCandidate, m *measured) {
	dir := c.Cwd
	file := c.Manifest != nil && c.Manifest.File
	prefix := ""
	var prefixErr error
	if file {
		out, err := git(ctx, dir, 4<<10, "rev-parse", "--show-prefix")
		prefix, prefixErr = strings.TrimSpace(out), err
	}
	for _, cc := range cands {
		if cc.Commit == "" {
			continue
		}
		cm := candidateMeasure{paths: map[string]bool{}}
		fail := func(err error) {
			if cm.unavailable == "" {
				cm.unavailable = gitWords(err)
			}
		}
		stats, err := diffOf(ctx, git, dir, base, cc.Commit)
		switch {
		case errors.Is(err, gitcli.ErrOutputLimit):
			cm.size = &DiffSize{List: []FileStat{}, NotMeasured: notMeasuredDiff}
		case err != nil:
			fail(err)
		default:
			cm.size = sizeOf(stats)
			for _, st := range stats {
				cm.paths[st.Path] = true
			}
			if cm.deps, err = depsOf(ctx, git, dir, base, cc.Commit, stats); err != nil {
				fail(err)
			}
			if cm.verify, err = verifyFilesOf(ctx, git, dir, base, cc.Commit, stats); err != nil {
				fail(err)
			}
		}
		if file && prefixErr != nil {
			fail(prefixErr)
		}
		if file && cm.unavailable == "" {
			path := prefix + manifestFile
			at, aerr := blobAt(ctx, git, dir, cc.Commit, path)
			was, berr := blobAt(ctx, git, dir, base, path)
			if err := cmpOr(aerr, berr); err != nil {
				fail(err)
			} else if at != was {
				cm.manifestChanged = true
				if at != "" {
					rec, ok, err := manifestAt(ctx, git, dir, at)
					switch {
					case err != nil:
						fail(err)
					case ok:
						cm.manifest = &rec
					}
				} else {
					cm.manifest = &Manifest{}
				}
			}
		}
		m.cands[cc.Task] = cm
	}
	sharedOf(ctx, git, dir, base, cands, m)
}

// cmpOr is the first error that is not nil.
func cmpOr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// gitWords is a failed git call's words for a candidate's Unavailable.
func gitWords(err error) string {
	if errors.Is(err, errLate) {
		return measureLate
	}
	if errors.Is(err, gitcli.ErrOutputLimit) {
		return "not measured: git's answer passed its bound"
	}
	return cutEncoded("not measured: "+err.Error(), maxWhyBytes)
}

// diffOf is the candidate's change against the base: each path with its
// raw status and its line counts, renames off whatever the repository's
// configuration says.
func diffOf(ctx context.Context, git gitFunc, dir, base, commit string) ([]FileStat, error) {
	out, err := git(ctx, dir, maxDiffOutput, "diff", "--no-renames", "--raw", "--numstat", "-z", base, commit, "--")
	if err != nil {
		return nil, err
	}
	var stats []FileStat
	at := map[string]int{}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		switch {
		case p == "":
		case strings.HasPrefix(p, ":"):
			// :<mode> <mode> <object> <object> <status>, then the path.
			f := strings.Fields(p)
			if len(f) < 5 || i+1 >= len(parts) {
				return nil, fmt.Errorf("git diff answered %q", cutEncoded(p, 80))
			}
			i++
			st := FileStat{Path: parts[i], Status: f[4][:1]}
			at[st.Path] = len(stats)
			stats = append(stats, st)
		default:
			// <added>\t<removed>\t<path>
			f := strings.SplitN(p, "\t", 3)
			if len(f) != 3 {
				return nil, fmt.Errorf("git diff answered %q", cutEncoded(p, 80))
			}
			j, ok := at[f[2]]
			if !ok {
				at[f[2]] = len(stats)
				j = len(stats)
				stats = append(stats, FileStat{Path: f[2], Status: "M"})
			}
			if f[0] == "-" && f[1] == "-" {
				stats[j].Binary = true
				continue
			}
			stats[j].Added, _ = strconv.Atoi(f[0])
			stats[j].Removed, _ = strconv.Atoi(f[1])
		}
	}
	return stats, nil
}

// sizeOf is the diff's size, its list cut at maxDiffList with the total.
func sizeOf(stats []FileStat) *DiffSize {
	ds := &DiffSize{Files: len(stats), Total: len(stats), List: []FileStat{}}
	for _, st := range stats {
		ds.Added += st.Added
		ds.Removed += st.Removed
		switch st.Status {
		case "A":
			ds.FilesAdded++
		case "D":
			ds.FilesDeleted++
		}
		if len(ds.List) < maxDiffList {
			st.Path = cutEncoded(st.Path, maxComparePath)
			ds.List = append(ds.List, st)
		}
	}
	return ds
}

// blobAt is the object id of path at commit, "" when the commit has none
// there.
func blobAt(ctx context.Context, git gitFunc, dir, commit, path string) (string, error) {
	out, err := git(ctx, dir, 4<<10, "ls-tree", "-z", "--full-tree", commit, "--", ":(literal)"+path)
	if err != nil {
		return "", err
	}
	entry := strings.TrimSuffix(out, "\x00")
	if entry == "" {
		return "", nil
	}
	meta, _, _ := strings.Cut(entry, "\t")
	f := strings.Fields(meta)
	if len(f) != 3 {
		return "", fmt.Errorf("git ls-tree answered %q", cutEncoded(entry, 80))
	}
	return f[2], nil
}

// manifestAt reads a file-form manifest's blob, as much as a comparison
// needs of it (its verify steps' ids): ok false when it is no manifest,
// the error when git could not read it.
func manifestAt(ctx context.Context, git gitFunc, dir, blob string) (Manifest, bool, error) {
	out, err := git(ctx, dir, maxManifestFile, "cat-file", "blob", blob)
	if err != nil {
		return Manifest{}, false, err
	}
	var rec Manifest
	if json.Unmarshal([]byte(out), &rec) != nil {
		return Manifest{}, false, nil
	}
	return rec, true, nil
}

// sharedOf fills each candidate's Shared and Equivalent: for each other
// lane with a commit, the commits both reach that the base does not, and
// the commits whose patch one of the other's carries (git cherry). A
// candidate whose measure failed is passed over.
func sharedOf(ctx context.Context, git gitFunc, dir, base string, cands []ComparedCandidate, m *measured) {
	type since struct {
		list  []string
		more  bool
		count int
		err   error
	}
	sinces := map[string]*since{}
	of := func(cc ComparedCandidate) *since {
		if s, ok := sinces[cc.Task]; ok {
			return s
		}
		s := &since{}
		out, err := git(ctx, dir, (maxSharedCount+1)*65, "rev-list", "--max-count="+strconv.Itoa(maxSharedCount+1), base+".."+cc.Commit, "--")
		if err != nil {
			s.err = err
		} else {
			s.list = strings.Fields(out)
			s.more = len(s.list) > maxSharedCount
			s.count = len(s.list)
		}
		sinces[cc.Task] = s
		return s
	}
	// The candidates measured whole so far: one whose commit git could
	// not read is paired with none, so its failure is its own alone.
	whole := map[string]bool{}
	for _, cc := range cands {
		if cm, ok := m.cands[cc.Task]; ok && cc.Commit != "" && cm.unavailable == "" {
			whole[cc.Task] = true
		}
	}
	for _, a := range cands {
		cm := m.cands[a.Task]
		if !whole[a.Task] {
			continue
		}
		cm.shared, cm.equivalent = []SharedCommits{}, []SharedCommits{}
		sa := of(a)
		if sa.err != nil {
			cm.unavailable = gitWords(sa.err)
			m.cands[a.Task] = cm
			continue
		}
		for _, b := range cands {
			if b.Task == a.Task || !whole[b.Task] {
				continue
			}
			sb := of(b)
			if sb.err != nil {
				continue // b's own failure, said in its column
			}
			if sa.more || sb.more {
				cm.shared = append(cm.shared, SharedCommits{With: b.Task, Commits: []string{}, Note: moreThanShared})
			} else {
				in := map[string]bool{}
				for _, id := range sb.list {
					in[id] = true
				}
				sc := SharedCommits{With: b.Task, Commits: []string{}}
				for _, id := range sa.list {
					if in[id] {
						sc.Count++
						if len(sc.Commits) < maxSharedList {
							sc.Commits = append(sc.Commits, id)
						}
					}
				}
				if sc.Count > 0 {
					cm.shared = append(cm.shared, sc)
				}
			}
			if sa.count > maxCherryCommits || sb.count > maxCherryCommits {
				cm.equivalent = append(cm.equivalent, SharedCommits{With: b.Task, Commits: []string{}, Note: cherryTooMany})
				continue
			}
			out, err := git(ctx, dir, (maxCherryCommits+1)*67, "cherry", b.Commit, a.Commit, base)
			if err != nil {
				if cm.unavailable == "" {
					cm.unavailable = gitWords(err)
				}
				continue
			}
			sc := SharedCommits{With: b.Task, Commits: []string{}}
			for _, line := range strings.Split(out, "\n") {
				if id, ok := strings.CutPrefix(line, "- "); ok {
					sc.Count++
					if len(sc.Commits) < maxSharedList {
						sc.Commits = append(sc.Commits, strings.TrimSpace(id))
					}
				}
			}
			if sc.Count > 0 {
				sc.Note = cherryWords
				cm.equivalent = append(cm.equivalent, sc)
			}
		}
		m.cands[a.Task] = cm
	}
}

// checkRow is a check row being built: its phase and id, the argv and
// timeout expected (or the first receipt's), whether the base expects it,
// and its argv's digest.
type checkRow struct {
	phase, id string
	argv      []string
	timeout   int
	expected  bool
	digest    string
}

// expectedRows are the rows every candidate is checked by: the candidate
// tasks' checks as launched, in order, then the verify steps of the
// manifest at the base (the crew's, or the file read there).
func expectedRows(ctx context.Context, c Crew, base string, git gitFunc) []*checkRow {
	var rows []*checkRow
	if len(c.Compare.Candidates) > 0 {
		if t := taskOfCrew(c, c.Compare.Candidates[0].Task); t != nil {
			for _, ch := range t.Checks {
				rows = append(rows, newRow(ledger.PhaseCheck, ch, true))
			}
		}
	}
	var verify []Check
	switch {
	case c.Manifest != nil && c.Manifest.File:
		out, err := git(ctx, c.Cwd, 4<<10, "rev-parse", "--show-prefix")
		if err == nil {
			if blob, err := blobAt(ctx, git, c.Cwd, base, strings.TrimSpace(out)+manifestFile); err == nil && blob != "" {
				if rec, ok, _ := manifestAt(ctx, git, c.Cwd, blob); ok {
					verify = rec.Verify
				}
			}
		}
	case c.Manifest != nil:
		verify = c.Manifest.Verify
	}
	for _, ch := range verify {
		rows = append(rows, newRow(ledger.PhaseVerify, ch, true))
	}
	return rows
}

func newRow(phase string, ch Check, expected bool) *checkRow {
	timeout := ch.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultCheckTimeout
	}
	return &checkRow{phase: phase, id: ch.ID, argv: slices.Clone(ch.Argv), timeout: timeout, expected: expected, digest: argvDigest(ch.Argv)}
}

// argvDigest is the SHA-256 of an argv as recorded, encoded as JSON so no
// two argvs share one.
func argvDigest(argv []string) string {
	b, _ := json.Marshal(argv)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// addReceiptRows adds a row for each other check or verify id a shown
// candidate's receipts show, in column order, within maxCompareRows.
func addReceiptRows(rows []*checkRow, cands []ComparedCandidate, cr chainRead) []*checkRow {
	has := map[string]bool{}
	for _, rw := range rows {
		has[rw.phase+"/"+rw.id] = true
	}
	for _, cc := range cands {
		if cc.Commit == "" {
			continue
		}
		for _, row := range cr.checks[fmt.Sprintf("%s/%s/%d", cc.Run, cc.Task, cc.Attempt)] {
			phase := cmpPhase(row.Phase)
			if phase != ledger.PhaseCheck && phase != ledger.PhaseVerify {
				continue
			}
			if has[phase+"/"+row.ID] || len(rows) >= maxCompareRows {
				continue
			}
			has[phase+"/"+row.ID] = true
			rows = append(rows, &checkRow{phase: phase, id: row.ID, argv: slices.Clone(row.Argv), digest: argvDigest(row.Argv)})
		}
	}
	return rows
}

func cmpPhase(p string) string {
	if p == "" {
		return ledger.PhaseCheck
	}
	return p
}

// compare is the row across the candidates: a cell each, and its verdict
// with its words.
func (rw *checkRow) compare(cands []ComparedCandidate, shown map[string]shownAttempt, cr chainRead, m *measured) ComparedCheck {
	cc := ComparedCheck{Phase: rw.phase, ID: rw.id, ArgvDigest: rw.digest, TimeoutSeconds: rw.timeout, Cells: []CheckCell{}, Why: []string{}}
	cc.Argv, cc.ArgvCut = argvPreview(rw.argv)
	named, namedRead := m.named[rw.digest]
	var why []string
	say := func(format string, args ...any) {
		why = append(why, fmt.Sprintf(format, args...))
	}
	notComparable := false
	type seen struct{ task, value string }
	var digests, wheres, manifests, timeouts []seen
	for _, cand := range cands {
		if cand.Commit == "" {
			notComparable = true
			say("%s has no result", cand.Task)
			continue
		}
		sh := shown[cand.Task]
		cell := CheckCell{Task: cand.Task, Attempt: cand.Attempt, State: CellNotRun}
		row, ok := latestRow(cr.checks[fmt.Sprintf("%s/%s/%d", cand.Run, cand.Task, cand.Attempt)], rw.phase, rw.id)
		if !ok {
			receipts := cr.checks[fmt.Sprintf("%s/%s/%d", cand.Run, cand.Task, cand.Attempt)]
			cell.NotRun = notRunOf(rw, sh, m.cands[cand.Task], receipts)
			notComparable = true
			say("%s: %s", cand.Task, notRunWordsOf(cell.NotRun, sh.attempt))
			cc.Cells = append(cc.Cells, cell)
			continue
		}
		cell.K, cell.State, cell.Signal, cell.TimedOut, cell.Where, cell.Stable = row.K, row.State, row.Signal, row.TimedOut, row.Where, row.Stable
		cell.Manifest = row.Manifest
		if rw.phase == ledger.PhaseCheck {
			cell.Manifest = checkoutManifest(cr.trees[cand.Run], cand.Task, cand.Attempt)
		}
		cell.Receipt = fmt.Sprintf("check:%s/%s/%d/%s/%d", cand.Run, cand.Task, cand.Attempt, row.ID, row.K)
		cell.LogSHA256 = row.LogSHA256
		if row.State == ledger.CheckFinished {
			exit := row.Exit
			cell.Exit = &exit
		}
		if row.Killed && !row.TimedOut {
			cell.State = CellCancelled
		}
		if !row.FinishedAt.IsZero() && !row.StartedAt.IsZero() {
			cell.DurationMS = row.FinishedAt.Sub(row.StartedAt).Milliseconds()
		}
		switch {
		case cell.State != ledger.CheckFinished:
			notComparable = true
			say("%s: its run did not finish (%s)", cand.Task, cell.State)
		case !row.Stable:
			notComparable = true
			say("%s: its run is not stable", cand.Task)
		}
		if row.Subject != cand.Commit {
			notComparable = true
			say("%s: it checked %s, not the candidate's commit", cand.Task, shortID(row.Subject))
		}
		digests = append(digests, seen{cand.Task, argvDigest(row.Argv)})
		wheres = append(wheres, seen{cand.Task, row.Where})
		manifests = append(manifests, seen{cand.Task, cell.Manifest})
		timeouts = append(timeouts, seen{cand.Task, strconv.Itoa(rw.timeoutOf(cr.crew, cand.Task, m.cands[cand.Task]))})
		cc.Cells = append(cc.Cells, cell)
	}
	differs := func(vals []seen, what string) {
		for _, v := range vals[min(1, len(vals)):] {
			if v.value != vals[0].value {
				notComparable = true
				say("%s: %s and %s differ", what, vals[0].task, v.task)
				return
			}
		}
	}
	if rw.expected {
		for _, d := range digests {
			if d.value != rw.digest {
				notComparable = true
				say("%s ran another command than the expected one", d.task)
			}
		}
	}
	differs(digests, "the command")
	differs(wheres, "where it ran")
	differs(manifests, "the manifest")
	differs(timeouts, "the timeout")
	for _, cand := range cands {
		cm, ok := m.cands[cand.Task]
		if cand.Commit == "" || !ok {
			continue
		}
		if cm.manifestChanged {
			notComparable = true
			say("%s changed the project manifest", cand.Task)
		}
		if cm.unavailable != "" || cm.size == nil || cm.size.NotMeasured != "" {
			continue
		}
		for _, path := range named {
			if cm.paths[path] {
				notComparable = true
				say("%s changed %s, which the check runs", cand.Task, cutEncoded(path, maxComparePath))
			}
		}
	}
	unknown := false
	if !notComparable && !namedRead {
		// The files its command names were not looked up (git failed or
		// was late): what a candidate changed of them is not known.
		unknown = true
		say("comparable only if no candidate changed what it runs: the files its command names could not be read")
	}
	if !notComparable {
		var changed []string
		for _, cand := range cands {
			cm := m.cands[cand.Task]
			switch {
			case cm.unavailable != "":
				unknown = true
				changed = append(changed, fmt.Sprintf("%s's change could not be read", cand.Task))
			case cm.size == nil || cm.size.NotMeasured != "":
				unknown = true
				changed = append(changed, fmt.Sprintf("%s's change was not measured", cand.Task))
			case cm.verify != nil && cm.verify.Total > 0:
				unknown = true
				f := cm.verify.Files[0]
				more := ""
				if cm.verify.Total > 1 {
					more = fmt.Sprintf(" and %d more", cm.verify.Total-1)
				}
				changed = append(changed, fmt.Sprintf("%s changed %s, a %s file%s", cand.Task, f.Path, f.Kind, more))
			default:
				changed = append(changed, fmt.Sprintf("%s changed none", cand.Task))
			}
		}
		if unknown {
			say("comparable only if their test changes are fair: %s", strings.Join(changed, "; "))
		}
	}
	switch {
	case notComparable:
		cc.Verdict = VerdictNotComparable
	case unknown:
		cc.Verdict = VerdictUnknown
	default:
		cc.Verdict = VerdictComparable
	}
	for _, w := range why {
		if len(cc.Why) == maxWhy {
			break
		}
		if !slices.Contains(cc.Why, w) {
			cc.Why = append(cc.Why, cutEncoded(w, maxWhyBytes))
		}
	}
	return cc
}

// timeoutOf is the timeout the candidate's tree gave the row: its task's
// check as launched, or the verify step of its own manifest when it
// changed it, else the base's; 0 when none is known.
func (rw *checkRow) timeoutOf(c Crew, task string, cm candidateMeasure) int {
	var steps []Check
	switch {
	case rw.phase == ledger.PhaseCheck:
		if t := taskOfCrew(c, task); t != nil {
			steps = t.Checks
		}
	case cm.manifestChanged && cm.manifest != nil:
		steps = cm.manifest.Verify
	default:
		return rw.timeout
	}
	for _, ch := range steps {
		if ch.ID == rw.id {
			return cmpOrInt(ch.TimeoutSeconds, defaultCheckTimeout)
		}
	}
	return rw.timeout
}

func cmpOrInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// latestRow is the receipt of the phase and id with the highest k.
func latestRow(rows []ledger.CheckRow, phase, id string) (ledger.CheckRow, bool) {
	var best ledger.CheckRow
	found := false
	for _, row := range rows {
		if cmpPhase(row.Phase) == phase && row.ID == id && (!found || row.K > best.K) {
			best, found = row, true
		}
	}
	return best, found
}

// notRunOf says why a candidate has no receipt of the row: its checks
// still under way; a verify step the manifest its tree ran (its own, when
// it changed it, else the base's) does not have; a check before it that
// failed (the tree's checks stop at the first); else its attempt ended
// before this check (stopped, interrupted, cancelled, or failed before its
// checks ran); "" when none of these is known. receipts are the
// attempt's.
func notRunOf(rw *checkRow, sh shownAttempt, cm candidateMeasure, receipts []ledger.CheckRow) string {
	switch sh.attempt.State {
	case ledger.AttemptSubmitting, ledger.AttemptVerifying, ledger.AttemptRunning:
		return NotRunPending
	}
	if rw.phase == ledger.PhaseVerify {
		has := rw.expected
		if cm.manifestChanged && cm.manifest != nil {
			has = slices.ContainsFunc(cm.manifest.Verify, func(ch Check) bool { return ch.ID == rw.id })
		}
		if !has {
			return NotRunNoSuchStep
		}
	}
	for _, row := range receipts {
		phase := cmpPhase(row.Phase)
		if (phase == ledger.PhaseCheck || phase == ledger.PhaseVerify) && row.State == ledger.CheckFinished &&
			(row.TimedOut || row.Exit != 0 && !row.Killed) {
			return NotRunEarlier
		}
	}
	switch sh.attempt.State {
	case ledger.AttemptSucceeded, "":
		// It passed every check it ran: no reason is known.
		return ""
	}
	return NotRunEnded
}

// notRunWordsOf is a not-run cell's words, an ended attempt's with its
// state and the stage it ended at.
func notRunWordsOf(why string, a ledger.AttemptRow) string {
	if why != NotRunEnded {
		return notRunText(why)
	}
	if a.Stage != "" {
		return fmt.Sprintf("%s (%s, at %s)", notRunEndedText, a.State, cutEncoded(a.Stage, 80))
	}
	return fmt.Sprintf("%s (%s)", notRunEndedText, a.State)
}

func notRunText(why string) string {
	switch why {
	case NotRunNoSuchStep:
		return notRunNoStepText
	case NotRunPending:
		return notRunPendingText
	case NotRunEnded:
		return notRunEndedText
	case "":
		return notRunNoReceiptText
	}
	return notRunEarlierText
}

// checkoutManifest is the manifest identity of the checkout an attempt's
// checks ran in, "" for none.
func checkoutManifest(trees []ledger.TreeRow, task string, n int) string {
	for _, tr := range trees {
		if tr.TaskID == task && tr.N == n && tr.Kind == ledger.TreeCheckout {
			return tr.Manifest
		}
	}
	return ""
}

func shortID(id string) string {
	if id == "" {
		return "no commit"
	}
	return id[:min(len(id), 12)]
}

// argvPreview is an argv as a row shows it: its entries while they fit
// in maxArgvPreview as encoded, cut at an entry's end.
func argvPreview(argv []string) ([]string, bool) {
	out := []string{}
	size := 2
	for _, a := range argv {
		n := encodedLen(a) + 1
		if size+n > maxArgvPreview {
			return out, true
		}
		size += n
		out = append(out, a)
	}
	return out, false
}

// namedFiles are the files at the base the argv names: each entry that is
// a path relative to the crew's directory and inside it (at most
// maxArgvPaths of them), looked up in one git ls-tree, by their paths from
// the repository's top. Only files count: an entry naming a folder (".",
// "tests/", "src") names none of the files in it, and one above the
// crew's directory is passed over.
func namedFiles(ctx context.Context, git gitFunc, dir, base string, argv []string) ([]string, error) {
	args := []string{"ls-tree", "-z", "--full-name", base, "--"}
	n := 0
	for _, a := range argv {
		if n == maxArgvPaths {
			break
		}
		if a == "" || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") || strings.ContainsAny(a, "\x00\n") {
			continue
		}
		clean := path.Clean(a)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasSuffix(a, "/") {
			continue
		}
		args = append(args, ":(literal)"+clean)
		n++
	}
	if n == 0 {
		return []string{}, nil
	}
	out, err := git(ctx, dir, 64<<10, args...)
	if err != nil {
		return nil, err
	}
	named := []string{}
	for _, entry := range strings.Split(out, "\x00") {
		// <mode> <type> <object>\t<path>: a blob is a file (a link among
		// them), a tree a folder.
		meta, p, ok := strings.Cut(entry, "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[1] == "blob" {
			named = append(named, p)
		}
	}
	return named, nil
}

// encodedLen is s's length as encoding/json writes it, quotes left out.
func encodedLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runeEncodedLen(r, size)
		i += size
	}
	return n
}

// runeEncodedLen is one rune's length as encoding/json writes it (an
// invalid byte as �).
func runeEncodedLen(r rune, size int) int {
	switch {
	case r == utf8.RuneError && size == 1:
		return 6
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '\b' || r == '\f':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == ' ' || r == ' ':
		return 6
	}
	return size
}

// cutEncoded cuts s at a character so that encoded it takes at most n
// bytes, quotes left out.
func cutEncoded(s string, n int) string {
	size := 0
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		size += runeEncodedLen(r, w)
		if size > n {
			return s[:i]
		}
		i += w
	}
	return s
}

// readSelected is the applied selection of the chain's runs, read with
// the rest of the chain (an application is a transition of the run that
// holds it, so moved sees it); nil for none, and for a ledger that keeps
// no decisions.
func readSelected(ctx context.Context, l Ledger, chain []ledger.RunPointer) (*AppliedSelection, error) {
	dl, ok := l.(decisionLedger)
	if !ok {
		return nil, nil
	}
	for i := len(chain) - 1; i >= 0; i-- {
		rows, err := allDecisions(ctx, dl, ledger.DecisionFilter{CurrentRun: chain[i].ID, States: []string{ledger.DecisionApplied}})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if sel := selectionOf(row); sel != nil {
				return sel, nil
			}
		}
	}
	return nil, nil
}
