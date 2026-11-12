package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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

// A preview's label and a person's captures of it over HTTP (Program item
// PV1, docs/plans/browser-evidence.md): the label of an attempt's preview,
// the tree's revision read now; and a capture's create, its reads, its
// edit, its delete and its screenshot, kept in Dispatch's store
// (dispatch.CreateCapture and the rest). Every route is the admin's.
//
// What a request carries reaches the store through one gate, a route's
// reader (readCaptureCreate, readCaptureEdit, readCaptureDelete): its
// body bounded as a whole and in each part, decoded once with no member
// unknown, its errors in fixed words, and the digest of what was decoded
// (the operation's content) taken there, before anything is scrubbed.
// Every write replays first (dispatch.Store.Replay), before it resolves
// the preview or reads a tree. What the server fills (captureFacts) is its
// own read: the tree's revision, the run's scope, each source checked and
// classed in the tree; and before anything is stored, the tree's secret is
// replaced by its name in every string the request carries and the server
// read (scrubSecret).

const (
	// maxCaptureJSON bounds a create's capture part and an edit's body.
	maxCaptureJSON = 2 << 20
	// maxCaptureBody bounds a create's body: its capture part, its
	// screenshot and their envelope.
	maxCaptureBody = maxCaptureJSON + dispatch.MaxCaptureImage + 64<<10
	// labelReadWait bounds a read of a tree, the label's and a save's.
	labelReadWait = 5 * time.Second
	// maxTreeIndexes bounds the trees whose read keeps its index.
	maxTreeIndexes = 8
	// treeSecretName is what a tree's secret is replaced by.
	treeSecretName = "${CONDUCTOR_TREE_SECRET}"
)

// routeCaptures adds the capture routes and the label, each the admin's
// and a switchyard's 403.
func (s *Server) routeCaptures(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/captures", s.requireAdmin(s.launchingAny(s.handleCaptureCreate)))
	mux.HandleFunc("GET /api/captures", s.requireAdmin(s.launchingAny(s.handleCaptures)))
	mux.HandleFunc("GET /api/captures/{id}", s.requireAdmin(s.launchingAny(s.handleCapture)))
	mux.HandleFunc("GET /api/captures/{id}/image", s.requireAdmin(s.launchingAny(s.handleCaptureImage)))
	mux.HandleFunc("POST /api/captures/{id}/edit", s.requireAdmin(s.launchingAny(s.handleCaptureEdit)))
	mux.HandleFunc("POST /api/captures/{id}/delete", s.requireAdmin(s.launchingAny(s.handleCaptureDelete)))
	mux.HandleFunc("GET /api/runs/{run}/tasks/{task}/attempts/{n}/preview", s.requireAdmin(s.launchingAny(s.handlePreviewLabel)))
}

// The bodies a capture route takes, as the gate decodes them.
type (
	// captureFrom is what a capture was taken of: an attempt's preview
	// (Seq a candidate's checkout, which no row offers yet), or a link.
	captureFrom struct {
		Run     string `json:"run,omitempty"`
		Task    string `json:"task,omitempty"`
		Attempt int    `json:"attempt,omitempty"`
		Seq     int    `json:"seq,omitempty"`
		URL     string `json:"url,omitempty"`
	}
	// captureAnnotation is an annotation as the client sends it: its
	// element the bridge's report as the bridge made it (proto).
	captureAnnotation struct {
		Kind     string               `json:"kind"`
		Note     string               `json:"note"`
		Rect     dispatch.Rect        `json:"rect"`
		Element  *proto.ElementReport `json:"element,omitempty"`
		Observed *dispatch.Observed   `json:"observed,omitempty"`
	}
	// captureCreateBody is a create's capture part: the operation, what
	// it was taken of, the client's words and the annotations.
	captureCreateBody struct {
		OpID   string      `json:"opId"`
		ByName string      `json:"byName,omitempty"`
		From   captureFrom `json:"from"`
		dispatch.NewCapture
		Annotations []captureAnnotation `json:"annotations"`
	}
	captureEditBody struct {
		OpID        string                    `json:"opId"`
		ByName      string                    `json:"byName,omitempty"`
		ExpectedRev int64                     `json:"expectedRev"`
		Annotations []dispatch.AnnotationEdit `json:"annotations"`
	}
	captureDeleteBody struct {
		OpID   string `json:"opId"`
		ByName string `json:"byName,omitempty"`
	}
	// captureArgs is a capture operation's content as the store
	// fingerprints it (dispatch's own, field for field).
	captureArgs struct {
		ID       string `json:"id,omitempty"`
		Received string `json:"received,omitempty"`
	}
)

