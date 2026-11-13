package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/gitrepo"
)

// Where a finding's lines are on another candidate (Program item RV1,
// docs/plans/review-repair.md, PR-4, "Located"): the file at that
// candidate's commit, its path followed through a rename between the
// finding's anchor and that commit, searched for the lines as the person
// saw them, the way the Files tab's comment card finds a quote's lines in
// the file as it is now (web/app/utils/quote.ts, relocate, ported here and
// tested on its fixtures). It is words for a person and a record kept with
// a mark: no located state changes a finding's state; only a mark does.

// The located states.
const (
	locatedSame     = "same"
	locatedMoved    = "moved"
	locatedChanged  = "changed"
	locatedFileGone = "file_gone"
	locatedRenamed  = "renamed"
	locatedUnknown  = "unknown"
)

const (
	// maxLocateFiles bounds the distinct files one reply reads to locate
	// findings: the findings page reads at most this many, a finding past
	// it located unknown, with why.
	maxLocateFiles = 50
	// maxQuoteLine is the bytes of a line a finding keeps
	// (dispatch.MaxAnchorLine, the F7 quote's MAX_QUOTE_LINE).
	maxQuoteLine = dispatch.MaxAnchorLine
)

// locatedState is where a finding's lines are on a candidate: same, moved
// (from, to: the nearest place they are now), changed, file_gone, renamed
// (path, and located, where in the renamed file), or unknown (why: the
// commit gone, the file binary or past 1 MiB, more than maxLocateFiles
// files asked in one reply). A file finding is same, renamed or
// file_gone.
type locatedState struct {
	State   string        `json:"state"`
	From    int           `json:"from,omitempty"`
	To      int           `json:"to,omitempty"`
	Path    string        `json:"path,omitempty"`
	Located *locatedState `json:"located,omitempty"`
	Why     string        `json:"why,omitempty"`
}

// cutLine is s cut to at most n bytes at a character's start, as the
// quote's cutBytes cuts it.
func cutLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// relocate is utils/quote.ts's relocate: where the lines want, quoted
// from lines from..to, are in fileLines: same where they were, else moved
// to the nearest place they are, either way from where they were, the
// range as long as it was, else changed.
func relocate(want []string, from, to int, fileLines []string) locatedState {
	if len(want) == 0 {
		return locatedState{State: locatedSame}
	}
	span := to - from + 1
	at := func(start int) bool {
		if start < 0 {
			return false
		}
		for i, l := range want {
			line := "\x00"
			if start+i < len(fileLines) {
				line = fileLines[start+i]
			}
			if cutLine(line, maxQuoteLine) != l {
				return false
			}
		}
		return true
	}
	if at(from - 1) {
		return locatedState{State: locatedSame}
	}
	best := -1
	dist := func(i int) int {
		d := i - (from - 1)
		if d < 0 {
			return -d
		}
		return d
	}
	for i := 0; i+len(want) <= len(fileLines); i++ {
		if at(i) && (best < 0 || dist(i) < dist(best)) {
			best = i
		}
	}
	if best < 0 {
		return locatedState{State: locatedChanged}
	}
	return locatedState{State: locatedMoved, From: best + 1, To: best + span}
}

// locator locates findings on candidates within one reply: the run's
// repository, each file read once, each commit pair compared once, at most
// maxLocateFiles distinct files read.
type locator struct {
	s     *Server
	repos map[string]locatorRepo
	files map[[2]string]locatorFile
	diffs map[[3]string]gitrepo.Detail
}

type locatorRepo struct {
	dir string
	err error
}

type locatorFile struct {
	f   gitrepo.File
	err error
}

func (s *Server) newLocator() *locator {
	return &locator{s: s, repos: map[string]locatorRepo{}, files: map[[2]string]locatorFile{}, diffs: map[[3]string]gitrepo.Detail{}}
}

// repoOf is the run's repository as the diff routes read it (runRepo):
// the crew's working directory as launched, through resolveCwd and
// crew.CheckRepo; an error when it cannot be read here.
func (l *locator) repoOf(ctx context.Context, run string) (string, error) {
	if r, ok := l.repos[run]; ok {
		return r.dir, r.err
	}
	var out locatorRepo
	switch row, found, err := l.s.ledger.Run(ctx, run); {
	case err != nil:
		out.err = err
	case !found:
		out.err = errors.New("the ledger has no such run")
	default:
		var c crew.Crew
		if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
			out.err = err
			break
		}
		out.dir, out.err = l.s.resolveCwd(c.Cwd)
		if out.err == nil {
			out.err = crew.CheckRepo(ctx, out.dir)
		}
	}
	l.repos[run] = out
	return out.dir, out.err
}

// fileAt reads path at commit, once per reply; errTooManyFiles past
// maxLocateFiles distinct files.
func (l *locator) fileAt(ctx context.Context, repo, commit, path string) (gitrepo.File, error) {
	k := [2]string{commit, path}
	if f, ok := l.files[k]; ok {
		return f.f, f.err
	}
	if len(l.files) >= maxLocateFiles {
		return gitrepo.File{}, errTooManyFiles
	}
	f, err := gitrepo.FileAt(ctx, repo, commit, path, gitcli.MaxShowBytes)
	l.files[k] = locatorFile{f, err}
	return f, err
}

var errTooManyFiles = errors.New("too many files")

// The locator's words for a file whose move is not known (renamedTo).
const (
	whyFilesCut   = "whether it moved is not known: more files changed than one comparison lists"
	whyRenamesCut = "whether it moved is not known: renames were not looked for or not told apart here"
)

