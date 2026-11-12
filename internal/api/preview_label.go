package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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

// treeReads reads the trees of the previews, each in an index of the
// server's own for that tree, kept for the next read of it, one read of a
// tree at a time (its entry's gate), the wait for it and the read together
// within labelReadWait. At most maxTreeIndexes are kept: an entry a read
// holds or waits for is reserved, and the least recently read of the
// others is let go to make room; with none to let go, a read of another
// tree waits for one. Once the server's stop has begun (close), a read
// makes nothing.
type treeReads struct {
	mu      sync.Mutex
	dir     string
	stopped bool
	order   []string
	entries map[string]*treeEntry
	// freed is closed, and made again, when an entry stops being
	// reserved, and at the stop.
	freed chan struct{}
	// seam, when set (by a test), is called at "handed" (a read has its
	// entry and has not yet taken its gate) and at "waiting" (a read waits
	// for room).
	seam func(at, dir string)
}

// treeEntry is a tree's kept index, its gate (held by sending to it) and
// how many reads hold or wait for it.
type treeEntry struct {
	ix       *crew.TreeIndex
	gate     chan struct{}
	reserved int
}

// errReadsStopped is a read that began once the server's stop had begun.
var errReadsStopped = errors.New("the server is stopping")

// take hands out the entry of the tree at dir, reserved under the reads'
// lock, made under the reads' own temporary directory when it is new, and
// then takes its gate, all within ctx. give lets it go.
func (tr *treeReads) take(ctx context.Context, dir string) (*treeEntry, error) {
	var e *treeEntry
	for e == nil {
		tr.mu.Lock()
		if tr.stopped {
			tr.mu.Unlock()
			return nil, errReadsStopped
		}
		if tr.dir == "" {
			d, err := os.MkdirTemp("", "conductor-labels-")
			if err != nil {
				tr.mu.Unlock()
				return nil, err
			}
			tr.dir, tr.entries, tr.freed = d, map[string]*treeEntry{}, make(chan struct{})
		}
		e = tr.entries[dir]
		// Past the bound, the least recently read entry no read holds or
		// waits for goes, its index and the index's lock with it.
		for i := 0; e == nil && i < len(tr.order) && len(tr.order) >= maxTreeIndexes; {
			if old := tr.entries[tr.order[i]]; old.reserved == 0 {
				_, _ = os.Remove(old.ix.Path), os.Remove(old.ix.Path+".lock")
				delete(tr.entries, tr.order[i])
				tr.order = slices.Delete(tr.order, i, i+1)
			} else {
				i++
			}
		}
		if e == nil && len(tr.order) < maxTreeIndexes {
			sum := sha256.Sum256([]byte(dir))
			e = &treeEntry{ix: &crew.TreeIndex{Path: filepath.Join(tr.dir, hex.EncodeToString(sum[:16])+".index")}, gate: make(chan struct{}, 1)}
			tr.entries[dir] = e
		}
		if e != nil {
			e.reserved++
			tr.order = append(slices.DeleteFunc(tr.order, func(d string) bool { return d == dir }), dir)
			tr.mu.Unlock()
			continue
		}
		freed := tr.freed
		tr.mu.Unlock()
		if tr.seam != nil {
			tr.seam("waiting", dir)
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if tr.seam != nil {
		tr.seam("handed", dir)
	}
	select {
	case e.gate <- struct{}{}:
		return e, nil
	case <-ctx.Done():
		tr.give(e, false)
		return nil, ctx.Err()
	}
}

// give lets the entry e go: its gate when the read held it, and its
// reservation.
func (tr *treeReads) give(e *treeEntry, held bool) {
	if held {
		<-e.gate
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if e.reserved--; e.reserved == 0 && tr.freed != nil {
		close(tr.freed)
		tr.freed = make(chan struct{})
	}
}

// close begins the stop: the reads' indexes are removed, a read waiting
// for room is let go, and no later read makes anything.
func (tr *treeReads) close() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.stopped = true
	if tr.dir != "" {
		_ = os.RemoveAll(tr.dir)
		close(tr.freed)
		tr.dir, tr.order, tr.entries, tr.freed = "", nil, nil, nil
	}
}

// read reads the worktree of wp now (crew.TreeRevision) in its entry,
// opened as the engine made it (crew.OpenTree: a tree that cannot be
// reached so, or is not a worktree of the run's repository, is not read).
// A read that ends past labelReadWait or fails after naming HEAD gives the
// commit with no content and the error.
func (tr *treeReads) read(ctx context.Context, wp crew.WorktreePreview) (crew.TreeRead, error) {
	ctx, cancel := context.WithTimeout(ctx, labelReadWait)
	defer cancel()
	e, err := tr.take(ctx, wp.Dir)
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer tr.give(e, true)
	t, err := crew.OpenTree(ctx, wp.Root, wp.Dir)
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer t.Close()
	read, err := crew.TreeRevision(ctx, t, e.ix)
	if err == nil && !t.Still() {
		err = crew.ErrTreeMoved
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
	read, err := s.trees.read(r.Context(), wp)
	cut := labelPaths(storedPaths(read.Paths))
	l := previewLabel{Tree: dispatch.TreeWorktree, Commit: read.Commit, Content: read.Content, Changed: read.Changed, ChangedPaths: cut,
		ChangedCut: len(cut) < read.Changed, Mutable: true, State: "ready", Where: cmpOr(wp.Where, "native"), ReadAt: time.Now().UTC()}
	if err != nil {
		s.log.Warn("the preview label's tree was not read", "run", runID, "task", taskID, "attempt", n, "why", readFailure(err))
		l.Unread = "the tree could not be read"
		if errors.Is(err, context.DeadlineExceeded) {
			l.Unread = "the tree was not read in time"
		}
	}
	// The tree's secret is in the URL alone: every other field is
	// scrubbed of the secret of every tree the engine holds one for.
	scrubSecrets(&l, s.runs.TreeSecrets())
	l.URL = labelURL(wp.URL, wp.Secret, principalAdmin(r))
	writeDispatch(w, http.StatusOK, l)
}

// readFailure is the kind of a tree's read that failed, in fixed words for
// the log, never git's own: late, not a repository, refused or git
// failed.
func readFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "late"
	case errors.Is(err, crew.ErrTreeNotWorktree), errors.Is(err, gitcli.ErrNotRepo):
		return "not a repository"
	case errors.Is(err, crew.ErrTreeMoved), errors.Is(err, errReadsStopped):
		return "refused"
	}
	return "git failed"
}

// scrubSecrets replaces every occurrence of each of secrets, in any
// case, with its name (treeSecretName) in every string v (a pointer)
// holds, however deep. A secret is hex, so its URL-encoded form is
// itself, and the name holds no run of hex that could make it again.
func scrubSecrets(v any, secrets []string) {
	for _, s := range secrets {
		if s != "" {
			scrubValue(reflect.ValueOf(v), asciiLower(s))
		}
	}
}

func scrubValue(v reflect.Value, secret string) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(scrubText(v.String(), secret))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			scrubValue(v.Elem(), secret)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			scrubValue(v.Field(i), secret)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			scrubValue(v.Index(i), secret)
		}
	}
}

// scrubText is s with every occurrence of secret, compared without regard
// to ASCII case, made treeSecretName.
func scrubText(s, secret string) string {
	low := asciiLower(s)
	var b strings.Builder
	for {
		i := strings.Index(low, secret)
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString(treeSecretName)
		s, low = s[i+len(secret):], low[i+len(secret):]
	}
	b.WriteString(s)
	return b.String()
}

// asciiLower is s with its ASCII letters lower-cased, every byte in its
// place.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
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