// captureRequest is a write as the gate took it: its operation, its body,
// the screenshot's bytes (nil for none) and the digest of the request as
// received, the operation's content.
type captureRequest[T any] struct {
	op     dispatch.Op
	body   T
	image  []byte
	digest string
}

// badCapture is a request the gate refuses, in fixed words.
func badCapture(words string) *apiError {
	return newAPIError(http.StatusBadRequest, "invalid_request", words)
}

// decodeCapture decodes one JSON object of v's members and no other from
// b, in fixed words.
func decodeCapture(b []byte, v any) *apiError {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return badCapture("the body is not one JSON object of the route's members")
	}
	return nil
}

// opOf is the store's operation of r's principal, its id checked.
func opOf(r *http.Request, id, by string) (dispatch.Op, *apiError) {
	if !dispatch.OpIDPattern.MatchString(id) {
		return dispatch.Op{}, badCapture("opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	}
	return dispatch.Op{Actor: actorOf(r), ID: id, By: by}, nil
}

// readJSONBody reads a body of at most limit bytes whole: 413 bound past
// it, in fixed words.
func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, *apiError) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return nil, newAPIError(http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the body is more than %d MiB", limit>>20))
	case err != nil:
		return nil, badCapture("the body could not be read")
	}
	return b, nil
}

// readCaptureCreate is the gate of a create: multipart/form-data of at
// most maxCaptureBody, its part capture first (JSON, at most
// maxCaptureJSON) and then, when there is a screenshot, its part image
// (image/png, at most dispatch.MaxCaptureImage), each once and no other;
// the digest of the capture as decoded with the screenshot's SHA-256.
func readCaptureCreate(w http.ResponseWriter, r *http.Request) (captureRequest[captureCreateBody], *apiError) {
	var req captureRequest[captureCreateBody]
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return req, badCapture("a capture is multipart/form-data")
	}
	mr := multipart.NewReader(http.MaxBytesReader(w, r.Body, maxCaptureBody), params["boundary"])
	var capture []byte
	for i := 0; ; i++ {
		part, err := mr.NextPart()
		var tooLarge *http.MaxBytesError
		switch {
		case err == io.EOF:
			if capture == nil {
				return req, badCapture("a capture's first part is capture")
			}
			aerr := decodeCapture(capture, &req.body)
			if aerr == nil {
				req.op, aerr = opOf(r, req.body.OpID, req.body.ByName)
			}
			if aerr != nil {
				return req, aerr
			}
			sum := ""
			if req.image != nil {
				h := sha256.Sum256(req.image)
				sum = hex.EncodeToString(h[:])
			}
			content := req.body
			content.OpID, content.ByName = "", ""
			req.digest, err = dispatch.RequestDigest(content, sum)
			if err != nil {
				return req, badCapture("the capture could not be encoded")
			}
			return req, nil
		case errors.As(err, &tooLarge):
			return req, newAPIError(http.StatusRequestEntityTooLarge, "bound", "a capture is at most 2 MiB of capture and 4 MiB of screenshot")
		case err != nil:
			return req, badCapture("the body is not a capture's multipart/form-data")
		}
		name, limit := part.FormName(), int64(maxCaptureJSON)
		switch {
		case i == 0 && name == "capture":
		case i == 1 && name == "image":
			if ct, _, err := mime.ParseMediaType(part.Header.Get("Content-Type")); err != nil || ct != "image/png" {
				return req, badCapture("the part image is image/png")
			}
			limit = dispatch.MaxCaptureImage
		default:
			return req, badCapture("a capture's parts are capture, and then image when there is a screenshot")
		}
		b, err := io.ReadAll(io.LimitReader(part, limit+1))
		switch {
		case errors.As(err, &tooLarge) || int64(len(b)) > limit:
			return req, newAPIError(http.StatusRequestEntityTooLarge, "bound", "a capture is at most 2 MiB of capture and 4 MiB of screenshot")
		case err != nil:
			return req, badCapture("the body is not a capture's multipart/form-data")
		case name == "capture":
			capture = b
		default:
			req.image = b
		}
	}
}

