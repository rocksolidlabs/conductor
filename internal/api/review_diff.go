package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/crew/ledger"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/gitrepo"
)

// A candidate's diff (Program item RV1, docs/plans/review-repair.md,
// PR-3): the files a managed run's candidate (an attempt's result commit)
// changed against a base, and each side of one file, for the Review view.
// Every commit is one the ledger names for the run (its base, the
// attempt's start, a result commit of the same task in the run's chain),
// read by its object id from the run's repository with go-git: no
// revision is resolved, no worktree is opened and no file on disk is read,
// so nothing of a session's folder, and nothing of a commit the run never
// named, is readable through them. A path asked for is a path inside the
// commit's tree, refused before git sees it when it could name anything
// else (a '..', a leading '/', a NUL).

const (
	// MaxDiffReply bounds the diff route's reply: at most gitrepo.MaxFiles
	// files, fewer when the next would pass it (truncated says so), as
	// writeJSON encodes them. Exported for the CLI.
	MaxDiffReply = 256 << 10
	// maxDiffPath bounds a path asked of the file route, in bytes (git's
	// own PATH_MAX).
	maxDiffPath = 4096
	// commitHeader names the commit a file was read at, truncatedHeader
	// says it was cut at gitcli.MaxShowBytes.
	commitHeader    = "X-Conductor-Commit"
	truncatedHeader = "X-Conductor-Truncated"
)

