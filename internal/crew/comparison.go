package crew

import (
	"context"
	"errors"
)

// MaxComparison bounds a comparison's reply as encoded.
const MaxComparison = 1 << 20

// maxCompareChain is how many runs of one chain a comparison walks: a
// comparison is continued at most maxCompareChain-1 times.
const maxCompareChain = 256

// ErrRunChanging is the answer when the chain moved through every read of
// its comparison: the route answers 503 run_changing and asks again.
var ErrRunChanging = errors.New("the run changed through every read of its comparison; ask again")

// errComparisonUnmeasured is the foundation's answer until the measure is
// built.
var errComparisonUnmeasured = errors.New("a comparison is not measured yet")

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
// it added, removed or moved.
type DepChanges struct {
	Files   []string       `json:"files"`
	Modules []ModuleChange `json:"modules"`
	Total   int            `json:"total"`
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

// SettledCandidate is one lane's shown candidate when every lane ended.
type SettledCandidate struct {
	Task    string `json:"task"`
	Run     string `json:"run"`
	Attempt int    `json:"attempt"`
	Commit  string `json:"commit"`
}

// Comparison measures the comparison of the chain runID belongs to.
func (e *Engine) Comparison(ctx context.Context, runID string) (Comparison, error) {
	return Comparison{}, errComparisonUnmeasured
}
