package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
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
	// treeSecretPart is what the scrub makes a part of a secret: no
	// longer than the shortest part it replaces (scrubRun).
	treeSecretPart = "[secret]"
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
// tree waits for one. The stop begins at Shutdown's entry (mark): a read
// that begins once it has begun makes no folder and no index; a read under
// way when it begins may still run its commands and write its own index
// until the stop ends it (close: its git ends with its context), and its
// label is unread. A server that does not stop leaves its folder in the
// temporary folder.
type treeReads struct {
	mu      sync.Mutex
	dir     string
	stopped bool
	order   []string
	entries map[string]*treeEntry
	// freed is closed, and made again, when an entry stops being
	// reserved, and at the stop.
	freed chan struct{}
	// reads cancels each read under way; running counts them.
	reads   map[*context.CancelFunc]bool
	running sync.WaitGroup
	// seam, when set (by a test), is called at "handed" (a read has its
	// entry and has not yet taken its gate), at "waiting" (a read waits
	// for room), at "closing" (the stop has cancelled the reads and
	// waits for them) and at "removing" (every read has returned and the
	// folder is about to go, under the reads' lock).
	seam func(at, dir string)
	// after, when set (by a test), stands in for time.After in the stop's
	// wait for the reads (labelStopWait).
	after func(time.Duration) <-chan time.Time
	// limit, when set (by a test), stands in for a read's time limit
	// (labelReadWait): the context the read runs in and its cancel, which
	// the stop calls.
	limit func(context.Context) (context.Context, context.CancelFunc)
}