// readCaptureEdit is the gate of an edit: one JSON object of at most
// maxCaptureJSON; the digest of its revision and annotations.
func readCaptureEdit(w http.ResponseWriter, r *http.Request) (captureRequest[captureEditBody], *apiError) {
	var req captureRequest[captureEditBody]
	b, aerr := readJSONBody(w, r, maxCaptureJSON)
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName)
	}
	if aerr != nil {
		return req, aerr
	}
	if req.body.ExpectedRev < 1 {
		return req, badCapture("expectedRev is the capture's revision")
	}
	content := req.body
	content.OpID, content.ByName = "", ""
	var err error
	if req.digest, err = dispatch.RequestDigest(content, ""); err != nil {
		return req, badCapture("the edit could not be encoded")
	}
	return req, nil
}

// readCaptureDelete is the gate of a delete: {opId, byName?}, at most
// maxBody.
func readCaptureDelete(w http.ResponseWriter, r *http.Request) (captureRequest[captureDeleteBody], *apiError) {
	var req captureRequest[captureDeleteBody]
	b, aerr := readJSONBody(w, r, maxBody)
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName)
	}
	return req, aerr
}

// captureID reads a capture route's {id}: 400 invalid_request for one that
// is not an id. When it reports false it has answered.
func captureID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return pathID(w, r)
}

