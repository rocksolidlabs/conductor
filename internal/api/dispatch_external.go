package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/tracker"
)

// A ticket's GitHub item (docs/plans/tracker-linking.md, PR-4): the
// preview, the import with its images, the link and the unlink, the
// refresh, the field owners, a link's read, the read after a link's add
// and the close's reads for merged. Every request to GitHub is made in one
// place, atGitHub: the credential from credentialFor, then the adapter's
// call with it, which alone opens it; no handler here builds a request, a
// header or an address. The token reaches only its own host's API, in one
// header, through the adapter's transport (internal/tracker), whose every
// request is made on a connection of its own. Nothing GitHub answers is
// logged: a line names the route, the item's reference and the answer's
// status. A refusal is in fixed words (trackerAnswer), naming an item only
// in the form its parse accepted. What an item gives (its title, body,
// labels, people and images) is content from outside: it goes into
// Dispatch through the store's own operations and bounds, read within the
// adapter's bounds, and nothing is made of it (no command, path, address
// or template): an image is fetched only by the adapter, from the hosts
// its rule admits, under the address the read named.

// The bounds of a route that reads GitHub: each route's deadline is the
// sum of its steps' caps (trackerCaps).
const (
	// TrackerReadTimeout bounds the preview, a link, an unlink, a field
	// given back and a link's read: the lock, the reads, one write.
	TrackerReadTimeout = trackerBusyWait + trackerReadsBudget + dispatchWriteTimeout
	// ImportTimeout bounds an import and a refresh: the lock, the reads,
	// the ticket's write, the images (their downloads and their commits).
	ImportTimeout = trackerBusyWait + trackerReadsBudget + dispatchWriteTimeout + trackerImagesBudget
	// MergedCloseTimeout bounds a ticket's edit to done when its project
	// requires merged: the links' locks and their reads, at most
	// maxCloseReads at once, in one phase; the reads' records, each
	// within a write's cap of its own; the edit.
	MergedCloseTimeout = trackerBusyWait + mergedReadsBudget + dispatchWriteTimeout + dispatchWriteTimeout

	// trackerBusyWait bounds a wait for a ticket's or a link's lock (the
	// plan's trackerLockWait, a name trackers.go's file lock holds).
	trackerBusyWait = 15 * time.Second
	// trackerReadsBudget bounds a phase's requests to GitHub (an item's
	// two, a refresh's second read, a link's checks), each still within
	// the adapter's own RequestTimeout.
	trackerReadsBudget = 45 * time.Second
	// trackerImagesBudget bounds an import's or a refresh's images: their
	// downloads (each within the adapter's ImageTimeout) and commits.
	trackerImagesBudget = 60 * time.Second
	// mergedReadsBudget bounds the close's reads of its delivery links.
	mergedReadsBudget = 30 * time.Second
	// maxCloseReads is the close's reads made at once.
	maxCloseReads = 4
)

// trackerCaps is the caps of a route's steps; the product's are
// defaultTrackerCaps, a test's scaled down through externalState.caps.
type trackerCaps struct{ busy, reads, images, merged, write time.Duration }

var defaultTrackerCaps = trackerCaps{busy: trackerBusyWait, reads: trackerReadsBudget, images: trackerImagesBudget, merged: mergedReadsBudget, write: dispatchWriteTimeout}

// externalState is the server's part of this file: the locks of a
// ticket's and a link's reads, held from the request to GitHub to the
// commit (never a database transaction across the call), and the tests'
// seams (caps, cut, waiting), unset in the product.
type externalState struct {
	mu   sync.Mutex
	held map[string]chan struct{}
	caps *trackerCaps
	// cut, when set, is asked after an import's ticket ("ticket") and after
	// each image's commit ("image"): true ends the route there, as a crash
	// would, so a resend is what completes it.
	cut func(at string) bool
	// waiting, when set, is told of a lock found held, before its wait.
	waiting func(key string)
	// stop ends as the server begins to stop (end): every request this
	// file makes to GitHub is made under it, and none begins after it.
	stop    context.Context
	stopped context.CancelFunc
}

// stopping is the context that ends as the server begins to stop.
func (x *externalState) stopping() context.Context {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.stop == nil {
		x.stop, x.stopped = context.WithCancel(context.Background())
	}
	return x.stop
}

// end ends stopping's context, at WaitBackground and Shutdown, where the
// server marks its own work done.
func (x *externalState) end() {
	x.stopping()
	x.mu.Lock()
	end := x.stopped
	x.mu.Unlock()
	end()
}

func (s *Server) trackerCaps() trackerCaps {
	if c := s.external.caps; c != nil {
		return *c
	}
	return defaultTrackerCaps
}

// lock takes the lock named key within wait: false when another holds it
// past wait or ctx ends; the func lets it go.
func (x *externalState) lock(ctx context.Context, key string, wait time.Duration) (func(), bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		x.mu.Lock()
		if x.held == nil {
			x.held = map[string]chan struct{}{}
		}
		ch, busy := x.held[key]
		if !busy {
			ch = make(chan struct{})
			x.held[key] = ch
			x.mu.Unlock()
			return func() {
				x.mu.Lock()
				delete(x.held, key)
				x.mu.Unlock()
				close(ch)
			}, true
		}
		x.mu.Unlock()
		if x.waiting != nil {
			x.waiting(key)
		}
		select {
		case <-ch:
		case <-timer.C:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

// errItemBusy answers a lock not taken within its wait: nothing read or
// written.
var errItemBusy = newAPIError(http.StatusServiceUnavailable, "tracker_busy", "another read of this item is under way: try again")

// lockFor takes key's lock within the caps' wait, answering 503
// tracker_busy (Retry-After) when it could not: false once it answered.
func (s *Server) lockFor(ctx context.Context, w http.ResponseWriter, key string) (func(), bool) {
	release, ok := s.external.lock(ctx, key, s.trackerCaps().busy)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, errItemBusy)
	}
	return release, ok
}

// atGitHub is the one door of this file to GitHub: once the server has
// begun to stop, none (errItemBusy, nothing asked); else the credential
// for ref (credentialFor, 412 no_token without one), then fn with the
// adapter, which alone opens it and makes every request.
func (s *Server) atGitHub(ctx context.Context, ref tracker.Ref, fn func(tracker.Adapter, tracker.Credential) error) error {
	if s.external.stopping().Err() != nil {
		return errItemBusy
	}
	c, err := s.credentialFor(ctx, ref)
	if err != nil {
		return err
	}
	return fn(s.trackers.gh, c)
}

// readItem reads ref's item with the validators v (atGitHub).
func (s *Server) readItem(ctx context.Context, ref tracker.Ref, v tracker.Validators) (it tracker.Item, notModified bool, err error) {
	err = s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		var e error
		it, notModified, e = ad.Read(ctx, c, ref, v)
		return e
	})
	return it, notModified, err
}