// labelStopWait bounds the stop's wait for the reads it cancelled. Each
// read's git ends with its context, so the bound only bounds a hang.
const labelStopWait = time.Minute

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
		// waits for goes, its index, the index's lock and its objects
		// with it.
		for i := 0; e == nil && i < len(tr.order) && len(tr.order) >= maxTreeIndexes; {
			if old := tr.entries[tr.order[i]]; old.reserved == 0 {
				old.ix.Remove()
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

// mark marks the stop, under the reads' lock: Shutdown calls it at its
// entry, before any wait. A read whose commands end after the mark is
// unread (stopping), and no later read makes anything.
func (tr *treeReads) mark() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.stopped = true
}

// close ends the reads at the stop: the stop is marked (if it is not
// already) and every read under way is cancelled, its git ending with its
// context, and waited for within labelStopWait. Once each of them has
// returned, the folder of indexes is removed. When one has not, the stop
// returns without removing it (left as a server that does not stop leaves
// it), and log says so.
func (tr *treeReads) close(log *slog.Logger) {
	tr.mu.Lock()
	tr.stopped = true
	for cancel := range tr.reads {
		(*cancel)()
	}
	tr.mu.Unlock()
	if tr.seam != nil {
		tr.seam("closing", "")
	}
	returned := make(chan struct{})
	go func() {
		tr.running.Wait()
		close(returned)
	}()
	after := time.After
	if tr.after != nil {
		after = tr.after
	}
	select {
	case <-returned:
	case <-after(labelStopWait):
		log.Warn("the preview labels' folder was left at the stop: a read had not returned", "why", "late")
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.dir != "" {
		if tr.seam != nil {
			tr.seam("removing", tr.dir)
		}
		_ = os.RemoveAll(tr.dir)
		close(tr.freed)
		tr.dir, tr.order, tr.entries, tr.freed = "", nil, nil, nil
	}
}

// begin counts a read under way, cancelled by cancel at the stop; false
// once the stop has begun. end, given back, ends the count.
func (tr *treeReads) begin(cancel context.CancelFunc) (end func(), ok bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.stopped {
		return nil, false
	}
	if tr.reads == nil {
		tr.reads = map[*context.CancelFunc]bool{}
	}
	key := &cancel
	tr.reads[key] = true
	tr.running.Add(1)
	return func() {
		tr.mu.Lock()
		delete(tr.reads, key)
		tr.mu.Unlock()
		tr.running.Done()
	}, true
}

// stopping reports whether the stop has begun, under the reads' lock: a
// read asks it after its last git command, and a read it answers true for
// is unread, its index perhaps removed while it ran.
func (tr *treeReads) stopping() bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.stopped
}

// read reads the worktree of wp now (crew.TreeRevision) in its entry,
// opened as the engine made it (crew.OpenTree: a tree that cannot be
// reached so is not read), and then, when step is given, runs step in the
// same tree, index, gate and time (a capture's sources, at the commit the
// read recorded). A read that ends past labelReadWait, fails after naming
// HEAD (step's error among it), or ends once the stop has begun
// (errReadsStopped, whatever else ended it) gives the commit alone and the
// error. A capture's step whose time runs out inside it (its look for the
// repository's top, the classes' git) does not fail the read: the content
// and the changed paths that were read are kept, the hints' classes are
// not known (and the repository's top not named, when the time ran out in
// its look), and the capture's log says so (captureFacts).
func (tr *treeReads) read(ctx context.Context, wp crew.WorktreePreview, step func(ctx context.Context, t *crew.Tree, ix *crew.TreeIndex, commit string) error) (crew.TreeRead, error) {
	limit := func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, labelReadWait)
	}
	if tr.limit != nil {
		limit = tr.limit
	}
	ctx, cancel := limit(ctx)
	defer cancel()
	end, ok := tr.begin(cancel)
	if !ok {
		return crew.TreeRead{}, errReadsStopped
	}
	defer end()
	e, err := tr.take(ctx, wp.Dir)
	if err != nil && tr.stopping() {
		err = errReadsStopped
	}
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer tr.give(e, true)
	t, err := crew.OpenTree(wp.Root, wp.Dir)
	if err != nil {
		return crew.TreeRead{}, err
	}
	defer t.Close()
	read, err := crew.TreeRevision(ctx, t, e.ix)
	if err == nil && step != nil {
		err = step(ctx, t, e.ix, read.Commit)
	}
	if err == nil && !t.Still() {
		err = crew.ErrTreeMoved
	}
	if tr.stopping() {
		err = errReadsStopped
	}
	if err != nil {
		return crew.TreeRead{Commit: read.Commit}, err
	}
	return read, nil
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
	read, err := s.trees.read(r.Context(), wp, nil)
	// The tree's secret is in the URL alone: the changed paths, the only
	// names the label carries beside git's ids and its own words, are
	// scrubbed of the secret of every tree the engine holds one for, and
	// then cut to their bound.
	paths := storedPaths(read.Paths)
	scrubSecrets(&paths, s.runs.TreeSecrets())
	cut := labelPaths(paths)
	l := previewLabel{Tree: dispatch.TreeWorktree, Commit: read.Commit, Content: read.Content, Changed: read.Changed, ChangedPaths: cut,
		ChangedCut: len(cut) < read.Changed, Mutable: true, State: "ready", Where: cmpOr(wp.Where, "native"), ReadAt: time.Now().UTC()}
	if err != nil {
		s.log.Warn("the preview label's tree was not read", "run", runID, "task", taskID, "attempt", n, "why", readFailure(err))
		l.Unread = "the tree could not be read"
		if errors.Is(err, context.DeadlineExceeded) {
			l.Unread = "the tree was not read in time"
		}
	}
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
	case errors.Is(err, gitcli.ErrNotRepo):
		return "not a repository"
	case errors.Is(err, crew.ErrTreeMoved), errors.Is(err, errReadsStopped), errors.Is(err, gitcli.ErrNoObjects):
		return "refused"
	}
	return "git failed"
}

