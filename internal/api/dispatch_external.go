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
	"github.com/rocksolidlabs/conductor/internal/gitrepo"
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
	// requires merged: the links' locks, their reads at most
	// maxCloseReads at once, the edit.
	MergedCloseTimeout = trackerBusyWait + mergedReadsBudget + dispatchWriteTimeout

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
// commit (never a database transaction across the call), and two seams of
// the tests (caps, cut), unset in the product.
type externalState struct {
	mu   sync.Mutex
	held map[string]chan struct{}
	caps *trackerCaps
	// cut, when set, is asked after an import's ticket ("ticket") and after
	// each image's commit ("image"): true ends the route there, as a crash
	// would, so a resend is what completes it.
	cut func(at string) bool
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

// atGitHub is the one door of this file to GitHub: the credential for ref
// (credentialFor, 412 no_token without one), then fn with the adapter,
// which alone opens it and makes every request.
func (s *Server) atGitHub(ctx context.Context, ref tracker.Ref, fn func(tracker.Adapter, tracker.Credential) error) error {
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

// projectSpace is a project's repository for a short form: its tracker's
// URL, else its folder's origin remote (resolveCwd, read by go-git), else
// none. Nothing of it is answered or logged: the parse keeps a host and a
// repository from it alone.
func (s *Server) projectSpace(ctx context.Context, p dispatch.Project) string {
	if p.TrackerURL != "" || p.Cwd == "" {
		return p.TrackerURL
	}
	dir, err := s.resolveCwd(p.Cwd)
	if err != nil {
		return ""
	}
	u, _ := gitrepo.RemoteURL(ctx, dir, "origin")
	return u
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
// the body, the validators. The store cleans, cuts and digests it.
func externalOf(it tracker.Item) dispatch.ExternalRead {
	repro, _ := tracker.Repro(it.Body)
	sn := dispatch.ExternalSnapshot{V: dispatch.SnapshotVersion, System: it.Ref.System, Host: it.Ref.Host, Space: it.Ref.Space, Kind: it.Ref.Kind, ID: it.Ref.ID,
		NodeID: it.NodeID, DatabaseID: string(it.DatabaseID), URL: it.URL, Title: it.Title, BodyTruncated: it.BodyTruncated, State: it.State,
		StateReason: it.StateReason, Merged: it.Merged, MergedAt: it.MergedAt, MergeSha: it.MergeSHA, HeadSha: it.HeadSHA, BaseRef: it.BaseRef,
		Labels: it.Labels, LabelsTotal: len(it.Labels), Assignees: it.Assignees, Milestone: it.Milestone, Author: it.Author, AuthorID: string(it.AuthorID),
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, ClosedAt: it.ClosedAt, Repro: repro}
	for _, im := range it.Images {
		sn.Images = append(sn.Images, dispatch.SnapshotImage{ID: im.ID})
	}
	return dispatch.ExternalRead{Snapshot: sn, Body: it.Body, IssueETag: etag(it.Validators.Issue), PullETag: etag(it.Validators.Pull)}
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
// by the receipt), bounded by its deadline.
func trackerContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), d)
}

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

// externalPreview is what an import of an item would make: the title and
// the description as the store would make them (the description's first
// maxPreviewDescription bytes and its length), the images by id, the
// steps to reproduce, GitHub's state, labels and assignees (shown, never
// mapped), the owners an import gives (the item's system for both), and
// the live ticket that has it as its linked issue already, if any.
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
// would make (externalPreview); it writes nothing.
func (s *Server) handleDispatchExternalPreview(w http.ResponseWriter, r *http.Request) {
	var body externalPreviewBody
	if !decodeExternal(w, r, &body, nil) {
		return
	}
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	ctx, cancel := trackerContext(r, TrackerReadTimeout)
	defer cancel()
	space := ""
	if body.Project != "" && !strings.Contains(body.URL, "://") {
		pv, err := ds.Project(ctx, body.Project)
		if err != nil {
			s.dispatchError(w, "project", err)
			return
		}
		space = s.projectSpace(ctx, pv.Project)
	}
	ref, ae := s.parseItem(body.URL, space)
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
	read := externalOf(it)
	title, description, _ := dispatch.FromExternal(it.Title, it.Body)
	p := externalPreview{Ref: it.Ref, URL: it.URL, Title: title, DescriptionLength: len(description), Repro: read.Snapshot.Repro, State: it.State,
		StateReason: it.StateReason, Labels: append([]string{}, it.Labels...), Assignees: append([]string{}, it.Assignees...),
		Owners: dispatch.Owners{Title: it.Ref.System, Description: it.Ref.System}, Images: []string{}}
	p.Description, _ = tracker.CutBytes(description, maxPreviewDescription)
	for _, im := range read.Snapshot.Images {
		p.Images = append(p.Images, im.ID)
	}
	// The live ticket whose linked issue it is: among the first tickets
	// the URL finds (its linked issue's page, or one of its links').
	if page, err := ds.List(ctx, dispatch.Query{External: it.URL}); err == nil {
		for _, t := range page.Tickets[:min(len(page.Tickets), 8)] {
			if tv, err := ds.Ticket(ctx, t.Key); err == nil && tv.DeletedAt.IsZero() && tv.ExternalURL == it.URL {
				p.Linked = t.Key
				break
			}
		}
	}
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
func (s *Server) completeImages(ctx context.Context, ds *dispatch.Store, parent dispatch.Op, key string, ref tracker.Ref, images func() ([]tracker.ImageRef, error)) {
	tv, err := ds.Ticket(ctx, key)
	if err != nil {
		return
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
	if !decodeExternal(w, r, &body, &body.dispatchOp) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := projectKey(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.reads+caps.write+caps.images)
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
		images = func() ([]tracker.ImageRef, error) {
			it, _, err := s.readItem(rctx, ref, tracker.Validators{NodeID: node})
			return it.Images, err
		}
	} else {
		pv, err := ds.Project(ctx, key)
		if err != nil {
			s.dispatchError(w, "project", err)
			return
		}
		var ae *apiError
		if ref, ae = s.parseItem(body.URL, s.projectSpace(ctx, pv.Project)); ae != nil {
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
			s.completeImages(ictx, ds, op, rc.Key, ref, images)
			icancel()
			release()
		}
	}
	s.trackerLog(r, ref, http.StatusOK)
	s.externalReply(ctx, w, ds, rc, rc.Key, true, nil)
}

// linkedTicket reads key's ticket for an operation on its linked issue,
// answering a store's failure: false once it answered.
func (s *Server) linkedTicket(ctx context.Context, w http.ResponseWriter, ds *dispatch.Store, key string) (dispatch.TicketView, bool) {
	tv, err := ds.Ticket(ctx, key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return tv, false
	}
	return tv, true
}

// handleDispatchLinkExternal sets a ticket's linked issue to the item at
// url (ticket.link_external), every field staying Dispatch's: the
// operation replayed first, then, under the ticket's lock, the item read
// and the operation run with the read. {receipt, ticket}; the store's
// refusals (409 external_linked, invalid for a ticket linked already).
func (s *Server) handleDispatchLinkExternal(w http.ResponseWriter, r *http.Request) {
	var body externalLinkBody
	if !decodeExternal(w, r, &body, &body.dispatchOp) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.reads+caps.write)
	defer cancel()
	op, args := body.op(r), dispatch.LinkArgs{Key: key, URL: body.URL}
	if rc, found, err := ds.Replay(ctx, op, dispatch.IntentLinkExternal, args); err != nil || found {
		s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
		return
	}
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	if err != nil {
		s.dispatchError(w, "project", err)
		return
	}
	ref, ae := s.parseItem(body.URL, s.projectSpace(ctx, pv.Project))
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
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
	if !decodeExternal(w, r, &body, &body) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.write)
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
// ran.
func (s *Server) handleDispatchRefresh(w http.ResponseWriter, r *http.Request) {
	var body externalRefreshBody
	if !decodeExternal(w, r, &body, &body.dispatchOp) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.reads+caps.write+caps.images)
	defer cancel()
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
	tv, ok := s.linkedTicket(ctx, w, ds, key)
	if !ok {
		return
	}
	ref, node, linked := snapshotRef(tv.ExternalSnapshot)
	if tv.ExternalID == "" || !linked {
		writeDispatch(w, http.StatusConflict, map[string]any{"error": apiError{Code: dispatch.RefusalInvalid, Message: key + " has no linked issue"}})
		return
	}
	op := body.op(r)
	v := tracker.Validators{NodeID: node}
	if er := tv.ExternalRead; er != nil && er.ExternalID == tv.ExternalID && er.Error == "" {
		v.Issue, v.Pull = er.IssueETag, er.PullETag
	}
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
	defer rcancel()
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
			images = func() ([]tracker.ImageRef, error) {
				again, _, err := s.readItem(rctx, ref, tracker.Validators{NodeID: node})
				return again.Images, err
			}
		}
		ictx, icancel := context.WithTimeout(ctx, caps.images)
		s.completeImages(ictx, ds, op, key, ref, images)
		icancel()
	}
	s.trackerLog(r, ref, http.StatusOK)
	tv, err = ds.Ticket(ctx, key)
	if err != nil {
		s.dispatchError(w, "ticket", err)
		return
	}
	extra["ticket"], extra["images"] = tv, manifestOf(tv.Ticket)
	writeDispatch(w, http.StatusOK, extra)
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
// its linked item's system (ticket.authority): replayed first; to
// Dispatch, run as it is; to the system, under the ticket's lock, the item
// read first and handed to the operation, which writes the snapshot, the
// owner and the field from it. {receipt, ticket}.
func (s *Server) handleDispatchAuthority(w http.ResponseWriter, r *http.Request) {
	var body externalAuthorityBody
	if !decodeExternal(w, r, &body, &body.dispatchOp) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.reads+caps.write)
	defer cancel()
	op, args := body.op(r), dispatch.AuthorityArgs{Key: key, Field: body.Field, Owner: body.Owner}
	if rc, found, err := ds.Replay(ctx, op, dispatch.IntentAuthority, args); err != nil || found {
		s.writeOp(w, "ticket", rc, err, false, "ticket", ticketAfter(ctx, ds, key))
		return
	}
	release, ok := s.lockFor(ctx, w, "ticket:"+key)
	if !ok {
		return
	}
	defer release()
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
// otherwise. A failure is recorded as the read's words (RecordReadError)
// and returned. Every request is made through atGitHub, within ctx.
func (s *Server) readLink(ctx context.Context, ds *dispatch.Store, linkID string, ref tracker.Ref, withChecks bool) error {
	caps := s.trackerCaps()
	prior, _, err := ds.LinkReadOf(ctx, linkID)
	if err != nil {
		return err
	}
	var it tracker.Item
	var set tracker.CheckSet
	var checksErr error
	rctx, rcancel := context.WithTimeout(ctx, caps.reads)
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
	rcancel()
	wctx, wcancel := context.WithTimeout(ctx, caps.write)
	defer wcancel()
	var ae *apiError
	switch {
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
		for _, c := range set.Checks {
			lr.Checks = append(lr.Checks, dispatch.LinkCheck{ID: c.ID, Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, URL: c.URL, App: c.App, HasLog: c.HasLog, CompletedAt: c.CompletedAt})
		}
		if len(set.Incomplete) > 0 {
			lr.Incomplete = &dispatch.ChecksIncomplete{Words: set.Incomplete, Omitted: set.Omitted}
		}
	}
	return ds.RecordRead(wctx, linkID, lr)
}