// handleCaptureCreate keeps a capture (capture.create): the gate, then a
// resend answered by its receipt, then the preview resolved (404
// no_preview when it is not open now), the request and the server's
// reads scrubbed of the tree's secret, the screenshot checked whole, and
// the store's create. 201 {receipt, capture}; 200 for a repeat; a refusal
// as answerCapture answers it.
func (s *Server) handleCaptureCreate(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	req, aerr := readCaptureCreate(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	args := captureArgs{Received: req.digest}
	rc, found, err := ds.Replay(ctx, req.op, dispatch.IntentCaptureCreate, args)
	if err != nil || found {
		s.answerCapture(w, ctx, ds, rc, err, true, "")
		return
	}
	n := req.body.NewCapture
	n.Annotations = make([]dispatch.NewAnnotation, len(req.body.Annotations))
	for i, a := range req.body.Annotations {
		n.Annotations[i] = dispatch.NewAnnotation{Kind: a.Kind, Note: a.Note, Rect: a.Rect, Element: storeElement(a.Element), Observed: a.Observed}
	}
	facts, secret, aerr := s.captureFacts(ctx, ds, req.body.From, n)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	scrubSecret(&n, secret)
	scrubSecret(&facts, secret)
	var image io.Reader
	if req.image != nil {
		if aerr := checkScreenshot(ctx, req.image, secret); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		image = bytes.NewReader(req.image)
	}
	rc, err = ds.CreateCapture(ctx, req.op, req.digest, n, facts, image)
	s.answerCapture(w, ctx, ds, rc, err, true, "")
}

// handleCaptureEdit edits a live capture (capture.edit): the gate, a
// resend answered by its receipt, then the edit scrubbed of the capture's
// tree's secret while that tree is open, and the store's edit. 200
// {receipt, capture}.
func (s *Server) handleCaptureEdit(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok {
		return
	}
	req, aerr := readCaptureEdit(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, found, err := ds.Replay(ctx, req.op, dispatch.IntentCaptureEdit, captureArgs{ID: id, Received: req.digest})
	if err != nil || found {
		s.answerCapture(w, ctx, ds, rc, err, false, id)
		return
	}
	edits := req.body.Annotations
	if c, err := ds.Capture(ctx, id); err == nil && c.Origin.Kind == dispatch.OriginAttempt && s.runs != nil {
		if wp, ok := s.runs.WorktreePreview(c.Origin.Run, c.Origin.Task, c.Origin.Attempt); ok {
			scrubSecret(&edits, wp.Secret)
		}
	}
	rc, err = ds.EditCapture(ctx, req.op, req.digest, id, req.body.ExpectedRev, edits)
	s.answerCapture(w, ctx, ds, rc, err, false, id)
}

// handleCaptureDelete deletes a live capture (capture.delete): {receipt}.
func (s *Server) handleCaptureDelete(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok {
		return
	}
	req, aerr := readCaptureDelete(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, err := ds.DeleteCapture(ctx, req.op, id)
	s.answerCapture(w, ctx, ds, rc, err, false, "-")
}

// refusalStatus is the status a refusal of the store's is answered with:
// 400 invalid, 413 bound, 404 not_found, 409 every other.
func refusalStatus(code string) int {
	switch code {
	case dispatch.RefusalInvalid:
		return http.StatusBadRequest
	case dispatch.RefusalBound:
		return http.StatusRequestEntityTooLarge
	case dispatch.RefusalNotFound:
		return http.StatusNotFound
	}
	return http.StatusConflict
}

// answerCapture answers a capture operation (rc, or err when the store
// took nothing, as dispatchError answers it). Refused: {error, receipt},
// the refusal its code and the store's words its message, at
// refusalStatus (503 dispatch_unavailable for read_only). Done: {receipt,
// capture}, the capture read after it by the receipt's id, else id ("-"
// for a delete, whose answer is the receipt alone); 201 for a create made
// now.
func (s *Server) answerCapture(w http.ResponseWriter, ctx context.Context, ds *dispatch.Store, rc dispatch.Receipt, err error, create bool, id string) {
	switch {
	case err != nil:
		s.dispatchError(w, "capture", err)
		return
	case rc.Status != dispatch.StatusDone && rc.Refusal == dispatch.RefusalReadOnly:
		writeError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "Dispatch is read-only: "+rc.Error)
		return
	case rc.Status != dispatch.StatusDone:
		writeDispatch(w, refusalStatus(rc.Refusal), map[string]any{"error": apiError{Code: rc.Refusal, Message: rc.Error}, "receipt": rc})
		return
	}
	status := http.StatusOK
	if create && !rc.Repeat {
		status = http.StatusCreated
	}
	reply := map[string]any{"receipt": rc}
	if id != "-" {
		var capture any
		if c, err := ds.Capture(ctx, cmpOr(id, rc.ID)); err == nil {
			capture = c
		}
		reply["capture"] = capture
	}
	writeDispatch(w, status, reply)
}

// storeElement is the bridge's report in the store's shape: the classes
// split at ASCII white space (the DOM's own separator), each member as
// the bridge gave it.
func storeElement(p *proto.ElementReport) *dispatch.ElementReport {
	if p == nil {
		return nil
	}
	classes := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r' })
	}
	e := &dispatch.ElementReport{Tag: p.Tag, ID: p.ID, Classes: classes(p.Classes), Role: p.Role, Name: p.Name, Text: p.Text, Selector: p.Selector,
		Box: dispatch.Rect(p.Box), Attributes: p.Attributes, HTML: p.HTML, Neighbourhood: p.Neighbourhood, Styles: p.Styles, Cut: p.Cut,
		State: dispatch.ElementState{Disabled: p.State.Disabled, Hidden: p.State.Hidden, AriaDisabled: p.State.AriaDisabled, AriaExpanded: p.State.AriaExpanded,
			PointerEvents: p.State.PointerEvents, Inert: p.State.Inert, Hovered: p.State.Hovered, Motion: int(p.State.Motion)},
		Hit: dispatch.ElementHit(p.Hit)}
	for _, a := range p.Ancestors {
		e.Ancestors = append(e.Ancestors, dispatch.Ancestor{Tag: a.Tag, ID: a.ID, Classes: classes(a.Classes), Role: a.Role})
	}
	for _, h := range p.Hints {
		e.Hints = append(e.Hints, dispatch.Hint{How: h.How, File: h.File, Line: int(h.Line), Column: int(h.Column), Component: h.Component})
	}
	return e
}

// pageOriginOf is a URL's origin as a browser compares it: its scheme and
// host lower-cased and its effective port; "" for no http or https URL
// with a host.
func pageOriginOf(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host + ":" + strconv.Itoa(n)
}

// captureFacts is what the server reads at a create, from what it holds
// and never from the request: for a link, the origin alone (its page of
// the link's origin); for an attempt, its worktree preview open now (404
// no_preview otherwise, a candidate's checkout among it), the page of
// that preview's origin (400 invalid), the tree's revision read now, the
// run's scope, and each element's sources made relative, checked and
// classed in the tree; and the tree's secret, for the scrub.
func (s *Server) captureFacts(ctx context.Context, ds *dispatch.Store, from captureFrom, n dispatch.NewCapture) (dispatch.CaptureFacts, string, *apiError) {
	page := pageOriginOf(n.URL)
	if from.Run == "" && from.Task == "" && from.Attempt == 0 && from.Seq == 0 {
		if from.URL == "" || page == "" || pageOriginOf(from.URL) != page {
			return dispatch.CaptureFacts{}, "", newAPIError(http.StatusBadRequest, "invalid", "a link's capture is of a page of the link's origin")
		}
		return dispatch.CaptureFacts{Origin: dispatch.OriginURL}, "", nil
	}
	if from.URL != "" || !crew.ValidRunID(from.Run) || !crew.ValidTaskID(from.Task) || from.Attempt < 1 {
		return dispatch.CaptureFacts{}, "", newAPIError(http.StatusBadRequest, "invalid", "from names an attempt (run, task, attempt) or a link (url), not both")
	}
	noPreview := newAPIError(http.StatusNotFound, "no_preview", "the attempt's preview is not open now")
	if from.Seq != 0 || s.runs == nil {
		return dispatch.CaptureFacts{}, "", noPreview
	}
	wp, ok := s.runs.WorktreePreview(from.Run, from.Task, from.Attempt)
	if !ok {
		return dispatch.CaptureFacts{}, "", noPreview
	}
	if want := pageOriginOf(wp.URL); page == "" || page != want {
		return dispatch.CaptureFacts{}, "", newAPIError(http.StatusBadRequest, "invalid", fmt.Sprintf("the page is %s, not this attempt's preview %s", cmpOr(page, "no http or https page"), want))
	}
	var hints [][]dispatch.Hint
	for _, a := range n.Annotations {
		var hs []dispatch.Hint
		if a.Element != nil {
			hs = a.Element.Hints
		}
		hints = append(hints, hs)
	}
	read, sources, err := s.trees.read(ctx, wp.Dir, hints)
	if read.Commit == "" {
		s.log.Warn("a capture's tree could not be read", "run", from.Run, "task", from.Task, "attempt", from.Attempt, "err", err)
		return dispatch.CaptureFacts{}, "", newAPIError(http.StatusServiceUnavailable, "tree_unread", "the attempt's tree could not be read: save again")
	}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginAttempt, Run: from.Run, Task: from.Task, Attempt: from.Attempt, Tree: dispatch.TreeWorktree,
		Commit: read.Commit, Content: read.Content, Changed: read.Changed, ChangedPaths: storedPaths(read.Paths), Mutable: true,
		Where: cmpOr(wp.Where, "native"), Scope: s.captureScope(ctx, ds, from.Run), Sources: sources}
	return f, wp.Secret, nil
}