// renamedTo is path's new path between the commits from and to, as
// Compare finds renames ("" when it was not renamed), and, when it was not
// found renamed, why whether it moved is not known: "" when Compare listed
// every file and its renames were not cut; whyFilesCut when the comparison
// was cut at its file bound (gitrepo.Detail.Truncated: the file may be
// past the files listed); otherwise whyRenamesCut when its renames were cut
// (gitrepo.Detail.RenamesCut: not looked for, or not told apart).
func (l *locator) renamedTo(ctx context.Context, repo, from, to, path string) (string, string, error) {
	k := [3]string{repo, from, to}
	d, ok := l.diffs[k]
	if !ok {
		var err error
		if d, err = gitrepo.Compare(ctx, repo, from, to); err != nil {
			return "", "", err
		}
		l.diffs[k] = d
	}
	for _, c := range d.Changes {
		if c.Status == "R" && c.From == path {
			return c.Path, "", nil
		}
	}
	switch {
	case d.Truncated:
		return "", whyFilesCut, nil
	case d.RenamesCut:
		return "", whyRenamesCut, nil
	}
	return "", "", nil
}

// unknownOf is the unknown state of a read that failed, in words.
func unknownOf(err error) *locatedState {
	why := "the file could not be read"
	switch {
	case errors.Is(err, errTooManyFiles):
		why = "more findings' files than one reply reads: ask for fewer"
	case errors.Is(err, gitrepo.ErrNoCommit):
		why = "the commit is no longer in the repository"
	case errors.Is(err, gitrepo.ErrNotRepo):
		why = "the run's repository cannot be read here"
	}
	return &locatedState{State: locatedUnknown, Why: why}
}

// locate is where the finding f's lines (or its file) are on the
// candidate c of the run's repository: nil for a finding with neither (a
// general or a check finding). The finding's own candidate and side read
// as they were: same.
func (l *locator) locate(ctx context.Context, f dispatch.Finding, c dispatch.Candidate) *locatedState {
	if f.Kind != dispatch.FindingKindLine && f.Kind != dispatch.FindingKindFile {
		return nil
	}
	if f.AnchorCommit == c.Commit {
		return &locatedState{State: locatedSame}
	}
	if !validTreePath(f.Path) {
		// Refused before git sees it, as the diff routes refuse one.
		return &locatedState{State: locatedUnknown, Why: "the path is none a commit's tree holds"}
	}
	repo, err := l.repoOf(ctx, c.Run)
	if err != nil {
		return &locatedState{State: locatedUnknown, Why: "the run's repository cannot be read here"}
	}
	path := f.Path
	file, err := l.fileAt(ctx, repo, c.Commit, path)
	renamed := ""
	if errors.Is(err, gitrepo.ErrNoFile) {
		to, why, rerr := l.renamedTo(ctx, repo, f.AnchorCommit, c.Commit, path)
		switch {
		case rerr != nil:
			return unknownOf(rerr)
		case to == "" && why != "":
			return &locatedState{State: locatedUnknown, Why: why}
		case to == "":
			return &locatedState{State: locatedFileGone}
		case len(to) > dispatch.MaxFindingPath:
			return &locatedState{State: locatedUnknown, Why: "renamed to a path too long to say"}
		}
		renamed = to
		file, err = l.fileAt(ctx, repo, c.Commit, to)
	}
	switch {
	case err != nil:
		return unknownOf(err)
	case file.Binary:
		return &locatedState{State: locatedUnknown, Why: "the file is binary"}
	case file.Truncated:
		return &locatedState{State: locatedUnknown, Why: "the file is more than 1 MiB"}
	}
	in := locatedState{State: locatedSame}
	if f.Kind == dispatch.FindingKindLine {
		in = relocate(f.Lines, f.From, f.To, fileLines(file.Content))
	}
	if renamed != "" {
		return &locatedState{State: locatedRenamed, Path: renamed, Located: &in}
	}
	return &in
}

// fileLines is a file's content as lines, each without its terminator:
// "\n", or "\r\n" (a finding's lines hold no "\r", which the store
// refuses, so a CRLF file's lines are compared without it).
func fileLines(content []byte) []string {
	lines := strings.Split(string(content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// locatedJSON is a located state as a mark keeps it, encoded with HTML
// escaping off (a path's "<" one byte, not six): nil for none. A state
// that still encodes past dispatch.MaxMarkLocated (a renamed path of
// control characters or U+2028s, six bytes each as JSON writes them) is
// kept as unknown, saying why, never dropped.
func locatedJSON(st *locatedState) json.RawMessage {
	if st == nil {
		return nil
	}
	encode := func(st *locatedState) []byte {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(st) != nil {
			return nil
		}
		return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	}
	if b := encode(st); b != nil && len(b) <= dispatch.MaxMarkLocated {
		return b
	}
	return encode(&locatedState{State: locatedUnknown, Why: "where the lines are is longer than a mark keeps: the renamed path, as JSON writes it, is past the bound"})
}

// newestResult is the newest result commit of the task in the chain
// forward from run: the highest-numbered attempt with a result commit in
// any run of it, as a candidate; ok false for none.
func (s *Server) newestResult(ctx context.Context, nodes []chainNode, run, task string) (dispatch.Candidate, bool, error) {
	var best ledger.AttemptRow
	for _, id := range chainForward(nodes, run) {
		attempts, err := s.ledger.Attempts(ctx, id)
		if err != nil {
			return dispatch.Candidate{}, false, err
		}
		for _, a := range attempts {
			if a.TaskID == task && a.ResultCommit != "" && a.N >= best.N {
				best = a
			}
		}
	}
	if best.ResultCommit == "" {
		return dispatch.Candidate{}, false, nil
	}
	return dispatch.Candidate{Run: best.RunID, Task: task, Attempt: best.N, Commit: best.ResultCommit}, true, nil
}