// errNotAnItem answers what no adapter parses, quoting nothing of it.
var errNotAnItem = newAPIError(http.StatusBadRequest, "not_a_tracker_url", "not a GitHub issue or pull request this server reads: https://github.com/<owner>/<repo>/issues/<n> or …/pull/<n> (or a GitHub Enterprise Server's host with a saved token), owner/repo#n, or #n in the project's repository")

// parseItem reads raw as an item's reference, the repository of a short
// form space's (a repository's URL or a git remote).
func (s *Server) parseItem(raw, space string) (tracker.Ref, *apiError) {
	ref, err := s.trackers.gh.Parse(raw, space)
	if err != nil {
		return tracker.Ref{}, errNotAnItem
	}
	return ref, nil
}

// errNoTracker answers #n in a project with no tracker address, and
// errNoDefault a short form in one whose tracker address is set and is no
// repository this server reads; neither quotes the address.
var (
	errNoTracker = newAPIError(http.StatusBadRequest, "not_a_tracker_url", "#n needs the project's tracker address, and the project has none: give the item's whole address (https://github.com/<owner>/<repo>/issues/<n>), or set the project's tracker address")
	errNoDefault = newAPIError(http.StatusBadRequest, "not_a_tracker_url", "the project's tracker address is not a GitHub repository this server reads: give the item's whole address (https://github.com/<owner>/<repo>/issues/<n>), or set the project's tracker address")
)

// projectItem reads raw as an item's reference in project p, for the
// preview, the import and the link alike. A whole address is read as it
// is. A short form's default repository is the project's tracker address
// and nothing else (the project's folder is never read): with none, there
// is no default (#n is errNoTracker, owner/repo#n read on github.com); one
// naming a repository on a host the parser admits is the default; one set
// and unusable (in no form the parser reads, or on a host it does not
// admit) refuses every short form (errNoDefault), none read on another
// host.
func (s *Server) projectItem(raw string, p dispatch.Project) (tracker.Ref, *apiError) {
	switch {
	case strings.Contains(raw, "://"):
		return s.parseItem(raw, "")
	case p.TrackerURL == "":
		if space, _, ok := strings.Cut(strings.TrimSpace(raw), "#"); ok && space == "" {
			return tracker.Ref{}, errNoTracker
		}
		return s.parseItem(raw, "")
	}
	// The address read as #n's repository: the parser refuses one it does
	// not read or whose host it does not admit.
	if _, err := s.trackers.gh.Parse("#1", p.TrackerURL); err != nil {
		return tracker.Ref{}, errNoDefault
	}
	return s.parseItem(raw, p.TrackerURL)
}

// snapshotRef is the item a ticket's snapshot names, and its validators'
// node id.
func snapshotRef(raw []byte) (tracker.Ref, string, bool) {
	sn, ok := dispatch.ParseSnapshot(string(raw))
	return tracker.Ref{System: sn.System, Host: sn.Host, Space: sn.Space, Kind: sn.Kind, ID: sn.ID}, sn.NodeID, ok
}

// etag is a validator as the store keeps one, or none: printable ASCII,
// at most dispatch.MaxValidator bytes, with no address or query in it.
func etag(v string) string {
	if len(v) > dispatch.MaxValidator || strings.ContainsAny(v, "?") || strings.Contains(v, "://") || strings.ContainsFunc(v, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return ""
	}
	return v
}

// externalOf is a read as the store's operations take it: the snapshot
// with the body's images as the manifest's ids and its steps to reproduce,
// the body, the validators. Every text GitHub wrote is made storable here
// (dispatch.Storable: the title, the labels, the assignees, the author,
// the milestone and the base branch as one line each, the body as lines);
// the rest is of a fixed form the adapter made (the place, the ids, the
// state, the times, the commits) or the store admits (the page, the
// images' ids). The store cuts and digests it.
func externalOf(it tracker.Item) dispatch.ExternalRead {
	line := func(s string, n int) string { return dispatch.Storable(s, n, false) }
	lines := func(l []string, n int) []string {
		out := make([]string, 0, len(l))
		for _, s := range l {
			out = append(out, line(s, n))
		}
		return out
	}
	body := dispatch.Storable(it.Body, tracker.MaxBody, true)
	repro, _ := tracker.Repro(body)
	sn := dispatch.ExternalSnapshot{V: dispatch.SnapshotVersion, System: it.Ref.System, Host: it.Ref.Host, Space: it.Ref.Space, Kind: it.Ref.Kind, ID: it.Ref.ID,
		NodeID: it.NodeID, DatabaseID: string(it.DatabaseID), URL: it.URL, Title: line(it.Title, dispatch.MaxSnapshotTitle), BodyTruncated: it.BodyTruncated,
		State: it.State, StateReason: it.StateReason, Merged: it.Merged, MergedAt: it.MergedAt, MergeSha: it.MergeSHA, HeadSha: it.HeadSHA,
		BaseRef: line(it.BaseRef, dispatch.MaxSnapshotName), Labels: lines(it.Labels, dispatch.MaxSnapshotLabel), LabelsTotal: max(it.LabelsTotal, len(it.Labels)),
		Assignees: lines(it.Assignees, dispatch.MaxSnapshotLogin), Milestone: line(it.Milestone, dispatch.MaxSnapshotName), Author: line(it.Author, dispatch.MaxSnapshotLogin),
		AuthorID: string(it.AuthorID), CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, ClosedAt: it.ClosedAt, Repro: repro}
	for _, im := range it.Images {
		sn.Images = append(sn.Images, dispatch.SnapshotImage{ID: im.ID})
	}
	return dispatch.ExternalRead{Snapshot: sn, Body: body, IssueETag: etag(it.Validators.Issue), PullETag: etag(it.Validators.Pull)}
}

// checkWord is a check's status or conclusion as a read keeps it: the
// system's word (lower-case letters and '_', at most 40), "" for none, and
// "other" for anything else, which the store counts as other.
func checkWord(s string) string {
	if s == "" || len(s) <= 40 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz_") == "" {
		return s
	}
	return "other"
}

