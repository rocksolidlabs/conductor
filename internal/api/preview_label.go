package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
	"github.com/rocksolidlabs/conductor/internal/session"
)

// The label of an attempt's preview over HTTP (Program item PV1,
// docs/plans/browser-evidence.md "The label"): the tree's revision read
// now, the admin's alone. The captures of a preview, which read the tree
// the same way, are a later row's.

const (
	// labelReadWait bounds a read of a tree.
	labelReadWait = 5 * time.Second
	// maxTreeIndexes bounds the trees whose read keeps its index.
	maxTreeIndexes = 8
	// treeSecretName is what a preview names its tree's secret by.
	treeSecretName = "${CONDUCTOR_TREE_SECRET}"
)

// routePreviewLabel adds the label's route, the admin's and a
// switchyard's 403.
func (s *Server) routePreviewLabel(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{run}/tasks/{task}/attempts/{n}/preview", s.requireAdmin(s.launchingAny(s.handlePreviewLabel)))
}

// storedPaths is the changed paths a label keeps: those the store's rule
// takes (UTF-8 with no control character), the rest left out and still
// counted.
func storedPaths(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		if utf8.ValidString(p) && !strings.ContainsFunc(p, unicode.IsControl) {
			out = append(out, p)
		}
	}
	return out
}

// treeReads reads the trees of the previews: each through its directory
// opened once (session.OpenFolder, an os.Root), git started at a path
// that names that open directory (gitcli.OpenedPath), the read refused
// when the directory's path came to name another meanwhile; in an index
// of the server's own for that tree, kept for the next read of it (at
// most maxTreeIndexes, the least recently read let go), one read of a
// tree at a time, the wait for it and the read together within
// labelReadWait.
type treeReads struct {
	mu    sync.Mutex
	dir   string
	order []string
	gates map[string]chan struct{}
}

// index takes the index of the tree at dir, made under the reads' own
// temporary directory: its path, and the gate that is its own (held by
// sending to it).
func (tr *treeReads) index(dir string) (string, chan struct{}, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.dir == "" {
		d, err := os.MkdirTemp("", "conductor-labels-")
		if err != nil {
			return "", nil, err
		}
		tr.dir, tr.gates = d, map[string]chan struct{}{}
	}
	name := func(dir string) string {
		sum := sha256.Sum256([]byte(dir))
		return filepath.Join(tr.dir, hex.EncodeToString(sum[:16])+".index")
	}
	if i := slices.Index(tr.order, dir); i >= 0 {
		tr.order = append(slices.Delete(tr.order, i, i+1), dir)
		return name(dir), tr.gates[dir], nil
	}
	// Past the bound, the least recently read index no read holds goes.
	for i := 0; i < len(tr.order) && len(tr.order) >= maxTreeIndexes; {
		old := tr.order[i]
		select {
		case tr.gates[old] <- struct{}{}:
			_ = os.Remove(name(old))
			delete(tr.gates, old)
			tr.order = slices.Delete(tr.order, i, i+1)
		default:
			i++
		}
	}
	tr.order, tr.gates[dir] = append(tr.order, dir), make(chan struct{}, 1)
	return name(dir), tr.gates[dir], nil
}

// close removes the reads' indexes.
func (tr *treeReads) close() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.dir != "" {
		_ = os.RemoveAll(tr.dir)
		tr.dir, tr.order, tr.gates = "", nil, nil
	}
}

// read reads the tree at dir now (crew.TreeRevision). The tree's
// directory is opened through its parent's (session.OpenFolderIn), so a
// link put in its place leads nowhere outside that parent. A read that
// ends past labelReadWait or fails after naming HEAD gives the commit
// with no content and the error.
func (tr *treeReads) read(ctx context.Context, dir string) (crew.TreeRead, error) {
	ctx, cancel := context.WithTimeout(ctx, labelReadWait)
	defer cancel()
	folder, err := session.OpenFolderIn(filepath.Dir(dir), filepath.Base(dir), nil)
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer folder.Close()
	d, err := folder.Dir()
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer d.Close()
	at, still := gitcli.OpenedPath(d, dir)
	index, gate, err := tr.index(dir)
	if err != nil {
		return crew.TreeRead{}, err
	}
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return crew.TreeRead{}, ctx.Err()
	}
	defer func() { <-gate }()
	read, err := crew.TreeRevision(ctx, at, index)
	if err == nil && !still() {
		err = errors.New("the tree's directory moved while it was read")
	}
	if err != nil {
		read.Content = ""
	}
	return read, err
}