// captureScope is the run's crew, its repository (the top of its working
// directory) and the project of the ticket it was launched from, as a
// read of the run now gives them; what cannot be read is left empty.
func (s *Server) captureScope(ctx context.Context, ds *dispatch.Store, runID string) dispatch.CaptureScope {
	var sc dispatch.CaptureScope
	if run, ok := s.runs.Get(runID); ok {
		sc.Crew = run.CrewID
		gctx, cancel := context.WithTimeout(ctx, labelReadWait)
		if top, err := gitcli.TopOf(gctx, run.Cwd); err == nil {
			sc.Repo = top
		}
		cancel()
	}
	if tk, ok, err := ds.RunTicket(ctx, runID); err == nil && ok && tk.DeletedAt.IsZero() {
		sc.Project = projectOfKey(tk.Key)
	}
	return sc
}

// storedPaths is the changed paths a capture and a label keep: those the
// store's rule takes (UTF-8 with no control character), the rest left
// out and still counted.
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

// read reads the tree at dir now (crew.TreeRevision) and, for each
// annotation's hints, its sources (captureSources). The tree's directory
// is opened through its parent's (session.OpenFolderIn), so a link put in
// its place leads nowhere outside that parent. A read that ends past
// labelReadWait or fails after naming HEAD gives the commit with no
// content and the error.
func (tr *treeReads) read(ctx context.Context, dir string, hints [][]dispatch.Hint) (crew.TreeRead, [][]dispatch.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, labelReadWait)
	defer cancel()
	folder, err := session.OpenFolderIn(filepath.Dir(dir), filepath.Base(dir), nil)
	if err != nil {
		return crew.TreeRead{}, nil, err
	}
	defer folder.Close()
	d, err := folder.Dir()
	if err != nil {
		return crew.TreeRead{}, nil, err
	}
	defer d.Close()
	at, still := gitcli.OpenedPath(d, dir)
	index, gate, err := tr.index(dir)
	if err != nil {
		return crew.TreeRead{}, nil, err
	}
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return crew.TreeRead{}, nil, ctx.Err()
	}
	defer func() { <-gate }()
	read, err := crew.TreeRevision(ctx, at, index)
	if err != nil {
		read.Content = ""
		return read, nil, err
	}
	var sources [][]dispatch.Source
	if len(hints) > 0 {
		sources, err = captureSources(ctx, folder, at, index, dir, hints)
	}
	if err == nil && !still() {
		err = errors.New("the tree's directory moved while it was read")
	}
	if err != nil {
		read.Content, sources = "", nil
	}
	return read, sources, err
}