// trackerAnswer is a GitHub path's failure as its answer, in fixed words
// (never a word GitHub sent, a header or an address), naming the item only
// as its parse accepted it: an *apiError (credentialFor's: 412 no_token,
// 400 invalid_host) as it is; 404 external_not_found; 422 token_refused
// for a token GitHub refuses (never 401, which the app reads as its own
// token refused); 403 token_permission; 429 rate_limited; 409 moved for an
// item that is another than the one held, or a repository's name that now
// names another; 502 tracker_bound past an answer's bound; 502
// tracker_unavailable for the rest (a network failure, a timeout, a 5xx,
// an answer the adapter cannot read, the request's end).
func trackerAnswer(err error, ref tracker.Ref) *apiError {
	var ae *apiError
	var rl *tracker.ErrRateLimited
	var mv *tracker.ErrMoved
	item := "the item " + ref.String()
	switch {
	case errors.As(err, &ae):
		return ae
	case errors.As(err, &rl):
		return newAPIError(http.StatusTooManyRequests, "rate_limited", "GitHub is limiting this token's requests: try again later")
	case errors.As(err, &mv):
		return newAPIError(http.StatusConflict, "moved", item+" is now another item, "+mv.To.String()+": unlink it and link that one")
	case errors.Is(err, tracker.ErrTargetChanged):
		return newAPIError(http.StatusConflict, "moved", "the repository of "+item+" now names another repository")
	case errors.Is(err, tracker.ErrNotFound):
		return newAPIError(http.StatusNotFound, "external_not_found", "GitHub has no "+item+", or the token cannot see it")
	case errors.Is(err, tracker.ErrUnauthorized):
		return newAPIError(http.StatusUnprocessableEntity, "token_refused", "GitHub refused the token saved for "+ref.Host+": save a new one in Settings")
	case errors.Is(err, tracker.ErrForbidden):
		return newAPIError(http.StatusForbidden, "token_permission", "the token saved for "+ref.Host+" lacks a permission this read needs")
	case errors.Is(err, tracker.ErrBound):
		return newAPIError(http.StatusBadGateway, "tracker_bound", "GitHub's answer for "+item+" was past its bound")
	}
	return newAPIError(http.StatusBadGateway, "tracker_unavailable", "GitHub could not be reached for "+item+", or its answer could not be read")
}

// trackerFail answers a GitHub path's failure (trackerAnswer), Retry-After
// beside a rate limit, and logs it: the route, the reference, the status.
func (s *Server) trackerFail(w http.ResponseWriter, r *http.Request, ref tracker.Ref, err error) {
	ae := trackerAnswer(err, ref)
	if s.external.stopping().Err() != nil {
		// The server's stop ended it: a busy route's answer.
		ae = errItemBusy
		w.Header().Set("Retry-After", "5")
	}
	var rl *tracker.ErrRateLimited
	if errors.As(err, &rl) {
		wait := 60
		if !rl.Reset.IsZero() {
			wait = max(1, int(time.Until(rl.Reset).Seconds()+0.999))
		}
		w.Header().Set("Retry-After", strconv.Itoa(wait))
	}
	s.trackerLog(r, ref, ae.status)
	writeAPIError(w, ae)
}

// trackerLog is a GitHub path's one line: the route, the reference (as its
// parse accepted it), the status answered.
func (s *Server) trackerLog(r *http.Request, ref tracker.Ref, status int) {
	s.log.Info("tracker read", "route", r.Pattern, "ref", ref.Key(), "status", status)
}

// trackerContext is a route's context: the request's values, not its end
// (a client gone mid-way leaves the operation whole, its resend answered
// by the receipt), bounded by its deadline, and made of the server's stop
// (externalState.stopping), so it has ended already when the stop began
// before it.
func (s *Server) trackerContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(stopValues{s.external.stopping(), r.Context()}, d)
}

// stopValues is the server's stop with a request's values.
type stopValues struct {
	context.Context
	values context.Context
}

func (c stopValues) Value(key any) any { return c.values.Value(key) }

// The bodies of this file's writes.
type (
	externalPreviewBody struct {
		URL     string `json:"url"`
		Project string `json:"project,omitempty"`
	}
	externalImportBody struct {
		dispatchOp
		URL       string   `json:"url"`
		Status    string   `json:"status,omitempty"`
		Priority  int      `json:"priority,omitempty"`
		Horizon   string   `json:"horizon,omitempty"`
		Owner     string   `json:"owner,omitempty"`
		Assignees []string `json:"assignees,omitempty"`
		Labels    []string `json:"labels,omitempty"`
		Milestone string   `json:"milestone,omitempty"`
		// Images false leaves the manifest's images pending.
		Images *bool `json:"images,omitempty"`
	}
	externalLinkBody struct {
		dispatchOp
		URL string `json:"url"`
	}
	externalRefreshBody struct {
		dispatchOp
		Images *bool `json:"images,omitempty"`
	}
	externalAuthorityBody struct {
		dispatchOp
		Field string `json:"field"`
		Owner string `json:"owner"`
	}
	linkReadBody struct{}
)

// decodeExternal decodes a body of this file's routes into v, at most
// maxBody, first of all (before the path's key and id are read): 413
// bound past it, 400 invalid_request in the decoder's words; op, when
// given, is checked as readOp checks it. False once it answered.
func decodeExternal(w http.ResponseWriter, r *http.Request, v any, op *dispatchOp) bool {
	if op != nil {
		return readOp(w, r, v, op, maxBody)
	}
	if err := decodeJSONLimit(w, r, v, maxBody); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d KiB", maxBody>>10))
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		}
		return false
	}
	return true
}

// externalStart begins one of this file's writes: its body decoded first
// (decodeExternal), the store for a write, the path's key (keyOf); false
// once it answered.
func (s *Server) externalStart(w http.ResponseWriter, r *http.Request, v any, op *dispatchOp, keyOf func(http.ResponseWriter, *http.Request) (string, bool)) (*dispatch.Store, string, bool) {
	if !decodeExternal(w, r, v, op) {
		return nil, "", false
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return nil, "", false
	}
	key, ok := keyOf(w, r)
	return ds, key, ok
}

// replayed answers an operation sent again from its receipt (Replay): true
// once it answered. A route asks it first, a shortcut, and again once it
// holds the ticket's lock, which a first send holds from its read of
// GitHub through its commit: that answer decides, so a resend that waited
// for its first send answers what the first did and asks GitHub nothing.
func (s *Server) replayed(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, op dispatch.Op, intent string, args any, key string) bool {
	rc, found, err := ds.Replay(ctx, op, intent, args)
	if err != nil || found {
		s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
		return true
	}
	return false
}

// externalPreview is what an import of an item would make, from the
// import's own projection (dispatch.Projection): the title and the
// description (the description's first maxPreviewDescription bytes and
// its length), the manifest's images by id, the steps to reproduce,
// GitHub's state, labels and assignees as the snapshot keeps them (shown,
// never mapped), the owners an import gives (the item's system for both),
// and the live ticket that has it as its linked issue already, if any.
type externalPreview struct {
	Ref               tracker.Ref     `json:"ref"`
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	DescriptionLength int             `json:"descriptionLength"`
	Images            []string        `json:"images"`
	Repro             string          `json:"repro,omitempty"`
	State             string          `json:"state"`
	StateReason       string          `json:"stateReason,omitempty"`
	Labels            []string        `json:"labels"`
	Assignees         []string        `json:"assignees"`
	Owners            dispatch.Owners `json:"owners"`
	Linked            string          `json:"linked,omitempty"`
}

// maxPreviewDescription bounds the description a preview shows.
const maxPreviewDescription = 2 << 10