// diffFile is one file of a candidate's diff: its path (a rename's new
// one, from the old), its status (A, M, D, R), its added and removed
// lines, and whether it is binary or too large to count (no lines then).
type diffFile struct {
	Path    string `json:"path"`
	From    string `json:"from,omitempty"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

// diffTarget is what a diff route names, read from the ledger: the run's
// row, the attempt with its result commit, the base (the one asked, else
// the run's), and the run's repository as resolved now.
type diffTarget struct {
	run     ledger.RunRow
	attempt ledger.AttemptRow
	base    string
	repo    string
}

// fullCommit reports whether s is a commit id in full: 40 lowercase hex
// digits, as the ledger keeps one and go-git reads one.
func fullCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	return strings.IndexFunc(s, func(c rune) bool { return (c < '0' || c > '9') && (c < 'a' || c > 'f') }) < 0
}

// diffTargetOf reads the target of a diff route (its run, task and
// attempt in the path, ?base= and the route's other parameters in q),
// having answered the request when it reports false: 400 invalid_request
// for an id or a parameter that is not one, and for a base the run never
// named (allowedBase); 404 not_found for a run or an attempt the ledger
// has not, and for an attempt with no result commit; 409 repo_unavailable
// for a repository the server cannot read now (its folder gone, outside
// the allowed roots, no repository); 503 ledger_unavailable, and
// run_changing while a continuation of the chain is being made.
func (s *Server) diffTargetOf(w http.ResponseWriter, r *http.Request, q dispatchQuery) (diffTarget, bool) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	n, okN := pathNumber(r.PathValue("n"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !okN {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id and an attempt number")
		return diffTarget{}, false
	}
	base, err := q.one("base")
	if err == nil && base != "" && !fullCommit(base) {
		err = errors.New("base is a commit id in full (40 hex digits)")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return diffTarget{}, false
	}
	ld := s.receiptLedger(w)
	if ld == nil {
		return diffTarget{}, false
	}
	ctx := r.Context()
	row, found, err := ld.Run(ctx, runID)
	if err != nil {
		s.receiptError(w, "diff: the run", runID, err)
		return diffTarget{}, false
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such run")
		return diffTarget{}, false
	}
	attempts, err := ld.Attempts(ctx, runID)
	if err != nil {
		s.receiptError(w, "diff: the attempts", runID, err)
		return diffTarget{}, false
	}
	i := slices.IndexFunc(attempts, func(a ledger.AttemptRow) bool { return a.TaskID == taskID && a.N == n })
	if i < 0 {
		writeError(w, http.StatusNotFound, "not_found", "no such attempt")
		return diffTarget{}, false
	}
	a := attempts[i]
	if a.ResultCommit == "" {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("attempt %d of %s has no result commit", n, taskID))
		return diffTarget{}, false
	}
	if base == "" {
		base = row.Base
	} else if ok, err := s.allowedBase(ctx, row, a, base); err != nil {
		s.chainReadError(w, runID, err)
		return diffTarget{}, false
	} else if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "the base is none the run names: its base, the attempt's start, or a result of the task in the run's chain")
		return diffTarget{}, false
	}
	repo, ok := s.runRepo(w, ctx, row)
	if !ok {
		return diffTarget{}, false
	}
	return diffTarget{run: row, attempt: a, base: base, repo: repo}, true
}

// allowedBase reports whether base is a base the candidate a of the run
// row may be compared with: the run's base, the attempt's start, or a
// result commit of the same task in any run of the run's chain.
func (s *Server) allowedBase(ctx context.Context, row ledger.RunRow, a ledger.AttemptRow, base string) (bool, error) {
	if base == row.Base || base == a.StartCommit {
		return true, nil
	}
	nodes, _, _, err := s.runChain(ctx, row.ID)
	if err != nil {
		return false, err
	}
	for _, nd := range nodes {
		attempts, err := s.ledger.Attempts(ctx, nd.id)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(attempts, func(o ledger.AttemptRow) bool { return o.TaskID == a.TaskID && o.ResultCommit == base }) {
			return true, nil
		}
	}
	return false, nil
}

// chainReadError answers a read of a run's chain that failed: 503
// run_changing (Retry-After 1) while a continuation of it is being made,
// else as a ledger read's error.
func (s *Server) chainReadError(w http.ResponseWriter, runID string, err error) {
	if errors.Is(err, errRunContinuing) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "run_changing", "a run of the chain is being continued; ask again")
		return
	}
	s.receiptError(w, "the run's chain", runID, err)
}

// runRepo is the run's repository: the crew's working directory as the
// run was launched with it, through resolveCwd (still under an allowed
// root) and crew.CheckRepo, as a launch's proposal resolves it; having
// answered 409 repo_unavailable, or the read's error, when it reports
// false.
func (s *Server) runRepo(w http.ResponseWriter, ctx context.Context, row ledger.RunRow) (string, bool) {
	var c crew.Crew
	if err := json.Unmarshal([]byte(row.Crew), &c); err != nil {
		s.receiptError(w, "the run's crew", row.ID, err)
		return "", false
	}
	dir, err := s.resolveCwd(c.Cwd)
	if err == nil {
		err = crew.CheckRepo(ctx, dir)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "repo_unavailable", "the run's repository cannot be read here: "+err.Error())
		return "", false
	}
	return dir, true
}

// gitReadError answers a read of the run's repository that failed: 410
// commit_gone for a commit no longer in it, 409 repo_unavailable for a
// folder that is no repository now, 500 git_failed otherwise (logged
// without the request's query).
func (s *Server) gitReadError(w http.ResponseWriter, runID string, err error) {
	switch {
	case errors.Is(err, gitrepo.ErrNoCommit):
		writeError(w, http.StatusGone, "commit_gone", "a commit of the diff is no longer in the repository: "+strings.TrimPrefix(err.Error(), gitrepo.ErrNoCommit.Error()+": "))
	case errors.Is(err, gitrepo.ErrNotRepo):
		writeError(w, http.StatusConflict, "repo_unavailable", "the run's repository cannot be read here: "+err.Error())
	default:
		s.log.Warn("a candidate's diff could not be read", "run", runID, "err", err)
		writeError(w, http.StatusInternalServerError, "git_failed", "the repository could not be read")
	}
}

// handleCandidateDiff answers GET /api/runs/{run}/tasks/{task}/attempts/
// {n}/diff[?base=COMMIT]: 200 {candidate, base, files, truncated}, the
// files that differ between the base (the run's unless given) and the
// attempt's result commit, at most gitrepo.MaxFiles and fewer when the
// next would pass MaxDiffReply (truncated); 410 commit_gone for a commit
// no longer in the repository; the rest as diffTargetOf answers.
func (s *Server) handleCandidateDiff(w http.ResponseWriter, r *http.Request) {
	q, err := readQuery(r, "base")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	dt, ok := s.diffTargetOf(w, r, q)
	if !ok {
		return
	}
	d, err := gitrepo.Compare(r.Context(), dt.repo, dt.base, dt.attempt.ResultCommit)
	if err != nil {
		s.gitReadError(w, dt.run.ID, err)
		return
	}
	files, truncated := fitDiff(dt.attempt.ResultCommit, dt.base, d)
	writeJSON(w, http.StatusOK, diffReply(dt.attempt.ResultCommit, dt.base, files, truncated))
}

// diffReply is the diff route's reply.
func diffReply(candidate, base string, files []diffFile, truncated bool) map[string]any {
	return map[string]any{"candidate": candidate, "base": base, "files": files, "truncated": truncated}
}

// fitDiff is d's files as the reply lists them: in d's order, each
// counted as writeJSON encodes it, until the next would take the reply
// past MaxDiffReply; truncated when any was left out, or d was.
func fitDiff(candidate, base string, d gitrepo.Detail) ([]diffFile, bool) {
	files := make([]diffFile, 0, len(d.Changes))
	empty, _ := json.Marshal(diffReply(candidate, base, files, true))
	size := len(empty) + 1 // the encoder's newline
	for _, c := range d.Changes {
		f := diffFile{Path: c.Path, From: c.From, Status: c.Status, Added: c.Added, Removed: c.Removed, Binary: c.Binary}
		b, _ := json.Marshal(f)
		if size+len(b)+1 > MaxDiffReply {
			return files, true
		}
		size += len(b) + 1
		files = append(files, f)
	}
	return files, d.Truncated
}

// validTreePath reports whether p names a path inside a commit's tree as
// git keeps one: relative, with '/', no empty, '.' or '..' part, no NUL,
// at most maxDiffPath bytes.
func validTreePath(p string) bool {
	if p == "" || len(p) > maxDiffPath || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	parts := strings.Split(p, "/")
	return !slices.Contains(parts, "..") && !slices.Contains(parts, ".")
}

// handleCandidateDiffFile answers GET /api/runs/{run}/tasks/{task}/
// attempts/{n}/diff/file?path=P&side=base|candidate[&base=COMMIT]: the
// file's bytes at that side's commit (the base, as the diff route takes
// it, or the attempt's result), text/plain, at most gitcli.MaxShowBytes,
// X-Conductor-Truncated: 1 past it, X-Conductor-Commit the commit read;
// 400 invalid_request for a path that could name anything outside the
// commit's tree, or a side that is neither; 404 not_found for a path the
// commit has no file at (an added file's base side, a deleted file's
// candidate side, a folder); 415 binary for a file git calls binary; the
// rest as the diff route answers.
func (s *Server) handleCandidateDiffFile(w http.ResponseWriter, r *http.Request) {
	q, err := readQuery(r, "path", "side", "base")
	var p, side string
	if err == nil {
		p, err = q.one("path")
	}
	if err == nil {
		side, err = q.one("side")
	}
	switch {
	case err != nil:
	case !validTreePath(p):
		err = errors.New("path is a file's path in the commit's tree: relative, with no '..', '.' or empty part, no NUL, at most 4096 bytes")
	case side != "base" && side != "candidate":
		err = errors.New("side is base or candidate")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	dt, ok := s.diffTargetOf(w, r, q)
	if !ok {
		return
	}
	commit := dt.attempt.ResultCommit
	if side == "base" {
		commit = dt.base
	}
	f, err := gitrepo.FileAt(r.Context(), dt.repo, commit, p, gitcli.MaxShowBytes)
	switch {
	case errors.Is(err, gitrepo.ErrNoFile):
		writeError(w, http.StatusNotFound, "not_found", "the "+side+"'s commit has no such file")
		return
	case err != nil:
		s.gitReadError(w, dt.run.ID, err)
		return
	case f.Binary:
		writeError(w, http.StatusUnsupportedMediaType, "binary", "the file is binary")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set(commitHeader, commit)
	if f.Truncated {
		h.Set(truncatedHeader, "1")
	}
	h.Set("Content-Length", strconv.Itoa(len(f.Content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.Content)
}