// captureSources is each annotation's hints checked in the tree at dir
// (read through folder and, for git, at the path at): a hint's file made
// relative to the tree (outside, kept as reported, when it is not in
// it), found when the folder holds it (no link followed out of it),
// classed tracked, untracked, ignored or vendor (a path with a
// node_modules element), and ordered by class, the innermost first within
// each, as the capture document says.
func captureSources(ctx context.Context, folder *session.Folder, at, index, dir string, hints [][]dispatch.Hint) ([][]dispatch.Source, error) {
	bases := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != bases[0] {
		bases = append(bases, real)
	}
	rel := func(file string) (string, bool) {
		p := filepath.Clean(filepath.FromSlash(file))
		if !filepath.IsAbs(p) {
			return filepath.ToSlash(p), p != ".." && !strings.HasPrefix(p, ".."+string(filepath.Separator)) && p != "."
		}
		for _, b := range bases {
			if r, err := filepath.Rel(b, p); err == nil && r != "." && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return filepath.ToSlash(r), true
			}
		}
		return file, false
	}
	out := make([][]dispatch.Source, len(hints))
	var inside []string
	for i, hs := range hints {
		for _, h := range hs {
			p, in := rel(h.File)
			src := dispatch.Source{Path: p, Line: h.Line, Column: h.Column, Component: h.Component, How: h.How, Outside: !in}
			if in {
				if _, err := folder.Lstat(filepath.FromSlash(p)); err == nil {
					src.Found = true
				}
				inside = append(inside, p)
			}
			out[i] = append(out[i], src)
		}
	}
	tracked, ignored, err := crew.TreeClasses(ctx, at, index, inside)
	if err != nil {
		return nil, err
	}
	for i := range out {
		for j := range out[i] {
			src := &out[i][j]
			switch {
			case slices.Contains(strings.Split(src.Path, "/"), "node_modules"):
				src.Class = "vendor"
			case !src.Outside && tracked[src.Path]:
				src.Class = "tracked"
			case !src.Outside && ignored[src.Path]:
				src.Class = "ignored"
			default:
				src.Class = "untracked"
			}
		}
		slices.SortStableFunc(out[i], func(a, b dispatch.Source) int {
			return slices.Index(dispatch.SourceClasses, a.Class) - slices.Index(dispatch.SourceClasses, b.Class)
		})
	}
	return out, nil
}