// handleDispatchExternalPreview reads an item and answers what its import
// would make (externalPreview); it writes nothing, and a store that takes
// no import (read-only) answers it as an import's 503.
func (s *Server) handleDispatchExternalPreview(w http.ResponseWriter, r *http.Request) {
	var body externalPreviewBody
	if !decodeExternal(w, r, &body, nil) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	ctx, cancel := s.trackerContext(r, TrackerReadTimeout)
	defer cancel()
	var project dispatch.Project
	if body.Project != "" && !strings.Contains(body.URL, "://") {
		pv, err := ds.Project(ctx, body.Project)
		if err != nil {
			s.dispatchError(w, "project", err)
			return
		}
		project = pv.Project
	}
	ref, ae := s.projectItem(body.URL, project)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	rctx, rcancel := context.WithTimeout(ctx, s.trackerCaps().reads)
	it, _, err := s.readItem(rctx, ref, tracker.Validators{})
	rcancel()
	if err != nil {
		s.trackerFail(w, r, ref, err)
		return
	}
	// What the import keeps, made by the import's own projection.
	sn, title, description, err := dispatch.Projection(externalOf(it))
	if err != nil {
		s.refusal(w, err)
		return
	}
	p := externalPreview{Ref: it.Ref, URL: sn.URL, Title: title, DescriptionLength: len(description), Repro: sn.Repro, State: sn.State,
		StateReason: sn.StateReason, Labels: sn.Labels, Assignees: sn.Assignees, Owners: dispatch.Owners{Title: it.Ref.System, Description: it.Ref.System}, Images: []string{}}
	p.Description, _ = tracker.CutBytes(description, maxPreviewDescription)
	for _, im := range sn.Images {
		p.Images = append(p.Images, im.ID)
	}
	// The live ticket whose linked issue it is, looked up by the item's
	// identity as the store holds one item to one ticket.
	p.Linked, _ = ds.LinkedTicket(ctx, sn.System, sn.Host, sn.NodeID)
	s.trackerLog(r, ref, http.StatusOK)
	writeDispatch(w, http.StatusOK, map[string]any{"preview": p})
}

// imageState is a manifest's image as a route answers it: done with its
// attachment, skipped (type, size), or pending (neither).
type imageState struct {
	ID           string `json:"id"`
	AttachmentID string `json:"attachmentId,omitempty"`
	Skipped      string `json:"skipped,omitempty"`
	Pending      bool   `json:"pending,omitempty"`
}

// manifestOf is the ticket's import manifest as a route answers it.
func manifestOf(tk dispatch.Ticket) []imageState {
	out := []imageState{}
	sn, _ := dispatch.ParseSnapshot(string(tk.ExternalSnapshot))
	for _, im := range sn.Images {
		out = append(out, imageState{ID: im.ID, AttachmentID: im.AttachmentID, Skipped: im.Skipped, Pending: im.AttachmentID == "" && im.Skipped == ""})
	}
	return out
}

// imageExt names an image's attachment by its sniffed type.
var imageExt = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

// completeImages makes done what key's manifest says is pending, from the
// images a read found (images, called only when an entry is pending: the
// import's own read, a refresh's, or one more read), matched by image id
// and never by position, within ctx (the images' budget): each one
// downloaded by the adapter (atGitHub) from the address the read named, at
// most dispatch.MaxAttachmentSize, its type sniffed from its bytes, and
// made the ticket's attachment with its manifest entry in one operation
// (ticket.import_image under DerivedOpID(parent, "image", id, its bytes'
// SHA-256)), or its skip recorded (a type the store does not take, a size
// past the bound) so it is never fetched again. An entry the read does not
// name, or whose download fails, stays pending, for a resend or the next
// refresh; an image the budget cuts commits nothing. The manifest is the
// record of what is left to do, so a resend never repeats a done image.
// A resend (replay) completes only what its own operation left: nothing,
// read or attached, once the ticket was unlinked or linked to an item
// since that operation (dispatch.LinkedSince).
func (s *Server) completeImages(ctx context.Context, ds *dispatch.Store, parent dispatch.Op, key string, ref tracker.Ref, images func() ([]tracker.ImageRef, error), replay bool) {
	tv, err := ticketFor(ctx, ds, key)
	if err != nil {
		return
	}
	if replay {
		if since, err := ds.LinkedSince(ctx, parent, key); err != nil || since {
			return
		}
	}
	var pending []imageState
	for _, im := range manifestOf(tv.Ticket) {
		if im.Pending {
			pending = append(pending, im)
		}
	}
	if len(pending) == 0 {
		return
	}
	found, err := images()
	if err != nil {
		return
	}
	for _, entry := range pending {
		i := slices.IndexFunc(found, func(im tracker.ImageRef) bool { return im.ID == entry.ID })
		if i < 0 || ctx.Err() != nil {
			continue
		}
		data, up, ok := s.downloadImage(ctx, ref, found[i])
		if !ok {
			continue
		}
		digest := "skipped:" + up.Skipped
		if data != nil {
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
			n := slices.IndexFunc(manifestOf(tv.Ticket), func(im imageState) bool { return im.ID == entry.ID }) + 1
			up.Name, up.Body = "github-"+strconv.Itoa(n)+"."+imageExt[up.ContentType], bytes.NewReader(data)
		}
		op := dispatch.Op{Actor: parent.Actor, ID: dispatch.DerivedOpID(parent.ID, "image", entry.ID, digest), By: parent.By}
		if _, err := ds.ImportImage(ctx, op, key, entry.ID, up); err != nil {
			continue
		}
		if s.external.cut != nil && s.external.cut("image") {
			return
		}
	}
}

// downloadImage downloads one image (atGitHub, the adapter's Image): its
// bytes and sniffed type, or its skip (type, size) with no bytes; false
// for a download that failed, which stays pending.
func (s *Server) downloadImage(ctx context.Context, ref tracker.Ref, img tracker.ImageRef) ([]byte, dispatch.ImageUpload, bool) {
	var data []byte
	err := s.atGitHub(ctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		rc, _, err := ad.Image(ctx, c, img, dispatch.MaxAttachmentSize)
		if err != nil {
			return err
		}
		defer rc.Close()
		data, err = io.ReadAll(rc)
		return err
	})
	switch {
	case errors.Is(err, tracker.ErrBound):
		return nil, dispatch.ImageUpload{Skipped: dispatch.SkippedSize}, true
	case err != nil:
		return nil, dispatch.ImageUpload{}, false
	}
	ct := http.DetectContentType(data)
	if !slices.Contains(dispatch.ImageTypes, ct) {
		return nil, dispatch.ImageUpload{Skipped: dispatch.SkippedType}, true
	}
	return data, dispatch.ImageUpload{ContentType: ct}, true
}