// handleDispatchReadLink reads the GitHub item a ticket's live delivery or
// external link names, and for a pull request its checks at its head
// commit, under the link's lock, and records it: 200 {read}, the link's
// read whole; GitHub's failures as trackerAnswer says, their words kept
// on the link's read.
func (s *Server) handleDispatchReadLink(w http.ResponseWriter, r *http.Request) {
	var body linkReadBody
	if !decodeExternal(w, r, &body, nil) {
		return
	}
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	key, ok := ticketKey(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.reads+caps.write)
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
	tv, ok := s.linkedTicket(r.Context(), w, ds, key)
	if !ok {
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
// TrackerReadTimeout, one at a time per ticket and per link, its read
// recorded as the link's (readLink). A host with no token, a link no
// adapter reads and a resend record nothing: the link is as DS1 made it.
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
		ctx, cancel := context.WithTimeout(context.Background(), caps.busy+caps.reads+caps.write)
		defer cancel()
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
// most maxCloseReads at once, each under its link's lock, within the
// merged reads' budget), recording each, so the edit's transaction judges
// a read made now (dispatch.MaxMergedReadAge). A read that fails leaves
// the link's last read and its words: the close is then the policy's to
// refuse. It never refuses the edit itself.
func (s *Server) readForClose(r *http.Request, ds *dispatch.Store, key string, e dispatch.TicketEdit) {
	if e.Set.Status == nil || *e.Set.Status != dispatch.TicketDone {
		return
	}
	caps := s.trackerCaps()
	ctx, cancel := trackerContext(r, caps.busy+caps.merged)
	defer cancel()
	project, _, _ := dispatch.ParseKey(key)
	pv, err := ds.Project(ctx, project)
	if err != nil || !slices.Contains(pv.Project.CloseRequires, dispatch.RequireMerged) {
		return
	}
	tv, err := ds.Ticket(ctx, key)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	at := make(chan struct{}, maxCloseReads)
	for _, l := range tv.Links {
		if l.Kind != dispatch.LinkDelivery || !l.DeletedAt.IsZero() {
			continue
		}
		ref, ae := s.parseItem(l.URL, "")
		if ae != nil {
			continue
		}
		wg.Go(func() {
			at <- struct{}{}
			defer func() { <-at }()
			if release, ok := s.external.lock(ctx, "link:"+l.ID, caps.busy); ok {
				if err := s.readLink(ctx, ds, l.ID, ref, false); err != nil {
					s.log.Info("tracker read", "route", r.Pattern, "ref", ref.Key(), "status", trackerAnswer(err, ref).status)
				}
				release()
			}
		})
	}
	wg.Wait()
}
