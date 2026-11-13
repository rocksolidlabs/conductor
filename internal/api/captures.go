package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// A person's captures of a preview over HTTP (Program item PV1,
// docs/plans/browser-evidence.md): a capture's create, its reads, its
// edit, its delete and its screenshot, kept in Dispatch's store
// (dispatch.CreateCapture and the rest). Every route is the admin's.
//
// A write goes in one order. Its gate (readCaptureCreate,
// readCaptureEdit, readCaptureDelete) reads the body bounded as a whole
// and in each part, decodes it once with no member unknown, refuses in
// fixed words and takes the digest of what was decoded, the operation's
// content. Then every string the request carries is scrubbed of the
// secret of every tree the engine holds one for (scrubSecrets), each text
// of an element's report is made storable (dispatch.ReportText), and the
// operation is answered by its receipt when it was seen
// (dispatch.Store.Replay). A create is then checked whole by the store's
// own pure check (dispatch.CheckNewCapture): a request it refuses goes to
// the store with no facts, which refuses it by the same check and keeps
// its receipt, and no tree is read for it. Only then are the screenshot
// checked, the preview resolved, the page's origin compared with it and
// the tree read (captureFacts), and the store's create made.

const (
	// maxCaptureJSON bounds a create's capture part and an edit's body.
	maxCaptureJSON = 2 << 20
	// maxCaptureBody bounds a create's body: its capture part, its
	// screenshot and their envelope.
	maxCaptureBody = maxCaptureJSON + dispatch.MaxCaptureImage + 64<<10
)

// routeCaptures adds the capture routes, each the admin's and a
// switchyard's 403.
func (s *Server) routeCaptures(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/captures", s.requireAdmin(s.launchingAny(s.handleCaptureCreate)))
	mux.HandleFunc("GET /api/captures", s.requireAdmin(s.launchingAny(s.handleCaptures)))
	mux.HandleFunc("GET /api/captures/{id}", s.requireAdmin(s.launchingAny(s.handleCapture)))
	mux.HandleFunc("GET /api/captures/{id}/image", s.requireAdmin(s.launchingAny(s.handleCaptureImage)))
	mux.HandleFunc("POST /api/captures/{id}/edit", s.requireAdmin(s.launchingAny(s.handleCaptureEdit)))
	mux.HandleFunc("POST /api/captures/{id}/delete", s.requireAdmin(s.launchingAny(s.handleCaptureDelete)))
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

// liveSecrets is the secret of every tree the engine holds one for now
// (crew.Engine.TreeSecrets), whatever its preview's state.
func (s *Server) liveSecrets() []string {
	if s.runs == nil {
		return nil
	}
	return s.runs.TreeSecrets()
}

// handleCaptureCreate keeps a capture (capture.create), in the order the
// file's comment gives: the gate, the scrub, the reports' texts made
// storable, a resend answered by its receipt, the store's pure check (a
// refusal the store's own), the screenshot checked whole, the preview
// resolved (404 no_preview when it is not open now) and the page's
// origin compared with it, the tree read, and the store's create. 201
// {receipt, capture}; 200 for a repeat; a refusal as answerCapture
// answers it.
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
	// Each report's measure as the bridge cut it, before anything changes
	// it.
	measured := make([]int, len(req.body.Annotations))
	for i, a := range req.body.Annotations {
		if a.Element != nil {
			measured[i] = proto.PreviewMeasure(*a.Element)
		}
	}
	scrubSecrets(&req.body, s.liveSecrets())
	req.op.By = req.body.ByName
	n := req.body.NewCapture
	n.Annotations = make([]dispatch.NewAnnotation, len(req.body.Annotations))
	for i, a := range req.body.Annotations {
		n.Annotations[i] = dispatch.NewAnnotation{Kind: a.Kind, Note: a.Note, Rect: a.Rect, Element: storeElement(a.Element, measured[i]), Observed: a.Observed}
	}
	ctx, cancel := opContext(r)
	defer cancel()
	rc, found, err := ds.Replay(ctx, req.op, dispatch.IntentCaptureCreate, captureArgs{Received: req.digest})
	if err != nil || found {
		s.answerCapture(w, ctx, ds, rc, err, true, "")
		return
	}
	var image io.Reader
	if req.image != nil {
		image = bytes.NewReader(req.image)
	}
	if dispatch.CheckNewCapture(n, req.image != nil) != nil {
		rc, err = ds.CreateCapture(ctx, req.op, req.digest, n, dispatch.CaptureFacts{}, image)
		s.answerCapture(w, ctx, ds, rc, err, true, "")
		return
	}
	if req.image != nil {
		if aerr := checkScreenshot(ctx, req.image, s.liveSecrets()); aerr != nil {
			writeAPIError(w, aerr)
			return
		}
	}
	facts, aerr := s.captureFacts(ctx, ds, req.body.From, n)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	// What the server read, and the request again, scrubbed of every
	// secret held now: a tree made since the first scrub among them.
	secrets := s.liveSecrets()
	scrubSecrets(&facts, secrets)
	scrubSecrets(&n, secrets)
	rc, err = ds.CreateCapture(ctx, req.op, req.digest, n, facts, image)
	s.answerCapture(w, ctx, ds, rc, err, true, "")
}