// externalReply answers an operation of this file that is done: its
// receipt, the ticket after it, and the manifest's images; 201 for an
// import made now.
func (s *Server) externalReply(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, rc dispatch.Receipt, key string, created bool, extra map[string]any) {
	reply := map[string]any{"receipt": rc}
	for k, v := range extra {
		reply[k] = v
	}
	if tv, err := ds.Ticket(ctx, cmp.Or(key, rc.Key)); err == nil {
		reply["ticket"], reply["images"] = tv, manifestOf(tv.Ticket)
	}
	status := http.StatusOK
	if created && !rc.Repeat {
		status = http.StatusCreated
	}
	writeDispatch(w, status, reply)
}

// handleDispatchImport imports an item as a ticket of the project
// (ticket.import): body externalImportBody. It replays the operation
// first, before any request to GitHub. A first send reads the item once
// and runs the import, its snapshot holding the image manifest; then,
// within the images' budget, it completes the manifest (completeImages).
// A resend is answered from the replay and from the stored manifest, never
// from a receipt: only the entries without an attachment or a skip are
// fetched again, with fresh addresses from one read, by id; with GitHub
// unreachable it answers its receipt with those images pending. 201
// {receipt, ticket, images}; 200 for a resend; the store's refusals as
// writeOp answers them (409 external_linked naming the ticket); GitHub's
// failures as trackerAnswer says, nothing written.
func (s *Server) handleDispatchImport(w http.ResponseWriter, r *http.Request) {
	var body externalImportBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, projectKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+caps.write+caps.images)
	defer cancel()
	op := body.op(r)
	args := dispatch.ImportArgs{Project: key, URL: body.URL, Status: body.Status, Priority: body.Priority, Horizon: body.Horizon, Owner: body.Owner,
		Assignees: body.Assignees, Labels: body.Labels, Milestone: body.Milestone}
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	defer rcancel()
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentTicketImport, args)
	if err != nil || found && rc.Status != dispatch.StatusDone {
		s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, ""))
		return
	}
	var images func() ([]tracker.ImageRef, error)
	var ref tracker.Ref
	if found {
		// A resend: the item as the ticket's snapshot names it, read again
		// only for images still pending.
		tv, err := ds.Ticket(ctx, rc.Key)
		if err != nil {
			s.dispatchError(w, "ticket", err)
			return
		}
		var node string
		ref, node, _ = snapshotRef(tv.ExternalSnapshot)
		images = s.readImages(rctx, ref, node)
	} else {
		pv, err := ds.Project(ctx, key)
		if err != nil {
			s.dispatchError(w, "project", err)
			return
		}
		var ae *apiError
		if ref, ae = s.projectItem(body.URL, pv.Project); ae != nil {
			writeAPIError(w, ae)
			return
		}
		it, _, err := s.readItem(rctx, ref, tracker.Validators{})
		if err != nil {
			s.trackerFail(w, r, ref, err)
			return
		}
		wctx, wcancel := context.WithTimeout(ctx, caps.write)
		rc, err = ds.ImportTicket(wctx, op, args, externalOf(it))
		wcancel()
		if err != nil || rc.Status != dispatch.StatusDone {
			s.writeOp(w, "ticket", rc, err, true, "ticket", ticketAfter(ctx, ds, ""))
			return
		}
		if s.external.cut != nil && s.external.cut("ticket") {
			return
		}
		images = func() ([]tracker.ImageRef, error) { return it.Images, nil }
	}
	if body.Images == nil || *body.Images {
		if release, ok := s.external.lock(ctx, "ticket:"+rc.Key, caps.busy); ok {
			ictx, icancel := context.WithTimeout(ctx, caps.images)
			s.completeImages(ictx, ds, op, rc.Key, ref, images, found)
			icancel()
			release()
		}
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.externalReply(ctx, w, ds, rc, rc.Key, true, nil)
}

// linkedTicket reads key's ticket for an operation on its linked issue
// (ticketFor), answering a store's failure and a deleted ticket's 404:
// false once it answered.
func (s *Server) linkedTicket(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, key string) (dispatch.TicketView, bool) {
	tv, err := ticketFor(ctx, ds, key)
	if err != nil {
		s.refusal(w, err)
		return tv, false
	}
	return tv, true
}

// handleDispatchLinkExternal sets a ticket's linked issue to the item at
// url (ticket.link_external), every field staying Dispatch's: the
// operation replayed (replayed, again under the ticket's lock), then the
// item read and the operation run with the read. {receipt, ticket}; the
// store's refusals (409 external_linked, invalid for a ticket linked
// already).
func (s *Server) handleDispatchLinkExternal(w http.ResponseWriter, r *http.Request) {
	var body externalLinkBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+caps.write)
	defer cancel()
	op, args := body.op(r), dispatch.LinkArgs{Key: key, URL: body.URL}
	if s.replayed(ctx, w, ds, op, dispatch.IntentLinkExternal, args, key) {
		return
	}
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	ref, ae := s.projectItem(body.URL, pv.Project)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
	if s.replayed(ctx, w, ds, op, dispatch.IntentLinkExternal, args, key) {
		return
	}
	if _, ok := s.linkedTicket(ctx, w, ds, key); !ok {
		return
	}
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	it, _, err := s.readItem(rctx, ref, tracker.Validators{})
	rcancel()
	if err != nil {
		s.trackerFail(w, r, ref, err)
		return
	}
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	rc, err := ds.LinkExternal(wctx, op, args, externalOf(it))
	s.trackerLog(r, ref, http.StatusOK)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(wctx, ds, key))
}

// handleDispatchUnlinkExternal empties a ticket's linked issue
// (ticket.unlink_external) under the ticket's lock, so it never lands
// inside a refresh or a link of the same ticket. {receipt, ticket}.
func (s *Server) handleDispatchUnlinkExternal(w http.ResponseWriter, r *http.Request) {
	var body dispatchOp
	ds, key, ok := s.externalStart(w, r, &body, &body, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.write)
	defer cancel()
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
	rc, err := ds.UnlinkExternal(ctx, body.op(r), key)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
}