// previewLabel is an attempt's preview label: the URL to open (the
// admin's, the tree's secret in it), the tree and its revision as read
// now (unread says why content is empty when it is), whether it is
// mutable, its state, where it runs and when it was read.
type previewLabel struct {
	URL          string    `json:"url"`
	Tree         string    `json:"tree"`
	Commit       string    `json:"commit"`
	Content      string    `json:"content"`
	Changed      int       `json:"changed"`
	ChangedPaths []string  `json:"changedPaths"`
	ChangedCut   bool      `json:"changedCut"`
	Mutable      bool      `json:"mutable"`
	State        string    `json:"state"`
	Where        string    `json:"where"`
	ReadAt       time.Time `json:"readAt"`
	Unread       string    `json:"unread,omitempty"`
}

// labelURL is the preview as a caller opens it: for the admin the tree's
// secret put in for its name, for any other the fragment, where a
// preview names it, dropped.
func labelURL(preview, secret string, admin bool) string {
	if admin {
		return strings.ReplaceAll(preview, treeSecretName, secret)
	}
	u, _, _ := strings.Cut(preview, "#")
	return u
}

// handlePreviewLabel answers the label of an attempt's worktree preview:
// 200 previewLabel, its revision read now (crew.TreeRevision, within 5 s:
// past it content is "" and unread says so); 404 no_preview when the
// attempt's preview is not open. It takes no parameter (a candidate's
// checkout, ?seq=, is a later row's).
func (s *Server) handlePreviewLabel(w http.ResponseWriter, r *http.Request) {
	runID, taskID := r.PathValue("run"), r.PathValue("task")
	n, ok := pathNumber(r.PathValue("n"))
	if !crew.ValidRunID(runID) || !crew.ValidTaskID(taskID) || !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "a run id, a task id and an attempt number")
		return
	}
	if !noQuery(w, r) {
		return
	}
	var wp crew.WorktreePreview
	if s.runs != nil {
		wp, ok = s.runs.WorktreePreview(runID, taskID, n)
	}
	if s.runs == nil || !ok {
		writeError(w, http.StatusNotFound, "no_preview", "the attempt's preview is not open now")
		return
	}
	read, err := s.trees.read(r.Context(), wp.Dir)
	paths := storedPaths(read.Paths)
	cut := labelPaths(paths)
	l := previewLabel{URL: labelURL(wp.URL, wp.Secret, principalAdmin(r)), Tree: dispatch.TreeWorktree, Commit: read.Commit, Content: read.Content,
		Changed: read.Changed, ChangedPaths: cut, ChangedCut: len(cut) < read.Changed, Mutable: true, State: "ready", Where: cmpOr(wp.Where, "native"),
		ReadAt: time.Now().UTC()}
	if err != nil {
		l.Unread = "the tree could not be read"
		if errors.Is(err, context.DeadlineExceeded) {
			l.Unread = "the tree was not read in time"
		}
	}
	writeDispatch(w, http.StatusOK, l)
}

// labelPaths is the first of paths within dispatch.MaxChangedPaths and
// dispatch.MaxChangedPathsBytes as encoded, as a capture keeps them.
func labelPaths(paths []string) []string {
	out, size := []string{}, len("[]")
	for _, p := range paths {
		n := proto.PreviewMeasure(p)
		if len(out) > 0 {
			n++
		}
		if len(out) == dispatch.MaxChangedPaths || size+n > dispatch.MaxChangedPathsBytes {
			break
		}
		out, size = append(out, p), size+n
	}
	return out
}

// principalAdmin reports whether r's principal is the admin.
func principalAdmin(r *http.Request) bool {
	p, ok := r.Context().Value(principalKey{}).(principal)
	return ok && p.admin
}