// handleCaptureEdit edits a live capture (capture.edit): the gate, the
// edit scrubbed of every tree's secret held now, a resend answered by its
// receipt, and the store's edit, which checks it first as it is handed in
// (dispatch.CheckCaptureEdit). 200 {receipt, capture}.
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
	scrubSecrets(&req.body, s.liveSecrets())
	req.op.By = req.body.ByName
	ctx, cancel := opContext(r)
	defer cancel()
	rc, found, err := ds.Replay(ctx, req.op, dispatch.IntentCaptureEdit, captureArgs{ID: id, Received: req.digest})
	if err != nil || found {
		s.answerCapture(w, ctx, ds, rc, err, false, id)
		return
	}
	rc, err = ds.EditCapture(ctx, req.op, req.digest, id, req.body.ExpectedRev, req.body.Annotations)
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
	scrubSecrets(&req.body, s.liveSecrets())
	req.op.By = req.body.ByName
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

// storeElement is the bridge's report in the store's shape, measured the
// report's measure as the bridge cut it: each text made storable
// (dispatch.ReportText, every string of the report, its type walked as
// the scrub walks it), the classes split at ASCII white space (the DOM's
// own separator), each member as the bridge gave it.
func storeElement(p *proto.ElementReport, measured int) *dispatch.ElementReport {
	if p == nil {
		return nil
	}
	eachText(reflect.ValueOf(p), dispatch.ReportText)
	classes := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r' })
	}
	e := &dispatch.ElementReport{Measured: measured, Tag: p.Tag, ID: p.ID, Classes: classes(p.Classes), Role: p.Role, Name: p.Name, Text: p.Text,
		Selector: p.Selector, Box: dispatch.Rect(p.Box), Attributes: p.Attributes, HTML: p.HTML, Neighbourhood: p.Neighbourhood, Styles: p.Styles,
		Cut: p.Cut, Hit: dispatch.ElementHit(p.Hit),
		State: dispatch.ElementState{Disabled: p.State.Disabled, Hidden: p.State.Hidden, AriaDisabled: p.State.AriaDisabled, AriaExpanded: p.State.AriaExpanded,
			PointerEvents: p.State.PointerEvents, Inert: p.State.Inert, Hovered: p.State.Hovered, Motion: int(p.State.Motion)}}
	for _, a := range p.Ancestors {
		e.Ancestors = append(e.Ancestors, dispatch.Ancestor{Tag: a.Tag, ID: a.ID, Classes: classes(a.Classes), Role: a.Role})
	}
	for _, h := range p.Hints {
		e.Hints = append(e.Hints, dispatch.Hint{How: h.How, File: h.File, Line: int(h.Line), Column: int(h.Column), Component: h.Component})
	}
	return e
}

// eachText sets every string v holds, however deep, to f of it.
func eachText(v reflect.Value, f func(string) string) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(f(v.String()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			eachText(v.Elem(), f)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			eachText(v.Field(i), f)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			eachText(v.Index(i), f)
		}
	}
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

// plainOrigin is an origin a refusal may quote: plain ASCII, a host's name
// or address and a port.
var plainOrigin = regexp.MustCompile(`^https?://([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*|\[[0-9a-f:.]{2,45}\]):[0-9]{1,5}$`)