// handleDispatchRefresh reads a ticket's linked item again under the
// ticket's lock, with the validators its last read kept: an item that is
// another than the one held is 409 moved, its words kept as the read's
// failure; a 304, or a 200 that changes nothing (dispatch.RefreshNeeded),
// is recorded alone (RecordTicketRead: no change, no receipt); anything
// else runs ticket.refresh with the read. Then, whatever the read found,
// unless images is false, the manifest's pending entries are completed
// (completeImages) from this read's images, or after a 304 from one more
// read without validators, made only when an entry is pending. 200
// {changed, receipt?, ticket, images}, receipt only when ticket.refresh
// ran. The receipt is looked for under the ticket's lock: a resend of a
// refresh that changed the ticket completes the images its first send
// left pending (completeImages, one more read of the item, and its
// downloads, made only when an entry is pending, and none once the
// ticket was unlinked or linked since) and answers {changed: true,
// receipt, ticket, images}. A refresh that changed nothing keeps no
// receipt, so its resend is a new read.
func (s *Server) handleDispatchRefresh(w http.ResponseWriter, r *http.Request) {
	var body externalRefreshBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+caps.write+caps.images)
	defer cancel()
	op := body.op(r)
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
	// The receipt is looked for under the ticket's lock, which a first send
	// holds from its read of GitHub through its commit: a resend answers
	// what the first did, its pending images completed first.
	rc, found, err := ds.Replay(ctx, op, dispatch.IntentRefresh, dispatch.RefreshArgs{Key: key})
	if err != nil || found && rc.Status != dispatch.StatusDone {
		s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
		return
	}
	tv, ok := s.linkedTicket(ctx, w, ds, key)
	if !ok {
		return
	}
	ref, node, linked := snapshotRef(tv.ExternalSnapshot)
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	defer rcancel()
	if found {
		if linked && (body.Images == nil || *body.Images) {
			ictx, icancel := context.WithTimeout(ctx, caps.images)
			s.completeImages(ictx, ds, op, key, ref, s.readImages(rctx, ref, node), true)
			icancel()
		}
		s.refreshReply(ctx, w, ds, key, map[string]any{"changed": true, "receipt": rc})
		return
	}
	if tv.ExternalID == "" || !linked {
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: key + " has no linked issue"}})
		return
	}
	v := tracker.Validators{NodeID: node}
	if er := tv.ExternalRead; er != nil && er.ExternalID == tv.ExternalID && er.Error == "" {
		v.Issue, v.Pull = er.IssueETag, er.PullETag
	}
	it, notModified, err := s.readItem(rctx, ref, v)
	if err != nil {
		// GitHub's failure is kept as the read's words; no token, or a host
		// out of form, asked GitHub nothing and keeps nothing.
		if ae := (*apiError)(nil); !errors.As(err, &ae) {
			_ = ds.RecordTicketReadError(ctx, key, tv.ExternalID, trackerAnswer(err, ref).Message)
		}
		s.trackerFail(w, r, ref, err)
		return
	}
	extra := map[string]any{"changed": false}
	read := dispatch.TicketRead{ReadBy: cmp.Or(op.By, op.Actor), IssueETag: v.Issue, PullETag: v.Pull}
	need := false
	if !notModified {
		ext := externalOf(it)
		read.UpdatedAt, read.IssueETag, read.PullETag = it.UpdatedAt, ext.IssueETag, ext.PullETag
		if need, err = dispatch.RefreshNeeded(tv.Ticket, ext); err != nil {
			s.refusal(w, err)
			return
		}
		if need {
			wctx, wcancel := context.WithTimeout(ctx, caps.write)
			rc, err := ds.RefreshExternal(wctx, op, key, ext)
			wcancel()
			if err != nil || rc.Status != dispatch.StatusDone {
				s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
				return
			}
			extra["changed"], extra["receipt"] = true, rc
			if s.external.cut != nil && s.external.cut("ticket") {
				return
			}
		}
	}
	if !need {
		if err := ds.RecordTicketRead(ctx, key, tv.ExternalID, read); err != nil {
			s.refusal(w, err)
			return
		}
	}
	if body.Images == nil || *body.Images {
		images := func() ([]tracker.ImageRef, error) { return it.Images, nil }
		if notModified {
			// No body, and no signed address is ever kept: one more read,
			// without validators, made only when an entry is pending.
			images = s.readImages(rctx, ref, node)
		}
		ictx, icancel := context.WithTimeout(ctx, caps.images)
		s.completeImages(ictx, ds, op, key, ref, images, false)
		icancel()
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.refreshReply(ctx, w, ds, key, extra)
}

// refreshReply answers a refresh: extra, the ticket and its manifest.
func (s *Server) refreshReply(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, key string, extra map[string]any) {
	tv, err := ds.Ticket(ctx, key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	extra["ticket"], extra["images"] = tv, manifestOf(tv.Ticket)
	writeDispatch(w, http.StatusOK, extra)
}

// readImages is the images of ref's item as one more read finds them, by
// its node id and without validators (atGitHub), for completeImages, which
// asks only when an entry is pending.
func (s *Server) readImages(ctx context.Context, ref tracker.Ref, node string) func() ([]tracker.ImageRef, error) {
	return func() ([]tracker.ImageRef, error) {
		it, _, err := s.readItem(ctx, ref, tracker.Validators{NodeID: node})
		return it.Images, err
	}
}

// refusal answers an error of a read's record: a store's refusal as 409
// {error} with its code (stale_read, invalid), anything else as
// dispatchError does.
func (s *Server) refusal(w http.ResponseWriter, err error) {
	var ref *dispatch.Refusal
	if errors.As(err, &ref) {
		status := http.StatusConflict
		if ref.Code == dispatch.RefusalNotFound {
			status = http.StatusNotFound
		}
		writeDispatch(w, status, map[string]any{"error": apiError{Code: ref.Code, Message: ref.Words}})
		return
	}
	s.dispatchError(w, "ticket", err)
}

// handleDispatchAuthority moves a field of a ticket between Dispatch and
// its linked item's system (ticket.authority): replayed (replayed, again
// under the ticket's lock); to
// Dispatch, run as it is; to the system, under the ticket's lock, the item
// read first and handed to the operation, which writes the snapshot, the
// owner and the field from it. {receipt, ticket}.
func (s *Server) handleDispatchAuthority(w http.ResponseWriter, r *http.Request) {
	var body externalAuthorityBody
	ds, key, ok := s.externalStart(w, r, &body, &body.dispatchOp, ticketKey)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+caps.write)
	defer cancel()
	op, args := body.op(r), dispatch.AuthorityArgs{Key: key, Field: body.Field, Owner: body.Owner}
	if s.replayed(ctx, w, ds, op, dispatch.IntentAuthority, args, key) {
		return
	}
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
	if s.replayed(ctx, w, ds, op, dispatch.IntentAuthority, args, key) {
		return
	}
	tv, ok := s.linkedTicket(ctx, w, ds, key)
	if !ok {
		return
	}
	var read *dispatch.ExternalRead
	ref, node, linked := snapshotRef(tv.ExternalSnapshot)
	if body.Owner != dispatch.OwnerDispatch && tv.ExternalID != "" && linked {
		rctx, rcancel := context.WithTimeout(ctx, caps.reads)
		it, _, err := s.readItem(rctx, ref, tracker.Validators{NodeID: node})
		rcancel()
		if err != nil {
			s.trackerFail(w, r, ref, err)
			return
		}
		ext := externalOf(it)
		read = &ext
		s.trackerLog(r, ref, http.StatusOK)
	}
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	rc, err := ds.SetAuthority(wctx, op, args, read)
	s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(wctx, ds, key))
}