// scrubSecrets replaces, in every string v (a pointer) holds however
// deep, each stretch of hexadecimal characters made of runs of scrubRun
// that are parts of one of secrets, in any case: a stretch that holds a
// whole secret with the secrets' name (treeSecretName), any other (a part
// of one at least scrubRun long, wherever a bound cut it) with
// treeSecretPart. Neither is longer than what it replaces, so no text and
// no list of texts is longer, as sent or as encoded, once scrubbed. A
// secret is hex, so its URL-encoded form is itself, and neither word holds
// a run of hex that could make one again. Each string is read once
// (secretStretches).
func scrubSecrets(v any, secrets []string) {
	if set := secretsOf(secrets); len(set.parts) > 0 {
		eachText(reflect.ValueOf(v), func(s string) string { return scrubText(s, set) })
	}
}

// scrubRun is the shortest part of a secret the scrub replaces.
const scrubRun = 8

// secretSet is what the scrub looks for: every run of scrubRun
// hexadecimal characters of each secret, each the number its digits make,
// and each secret whole, in lower case.
type secretSet struct {
	parts map[uint32]bool
	whole []string
}

// secretsOf is the scrub's set of secrets.
func secretsOf(secrets []string) secretSet {
	set := secretSet{parts: map[uint32]bool{}}
	for _, s := range secrets {
		secretStretches(s, nil, func(n uint32, _, _ int) { set.parts[n] = true })
		set.whole = append(set.whole, strings.ToLower(s))
	}
	return set
}

// wordFor is what the stretch of parts of a secret is made: the secrets'
// name when it holds a whole one and is no shorter than the name, else
// treeSecretPart.
func (set secretSet) wordFor(stretch string) string {
	if len(stretch) >= len(treeSecretName) {
		low := strings.ToLower(stretch)
		if slices.ContainsFunc(set.whole, func(w string) bool { return w != "" && strings.Contains(low, w) }) {
			return treeSecretName
		}
	}
	return treeSecretPart
}

// secretStretches reads s once, keeping the number the last scrubRun
// hexadecimal characters make (none past any other byte). Without parts,
// it calls f with each such run; with parts, with each stretch of s made
// of runs among parts (the runs that overlap or touch joined), in order,
// from its start to its end.
func secretStretches(s string, parts map[uint32]bool, f func(n uint32, start, end int)) {
	var n uint32
	run, start, end := 0, -1, -1
	for i := 0; i < len(s); i++ {
		c := s[i] | 0x20
		switch {
		case '0' <= s[i] && s[i] <= '9':
			n = n<<4 | uint32(s[i]-'0')
		case 'a' <= c && c <= 'f':
			n = n<<4 | uint32(c-'a'+10)
		default:
			run = 0
			continue
		}
		if run++; run < scrubRun {
			continue
		}
		at := i + 1 - scrubRun
		switch {
		case parts == nil:
			f(n, at, i+1)
		case !parts[n]:
		case start >= 0 && at <= end:
			end = i + 1
		default:
			if start >= 0 {
				f(0, start, end)
			}
			start, end = at, i+1
		}
	}
	if start >= 0 {
		f(0, start, end)
	}
}

// scrubText is s with each stretch of parts of a secret (secretStretches)
// made its word (wordFor), the one place the scrub replaces a stretch.
func scrubText(s string, set secretSet) string {
	var b strings.Builder
	from := 0
	secretStretches(s, set.parts, func(_ uint32, start, end int) {
		b.WriteString(s[from:start])
		b.WriteString(set.wordFor(s[start:end]))
		from = end
	})
	if from == 0 && b.Len() == 0 {
		return s
	}
	b.WriteString(s[from:])
	return b.String()
}

// holdsSecret is whether s holds a part of one of secrets as the scrub
// finds one.
func holdsSecret(s string, secrets []string) bool {
	held := false
	if set := secretsOf(secrets); len(set.parts) > 0 {
		secretStretches(s, set.parts, func(uint32, int, int) { held = true })
	}
	return held
}

// labelPaths is the first of paths within dispatch.MaxChangedPaths and
// dispatch.MaxChangedPathsBytes as encoded, as a capture keeps them; the
// label cuts its paths once they are scrubbed.
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