// scrubSecret replaces every occurrence of the tree's secret, in any case,
// with its name (treeSecretName) in every string v (a pointer) holds,
// however deep: the request's strings and the server's reads alike, before
// any is checked or kept. The secret is hex, so its URL-encoded form is
// itself, and the name holds no run of hex that could make it again.
func scrubSecret(v any, secret string) {
	if secret != "" {
		scrubValue(reflect.ValueOf(v), strings.ToLower(secret))
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
	if !strings.Contains(low, secret) {
		return s
	}
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
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// pngDecodes holds the whole decode of a screenshot to one at a time: at
// most one 4,096-pixel image (128 MiB at 16 bits a channel) in memory.
var pngDecodes = make(chan struct{}, 1)

// checkScreenshot checks a screenshot's bytes before the store keeps
// them: a PNG by its bytes, its header and size first (png.DecodeConfig:
// each side 1 to dispatch.MaxCaptureImageSide, before a pixel is
// allocated), then the whole of it decoded (every chunk's CRC and every
// row), one decode at a time; and the tree's secret in none of its bytes.
func checkScreenshot(ctx context.Context, b []byte, secret string) *apiError {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a PNG")
	}
	if most := dispatch.MaxCaptureImageSide; cfg.Width < 1 || cfg.Height < 1 || cfg.Width > most || cfg.Height > most {
		return newAPIError(http.StatusBadRequest, "invalid", fmt.Sprintf("the screenshot is %d by %d pixels; a side is 1 to %d", cfg.Width, cfg.Height, most))
	}
	if secret != "" && strings.Contains(asciiLower(string(b)), strings.ToLower(secret)) {
		return newAPIError(http.StatusBadRequest, "invalid", "the screenshot holds the tree's secret")
	}
	select {
	case pngDecodes <- struct{}{}:
	case <-ctx.Done():
		return newAPIError(http.StatusServiceUnavailable, "busy", "the server is checking another screenshot: save again")
	}
	defer func() { <-pngDecodes }()
	if _, err := png.Decode(bytes.NewReader(b)); err != nil {
		return newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a whole PNG")
	}
	return nil
}

// handleCaptures answers a page of the live captures, newest first:
// ?run=&task=&attempt= (a run's, a task's of it, an attempt's), ?ticket=
// (the captures a send was sent to), ?after= (a page's next), ?limit= (1
// to 100, 100 when absent): {captures, next?}, each with its first
// annotation alone, its note cut to 200 characters.
func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	q, err := readQuery(r, "run", "task", "attempt", "ticket", "after", "limit")
	var f dispatch.CaptureFilter
	var after string
	limit := dispatch.MaxCapturePage
	if err == nil {
		f.Run, err = q.one("run")
	}
	if err == nil {
		f.Task, err = q.one("task")
	}
	if err == nil {
		f.Attempt, err = q.count("attempt")
	}
	if err == nil {
		f.Ticket, err = q.one("ticket")
	}
	if err == nil {
		after, err = q.one("after")
	}
	if l, lerr := q.count("limit"); err == nil && lerr == nil && l != 0 {
		limit = l
	} else if err == nil {
		err = lerr
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "a page of captures takes run, task, attempt, ticket, after and limit, each once")
		return
	}
	page, err := ds.Captures(r.Context(), f, after, limit)
	if err != nil {
		s.dispatchError(w, "capture", err)
		return
	}
	writeDispatch(w, http.StatusOK, page)
}

// handleCapture answers a capture with its annotations and its sends; a
// deleted one is its tombstone.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	c, err := ds.Capture(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "capture", err)
		return
	}
	writeDispatch(w, http.StatusOK, c)
}

// handleCaptureImage serves a live capture's screenshot as an image and
// nothing else: Content-Type image/png, nosniff, a sandbox with no
// permission at all (Content-Security-Policy: sandbox), its SHA-256 in
// X-Conductor-SHA256. 404 not_found without one.
func (s *Server) handleCaptureImage(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchStore(w)
	if !ok {
		return
	}
	id, ok := captureID(w, r)
	if !ok || !noQuery(w, r) {
		return
	}
	c, f, err := ds.OpenCaptureImage(r.Context(), id)
	if err != nil {
		s.dispatchError(w, "capture", err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Content-Disposition", "inline")
	h.Set("X-Conductor-SHA256", c.Image.SHA256)
	http.ServeContent(w, r, "", time.Time{}, f)
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
	read, _, err := s.trees.read(r.Context(), wp.Dir, nil)
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