// readableLink is the ticket's live delivery or external link with the
// id and the item its URL names, answering why not (404 not_found for a
// link that is not the ticket's live one, 400 not_a_tracker_url for one
// no adapter reads): false once it answered.
func (s *Server) readableLink(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, key, id string) (tracker.Ref, bool) {
	tv, ok := s.linkedTicket(ctx, w, ds, key)
	if !ok {
		return tracker.Ref{}, false
	}
	i := slices.IndexFunc(tv.Links, func(l dispatch.Link) bool { return l.ID == id && l.DeletedAt.IsZero() })
	if i < 0 || tv.Links[i].Kind != dispatch.LinkDelivery && tv.Links[i].Kind != dispatch.LinkExternal {
		writeError(w, http.StatusNotFound, "not_found", "no such live delivery or external link of "+key)
		return tracker.Ref{}, false
	}
	ref, ae := s.parseItem(tv.Links[i].URL, "")
	if ae != nil {
		writeAPIError(w, ae)
		return tracker.Ref{}, false
	}
	return ref, true
}

// readLink reads the item a link names and records it (RecordRead): its
// state, a pull request's merge and head, and, withChecks, its checks at
// the head commit (a cut set saying so; checks that could not be read
// said, the state kept). Without checks (the close's reads), the last
// read's checks are kept when the head is the same, and said not read
// otherwise. A failure, GitHub's or the store's check of what was read,
// is recorded as the read's words (RecordReadError) and returned. Every request is made through atGitHub, within ctx; once
// the server begins to stop, none is made and nothing is recorded.
func (s *Server) readLink(ctx context.Context, ds *dispatch.Store, linkID string, ref tracker.Ref, withChecks bool) error {
	rctx, rcancel := context.WithTimeout(ctx, s.trackerCaps().reads)
	defer rcancel()
	return s.readLinkIn(rctx, ctx, ds, linkID, ref, withChecks)
}

// readLinkIn is readLink with its reads made within rctx and its record
// within a write's cap of its own taken from ctx, so a read that used all
// of its time leaves the record its own.
func (s *Server) readLinkIn(rctx, ctx context.Context, ds *dispatch.Store, linkID string, ref tracker.Ref, withChecks bool) error {
	caps := s.trackerCaps()
	stop := s.external.stopping()
	if stop.Err() != nil {
		return stop.Err()
	}
	prior, _, err := ds.LinkReadOf(ctx, linkID)
	if err != nil {
		return err
	}
	var it tracker.Item
	var set tracker.CheckSet
	var checksErr error
	err = s.atGitHub(rctx, ref, func(ad tracker.Adapter, c tracker.Credential) error {
		var e error
		if it, _, e = ad.Read(rctx, c, ref, tracker.Validators{NodeID: prior.NodeID}); e != nil {
			return e
		}
		if withChecks && it.Ref.Kind == tracker.KindChange && it.HeadSHA != "" {
			set, checksErr = ad.Checks(rctx, c, it.Ref, it.HeadSHA)
		}
		return nil
	})
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	var ae *apiError
	switch {
	case stop.Err() != nil:
		return stop.Err()
	case errors.As(err, &ae):
		// No token, or a host out of form: GitHub was not asked.
		return err
	case err != nil:
		_ = ds.RecordReadError(wctx, linkID, trackerAnswer(err, ref).Message)
		return err
	}
	lr := dispatch.LinkRead{System: it.Ref.System, Host: it.Ref.Host, Kind: it.Ref.Kind, NodeID: it.NodeID, State: it.State, Merged: it.Merged,
		MergedAt: it.MergedAt, MergeSha: it.MergeSHA, HeadSha: it.HeadSHA, Checks: []dispatch.LinkCheck{}, UpdatedAt: it.UpdatedAt,
		IssueETag: etag(it.Validators.Issue), PullETag: etag(it.Validators.Pull)}
	switch {
	case it.Ref.Kind != tracker.KindChange:
	case !withChecks && prior.HeadSha == it.HeadSHA:
		lr.Checks, lr.Incomplete = prior.Checks, prior.Incomplete
	case !withChecks:
		lr.Incomplete = &dispatch.ChecksIncomplete{Words: []string{"the checks at this head commit were not read with this read: read the link again"}}
	case checksErr != nil:
		lr.Incomplete = &dispatch.ChecksIncomplete{Words: []string{"the checks could not be read: " + trackerAnswer(checksErr, ref).Code}}
	default:
		// Every text GitHub wrote made storable (dispatch.Storable), each
		// state one of the system's words (checkWord).
		for _, c := range set.Checks {
			lr.Checks = append(lr.Checks, dispatch.LinkCheck{ID: c.ID, Name: dispatch.Storable(c.Name, dispatch.MaxCheckName, false), Status: checkWord(c.Status),
				Conclusion: checkWord(c.Conclusion), URL: c.URL, App: dispatch.Storable(c.App, dispatch.MaxCheckName, false), HasLog: c.HasLog, CompletedAt: c.CompletedAt})
		}
		if len(set.Incomplete) > 0 {
			// Cut to what the store keeps, measured as it measures.
			lr.Incomplete = dispatch.FitIncomplete(set.Incomplete, set.Omitted)
		}
	}
	err = ds.RecordRead(wctx, linkID, lr)
	if ref := (*dispatch.Refusal)(nil); errors.As(err, &ref) && ref.Code != dispatch.RefusalNotFound && ref.Code != dispatch.RefusalStaleRead {
		// The store's check refused what was read: a failed read, as
		// GitHub's failures are.
		_ = ds.RecordReadError(wctx, linkID, "GitHub's answer for the item could not be kept: "+ref.Code)
	}
	return err
}

// handleDispatchReadLink reads the GitHub item a ticket's live delivery or
// external link names, and for a pull request its checks at its head
// commit, under the link's lock, and records it: 200 {read}, the link's
// read whole; GitHub's failures as trackerAnswer says, their words kept
// on the link's read.
func (s *Server) handleDispatchReadLink(w http.ResponseWriter, r *http.Request) {
	var body linkReadBody
	ds, key, ok := s.externalStart(w, r, &body, nil, ticketKey)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.reads+caps.write)
	defer cancel()
	ref, ok := s.readableLink(ctx, w, ds, key, id)
	if !ok {
		return
	}
	release, ok := s.lockFor(ctx, w, "link:"+id)
	if !ok {
		return
	}
	defer release()
	if err := s.readLink(ctx, ds, id, ref, true); err != nil {
		var refused *dispatch.Refusal
		if errors.As(err, &refused) {
			s.refusal(w, err)
			return
		}
		s.trackerFail(w, r, ref, err)
		return
	}
	s.trackerLog(r, ref, http.StatusOK)
	read, _, err := ds.LinkReadOf(ctx, id)
	if err != nil {
		s.dispatchError(w, "link", err)
		return
	}
	writeDispatch(w, http.StatusOK, map[string]any{"read": read})
}