// otherOrigin is the refusal of a page whose origin is not the attempt's
// preview's (want): it quotes the page's origin only when it is plain
// (plainOrigin) and holds no tree's secret, else it says so in fixed
// words.
func otherOrigin(page, want string, secrets []string) *apiError {
	quoted := page
	scrubSecrets(&quoted, secrets)
	words := "the page is of another origin than this attempt's preview " + want
	switch {
	case page == "":
		words = "the page is no http or https page, not this attempt's preview " + want
	case quoted == page && len(page) <= 300 && plainOrigin.MatchString(page):
		words = "the page is " + page + ", not this attempt's preview " + want
	}
	return newAPIError(http.StatusBadRequest, "invalid", words)
}

// captureFacts is what the server reads at a create, from what it holds
// and never from the request: for a link, the origin alone (its page of
// the link's origin); for an attempt, its worktree preview open now (404
// no_preview otherwise, a candidate's checkout among it), the page of
// that preview's origin (400 invalid), the tree's revision read now, the
// run's scope, and each element's sources made relative, checked and
// classed in the tree.
func (s *Server) captureFacts(ctx context.Context, ds *dispatch.Store, from captureFrom, n dispatch.NewCapture) (dispatch.CaptureFacts, *apiError) {
	page := pageOriginOf(n.URL)
	if from.Run == "" && from.Task == "" && from.Attempt == 0 && from.Seq == 0 {
		if from.URL == "" || page == "" || pageOriginOf(from.URL) != page {
			return dispatch.CaptureFacts{}, newAPIError(http.StatusBadRequest, "invalid", "a link's capture is of a page of the link's origin")
		}
		return dispatch.CaptureFacts{Origin: dispatch.OriginURL}, nil
	}
	if from.URL != "" || !crew.ValidRunID(from.Run) || !crew.ValidTaskID(from.Task) || from.Attempt < 1 {
		return dispatch.CaptureFacts{}, newAPIError(http.StatusBadRequest, "invalid", "from names an attempt (run, task, attempt) or a link (url), not both")
	}
	noPreview := newAPIError(http.StatusNotFound, "no_preview", "the attempt's preview is not open now")
	if from.Seq != 0 || s.runs == nil {
		return dispatch.CaptureFacts{}, noPreview
	}
	wp, ok := s.runs.WorktreePreview(from.Run, from.Task, from.Attempt)
	if !ok {
		return dispatch.CaptureFacts{}, noPreview
	}
	if want := pageOriginOf(wp.URL); page == "" || page != want {
		return dispatch.CaptureFacts{}, otherOrigin(page, want, s.liveSecrets())
	}
	var hints [][]dispatch.Hint
	for _, a := range n.Annotations {
		var hs []dispatch.Hint
		if a.Element != nil {
			hs = a.Element.Hints
		}
		hints = append(hints, hs)
	}
	read, sources, err := s.trees.readCapture(ctx, wp, hints)
	if read.Commit == "" {
		s.log.Warn("a capture's tree could not be read", "run", from.Run, "task", from.Task, "attempt", from.Attempt, "why", readFailure(err))
		return dispatch.CaptureFacts{}, newAPIError(http.StatusServiceUnavailable, "tree_unread", "the attempt's tree could not be read: save again")
	}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginAttempt, Run: from.Run, Task: from.Task, Attempt: from.Attempt, Tree: dispatch.TreeWorktree,
		Commit: read.Commit, Content: read.Content, Changed: read.Changed, ChangedPaths: storedPaths(read.Paths), Mutable: true,
		Where: cmpOr(wp.Where, "native"), Scope: s.captureScope(ctx, ds, from.Run), Sources: sources}
	return f, nil
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