// handleDispatchLinkRead answers a ticket's link's last read whole, its
// checks included: {read}, null when it was never read; 404 for a link
// that is not the ticket's.
func (s *Server) handleDispatchLinkRead(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tv, err := ds.Ticket(r.Context(), key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	if !slices.ContainsFunc(tv.Links, func(l dispatch.Link) bool { return l.ID == id }) {
		writeError(w, http.StatusNotFound, "not_found", "no such link of "+key)
		return
	}
	read, found, err := ds.LinkReadOf(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "link", err)
		return
	}
	reply := map[string]any{"read": nil}
	if found {
		reply["read"] = read
	}
	writeDispatch(w, http.StatusOK, reply)
}

// readAddedLink reads, once, the GitHub item a delivery or external link
// just added names, when a token is kept for its host (asked on the
// request, credentialFor): in the background, bounded by
// TrackerReadTimeout and ended as the server begins to stop (none begun
// after), one at a time per ticket and per link, its read recorded as the
// link's (readLink). A host with no token, a link no adapter reads and a
// resend record nothing: the link is as DS1 made it.
func (s *Server) readAddedLink(ds *dispatch.Store, key string, l dispatch.NewLink, rc dispatch.Receipt, err error) {
	if err != nil || rc.Status != dispatch.StatusDone || rc.Repeat || rc.ID == "" || l.Kind != dispatch.LinkDelivery && l.Kind != dispatch.LinkExternal {
		return
	}
	ref, perr := s.trackers.gh.Parse(l.URL, "")
	if perr != nil {
		return
	}
	if _, err := s.credentialFor(context.Background(), ref); err != nil {
		return
	}
	s.track(func() {
		caps := s.trackerCaps()
		ctx, cancel := context.WithTimeout(s.external.stopping(), caps.busy+caps.reads+caps.write)
		defer cancel()
		if ctx.Err() != nil {
			return
		}
		release, ok := s.external.lock(ctx, "added:"+key, caps.busy)
		if !ok {
			return
		}
		defer release()
		if release, ok := s.external.lock(ctx, "link:"+rc.ID, caps.busy); ok {
			_ = s.readLink(ctx, ds, rc.ID, ref, true)
			release()
		}
	})
}

// readForClose reads, before a ticket's edit to done whose project's
// policy requires merged, every live delivery link to a GitHub item (at
// most maxCloseReads at once, each under its link's lock, the waits and
// the reads within one phase, each read within the merged reads' cap),
// recording each within a write's cap of its own, so the edit's
// transaction judges reads made now (dispatch.MaxMergedReadAge). It reads
// nothing for an edit the store would not run as a close: one whose
// receipt is kept (a resend), or of a ticket deleted, done already, or at
// another revision than the one sent; the store answers those. The edit
// goes to the store only when every read it needed was made and recorded
// without a failure: a read not made (no token kept for the link's host,
// the link's wait ended, no slot within the phase, an address the parser
// no longer reads for a link read before) or failed refuses it, 409
// close_policy naming the link, nothing closed. A store's failure before
// the reads is the edit's to answer, but once the server has begun to stop
// an edit whose reads it could not settle is a busy route's 503, nothing
// closed. False once it answered.
func (s *Server) readForClose(w http.ResponseWriter, r *http.Request, ds *dispatch.Store, key string, op dispatch.Op, e dispatch.TicketEdit) bool {
	if e.Set.Status == nil || *e.Set.Status != dispatch.TicketDone {
		return true
	}
	running := func() bool {
		if s.external.stopping().Err() == nil {
			return true
		}
		w.Header().Set("Retry-After", "5")
		writeAPIError(w, errItemBusy)
		return false
	}
	caps := s.trackerCaps()
	ctx, cancel := s.trackerContext(r, caps.busy+caps.merged+caps.write)
	defer cancel()
	// The locks' waits and the reads in one phase, each read within the
	// merged reads' cap; each record within a write's cap of its own after.
	pctx, pcancel := context.WithTimeout(ctx, caps.busy+caps.merged)
	defer pcancel()
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	switch {
	case err != nil:
		return running()
	case !slices.Contains(pv.Project.CloseRequires, dispatch.RequireMerged):
		return true
	}
	// The edit's content as EditTicket hands it to Apply.
	args := struct {
		Key string `json:"key"`
		dispatch.TicketEdit
	}{key, e}
	_, found, err := ds.Replay(ctx, op, "ticket.edit", args)
	if err != nil {
		return running()
	}
	tv, err := ticketFor(ctx, ds, key)
	var refused *dispatch.Refusal
	switch {
	case found, errors.As(err, &refused), err == nil && (tv.Status == dispatch.TicketDone || op.ExpectedRev != 0 && op.ExpectedRev != tv.Rev):
		return true
	case err != nil:
		return running()
	}
	var wg sync.WaitGroup
	at := make(chan struct{}, maxCloseReads)
	failed := make([]string, len(tv.Links))
	for i, l := range tv.Links {
		if l.Kind != dispatch.LinkDelivery || !l.DeletedAt.IsZero() {
			continue
		}
		ref, ae := s.parseItem(l.URL, "")
		if ae != nil {
			// Not a GitHub item the parser reads now: needed only when a
			// read of it is kept, which the policy could count.
			if _, kept, _ := ds.LinkReadOf(ctx, l.ID); kept {
				failed[i] = "link " + l.ID
			}
			continue
		}
		wg.Go(func() {
			failed[i] = ref.String()
			select {
			case at <- struct{}{}:
			case <-pctx.Done():
				return
			}
			defer func() { <-at }()
			release, ok := s.external.lock(pctx, "link:"+l.ID, caps.busy)
			if !ok {
				return
			}
			defer release()
			rctx, rcancel := context.WithTimeout(pctx, caps.merged)
			defer rcancel()
			if err := s.readLinkIn(rctx, ctx, ds, l.ID, ref, false); err != nil {
				s.log.Info("tracker read", "route", r.Pattern, "ref", ref.Key(), "status", trackerAnswer(err, ref).status)
				return
			}
			failed[i] = ""
		})
	}
	wg.Wait()
	if !running() {
		return false
	}
	for _, name := range failed {
		if name != "" {
			writeAPIError(w, newAPIError(http.StatusConflict, "close_policy", "the delivery "+name+" could not be read for the close, so the ticket is not closed: read it again"))
			return false
		}
	}
	return true
}

// ticketFor reads key's ticket for a route that reads GitHub: a deleted
// one is refused (a *dispatch.Refusal not_found, as the store refuses an
// operation on it), so it is asked nothing.
func ticketFor(ctx context.Context, ds *dispatch.Store, key string) (dispatch.TicketView, error) {
	tv, err := ds.Ticket(ctx, key)
	if err == nil && !tv.DeletedAt.IsZero() {
		err = &dispatch.Refusal{Code: dispatch.RefusalNotFound, Words: key + " is deleted"}
	}
	return tv, err
}