// readCapture reads the worktree of wp now as the label's read does (its
// entry of the reads, crew.OpenTree, crew.TreeRevision) and, under the
// same entry's gate, each annotation's hints in it (captureSources). A
// read that ends past labelReadWait, fails after naming HEAD, finds the
// tree moved or ends once the stop has begun gives the commit alone and
// the error.
func (tr *treeReads) readCapture(ctx context.Context, wp crew.WorktreePreview, hints [][]dispatch.Hint) (crew.TreeRead, [][]dispatch.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, labelReadWait)
	defer cancel()
	e, err := tr.take(ctx, wp.Dir)
	if err != nil {
		return crew.TreeRead{}, nil, err
	}
	defer tr.give(e, true)
	t, err := crew.OpenTree(wp.Root, wp.Dir)
	if err != nil {
		return crew.TreeRead{}, nil, err
	}
	defer t.Close()
	read, err := crew.TreeRevision(ctx, t, e.ix)
	var sources [][]dispatch.Source
	if err == nil && slices.ContainsFunc(hints, func(hs []dispatch.Hint) bool { return len(hs) > 0 }) {
		sources, err = captureSources(ctx, t, e.ix, read.Commit, wp.Dir, hints)
	}
	if err == nil && !t.Still() {
		err = crew.ErrTreeMoved
	}
	if err == nil && tr.stopping() {
		err = errReadsStopped
	}
	if err != nil {
		return crew.TreeRead{Commit: read.Commit}, nil, err
	}
	return read, sources, nil
}

// captureSources is each annotation's hints checked in the tree t (made
// at dir), at its commit: a hint's file made relative to the tree
// (outside, kept as reported, when it is not in it), found when the tree
// holds it (looked up through the opened tree, no link followed out of
// it), classed tracked, untracked, ignored (crew.TreeClasses, asked about
// exactly those paths) or vendor (a path with a node_modules element), and
// ordered by class, the innermost first within each, as the capture
// document says.
func captureSources(ctx context.Context, t *crew.Tree, ix *crew.TreeIndex, commit, dir string, hints [][]dispatch.Hint) ([][]dispatch.Source, error) {
	root, err := t.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	bases := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != bases[0] {
		bases = append(bases, real)
	}
	rel := func(file string) (string, bool) {
		p := filepath.Clean(filepath.FromSlash(file))
		if !filepath.IsAbs(p) {
			return filepath.ToSlash(p), p != "." && filepath.IsLocal(p)
		}
		for _, b := range bases {
			if r, err := filepath.Rel(b, p); err == nil && r != "." && filepath.IsLocal(r) {
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
				if _, err := root.Lstat(filepath.FromSlash(p)); err == nil {
					src.Found = true
				}
				if !slices.Contains(inside, p) {
					inside = append(inside, p)
				}
			}
			out[i] = append(out[i], src)
		}
	}
	tracked, ignored, err := crew.TreeClasses(ctx, t, ix, commit, inside)
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

// pngDecodes holds the whole decode of a screenshot to one at a time: at
// most one 4,096-pixel image (128 MiB at 16 bits a channel) in memory.
var pngDecodes = make(chan struct{}, 1)

// checkScreenshot checks a screenshot's bytes before the store keeps
// them: a PNG by its bytes, its header and size first (png.DecodeConfig:
// each side 1 to dispatch.MaxCaptureImageSide, before a pixel is
// allocated); no text chunk among its chunks (pngText) and no tree's
// secret among its bytes; then the whole of it decoded (every chunk's CRC
// and every row), one decode at a time.
func checkScreenshot(ctx context.Context, b []byte, secrets []string) *apiError {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a PNG")
	}
	if most := dispatch.MaxCaptureImageSide; cfg.Width < 1 || cfg.Height < 1 || cfg.Width > most || cfg.Height > most {
		return newAPIError(http.StatusBadRequest, "invalid", fmt.Sprintf("the screenshot is %d by %d pixels; a side is 1 to %d", cfg.Width, cfg.Height, most))
	}
	if pngText(b) {
		return newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a plain screenshot: a PNG that carries text")
	}
	low := asciiLower(string(b))
	for _, secret := range secrets {
		if secret != "" && strings.Contains(low, asciiLower(secret)) {
			return newAPIError(http.StatusBadRequest, "invalid", "the screenshot holds a tree's secret")
		}
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

// pngText reports whether the PNG b carries a text chunk (tEXt, zTXt or
// iTXt), its chunks' types read in one pass up to its end (IEND); a chunk
// cut short ends the pass, and the whole decode refuses it.
func pngText(b []byte) bool {
	for i := 8; i+8 <= len(b); {
		switch string(b[i+4 : i+8]) {
		case "tEXt", "zTXt", "iTXt":
			return true
		case "IEND":
			return false
		}
		n := binary.BigEndian.Uint32(b[i:])
		if uint64(n) > uint64(len(b)-i-8) {
			return false
		}
		i += 12 + int(n)
	}
	return false
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
