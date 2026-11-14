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
	"regexp"
	"slices"
	"strconv"
	"strings"

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
// its receipt, and no tree is read for it. Only then is the screenshot
// decoded and the server's own encoding of it made (keptScreenshot), the
// preview resolved, the page's origin compared with it and the tree read
// (captureFacts), and the store's create made.

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

// opOf is the store's operation of r's principal, judged by the store's
// check of an operation (dispatch.Op.Check) as the request carried it,
// before anything of it is scrubbed, so that a display name is judged the
// same whatever secrets are held when it comes again (it is scrubbed later
// with the body, never longer). An id the scrub would change (it holds a
// part of one of secrets) is refused here, the one place an operation is
// made from a request, before any call to the store: no operation's row,
// receipt or reply carries it.
func opOf(r *http.Request, id, by string, secrets []string) (dispatch.Op, *apiError) {
	op := dispatch.Op{Actor: actorOf(r), ID: id, By: by}
	switch err := op.Check(); {
	case errors.Is(err, dispatch.ErrBadOpID):
		return dispatch.Op{}, badCapture("opId is 1 to 32 letters, digits, '.', '_', '~' or '-'")
	case err != nil:
		return dispatch.Op{}, badCapture(fmt.Sprintf("byName is a display name of at most %d characters", dispatch.MaxByName))
	case holdsSecret(id, secrets):
		return dispatch.Op{}, badCapture("opId holds a tree's secret")
	}
	return op, nil
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
// the digest of the capture as decoded with the screenshot's SHA-256 as
// uploaded; the operation as opOf makes it (secrets the trees' held now).
func readCaptureCreate(w http.ResponseWriter, r *http.Request, secrets []string) (captureRequest[captureCreateBody], *apiError) {
	var req captureRequest[captureCreateBody]
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return req, badCapture("a capture is multipart/form-data")
	}
	body := http.MaxBytesReader(w, r.Body, maxCaptureBody)
	mr := multipart.NewReader(body, params["boundary"])
	var capture []byte
	for i := 0; ; i++ {
		part, err := mr.NextPart()
		var tooLarge *http.MaxBytesError
		if err == io.EOF {
			// What follows the closing boundary counts against the body's
			// bound too.
			if _, err = io.Copy(io.Discard, body); err == nil {
				err = io.EOF
			}
		}
		switch {
		case err == io.EOF:
			if capture == nil {
				return req, badCapture("a capture's first part is capture")
			}
			aerr := decodeCapture(capture, &req.body)
			if aerr == nil {
				req.op, aerr = opOf(r, req.body.OpID, req.body.ByName, secrets)
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
func readCaptureEdit(w http.ResponseWriter, r *http.Request, secrets []string) (captureRequest[captureEditBody], *apiError) {
	var req captureRequest[captureEditBody]
	b, aerr := readJSONBody(w, r, maxCaptureJSON)
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName, secrets)
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
func readCaptureDelete(w http.ResponseWriter, r *http.Request, secrets []string) (captureRequest[captureDeleteBody], *apiError) {
	var req captureRequest[captureDeleteBody]
	b, aerr := readJSONBody(w, r, maxBody)
	if aerr == nil {
		aerr = decodeCapture(b, &req.body)
	}
	if aerr == nil {
		req.op, aerr = opOf(r, req.body.OpID, req.body.ByName, secrets)
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
// storable, a resend answered by its receipt, the screenshot's header,
// the store's look before a tree is read (a refusal the store's own), the
// screenshot decoded and encoded again (what the store keeps of it), the
// preview resolved (404 no_preview when it
// is not open now) and the page's origin compared with it, the tree read,
// and the store's create. 201 {receipt, capture}; 200 for a repeat; a
// refusal as answerCapture answers it.
func (s *Server) handleCaptureCreate(w http.ResponseWriter, r *http.Request) {
	ds, ok := s.dispatchWriter(w)
	if !ok {
		return
	}
	req, aerr := readCaptureCreate(w, r, s.liveSecrets())
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
	var img *dispatch.CaptureImage
	if req.image != nil {
		width, height, aerr := screenshotSize(req.image)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		img = &dispatch.CaptureImage{Width: width, Height: height}
	}
	// Everything the store judges without the tree's facts, before the
	// tree is read.
	if rc, refused, err := ds.CheckCaptureCreate(ctx, req.op, req.digest, n, img, isLink(req.body.From)); err != nil || refused {
		s.answerCapture(w, ctx, ds, rc, err, true, "")
		return
	}
	if req.image != nil {
		kept, aerr := keptScreenshot(ctx, req.image, s.liveSecrets())
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		image = bytes.NewReader(kept)
	}
	facts, aerr := s.captureFacts(ctx, ds, req.body.From, n)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	// The request again, scrubbed of every secret held now: a tree made
	// since the first scrub among them.
	scrubSecrets(&n, s.liveSecrets())
	rc, err = ds.CreateCapture(ctx, req.op, req.digest, n, facts, image)
	s.answerCapture(w, ctx, ds, rc, err, true, "")
}

// isLink is whether from names a link, not an attempt.
func isLink(from captureFrom) bool {
	return from.Run == "" && from.Task == "" && from.Attempt == 0 && from.Seq == 0
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
	req, aerr := readCaptureEdit(w, r, s.liveSecrets())
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
	req, aerr := readCaptureDelete(w, r, s.liveSecrets())
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
			capture = c.Answer()
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
// that preview's origin (400 invalid), the tree's revision read now by the
// label's read (treeReads.read) with, as its step, the top of the run's
// repository looked up and each element's sources made relative, checked
// and classed in the tree, and the run's scope. What the read gives
// decides the create, here alone: a read that
// gave no commit, or was refused (the tree moved, the stop began), is 503
// tree_unread and nothing is kept; one that named the commit and then
// failed (its time ran out, git failed) keeps the commit alone, no content,
// no changed paths and no sources, as the label answers it; sources git
// would not class are kept with their class unknown. Each is logged by its
// kind. What was read is scrubbed of the read's own tree's secret and of
// every secret held once the read is over.
func (s *Server) captureFacts(ctx context.Context, ds *dispatch.Store, from captureFrom, n dispatch.NewCapture) (dispatch.CaptureFacts, *apiError) {
	page := pageOriginOf(n.URL)
	if isLink(from) {
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
	run, held := s.runs.Get(from.Run)
	var sources [][]dispatch.Source
	var top string
	var unclassed, untopped error
	read, err := s.trees.read(ctx, wp, func(ctx context.Context, t *crew.Tree, ix *crew.TreeIndex, commit string) (err error) {
		// The repository's top is looked for here, in the read's time and
		// under what the stop cancels and waits for: no git of a capture
		// runs outside its read.
		if held {
			top, untopped = s.topOf(ctx, run.Cwd)
		}
		if slices.ContainsFunc(hints, func(hs []dispatch.Hint) bool { return len(hs) > 0 }) {
			sources, unclassed, err = captureSources(ctx, t, ix, commit, wp.Dir, hints)
		}
		return err
	})
	if s.afterCaptureRead != nil {
		s.afterCaptureRead()
	}
	ids := []any{"run", from.Run, "task", from.Task, "attempt", from.Attempt}
	switch {
	case read.Commit == "" || (err != nil && readFailure(err) == "refused"):
		s.log.Warn("a capture's tree could not be read: nothing is kept", append(ids, "why", readFailure(err))...)
		return dispatch.CaptureFacts{}, newAPIError(http.StatusServiceUnavailable, "tree_unread", "the attempt's tree could not be read: save again")
	case err != nil:
		s.log.Warn("a capture's tree was read to its commit alone: the capture is kept with it", append(ids, "why", readFailure(err))...)
		read, sources, unclassed = crew.TreeRead{Commit: read.Commit}, nil, nil
	}
	if unclassed != nil {
		s.log.Warn("a capture's sources are kept with their class unknown", append(ids, "why", readFailure(unclassed))...)
	}
	if untopped != nil {
		s.log.Warn("a capture's repository is not named", append(ids, "why", readFailure(untopped))...)
	}
	f := dispatch.CaptureFacts{Origin: dispatch.OriginAttempt, Run: from.Run, Task: from.Task, Attempt: from.Attempt, Tree: dispatch.TreeWorktree,
		Commit: read.Commit, Content: read.Content, Changed: read.Changed, ChangedPaths: storedPaths(read.Paths), Mutable: true,
		Where: cmpOr(wp.Where, "native"), Scope: s.captureScope(ctx, ds, from.Run, run, held, top), Sources: sources}
	// The names the server read (git's ids aside), scrubbed of the read's
	// own tree's secret beside every secret held now.
	secrets := append(s.liveSecrets(), wp.Secret)
	scrubSecrets(&f.ChangedPaths, secrets)
	scrubSecrets(&f.Sources, secrets)
	scrubSecrets(&f.Scope, secrets)
	return f, nil
}

// captureScope is the run's crew (run, held when the engine holds it),
// its repository (top, the top of its working directory as the capture's
// read looked it up) and the project of the ticket it was launched from
// (the run's own, crew.Run.Ticket), else of the live ticket it is linked
// to (dispatch.Store.RunTicket); what cannot be read is left empty. It
// starts no git.
func (s *Server) captureScope(ctx context.Context, ds *dispatch.Store, runID string, run crew.Run, held bool, top string) dispatch.CaptureScope {
	sc := dispatch.CaptureScope{Repo: top}
	if held {
		sc.Crew = run.CrewID
		if run.Ticket != nil {
			sc.Project = projectOfKey(run.Ticket.Key)
		}
	}
	if tk, ok, err := ds.RunTicket(ctx, runID); sc.Project == "" && err == nil && ok && tk.DeletedAt.IsZero() {
		sc.Project = projectOfKey(tk.Key)
	}
	return sc
}

// topOf is the top of the working tree dir is in (gitcli.TopOf), or what
// the test's seam answers (repoTop).
func (s *Server) topOf(ctx context.Context, dir string) (string, error) {
	if s.repoTop != nil {
		return s.repoTop(ctx, dir)
	}
	return gitcli.TopOf(ctx, dir)
}

// captureSources is each annotation's hints checked in the tree t (made
// at dir), at its commit, the step of the capture's read: a hint's file
// made relative to the tree (outside, kept as reported, when it is not in
// it), found when the tree holds it (looked up through the opened tree, no
// link followed out of it), classed tracked, untracked, ignored
// (crew.TreeClasses, asked about exactly those paths whose folders are
// folders, no link among them: throughLink) or vendor (a path with a
// node_modules element), unknown when git was not asked or did not answer
// (unclassed says why), and ordered by class, the innermost first within
// each, as the capture document says. A form of dir with its links
// resolved is a base of the tree only when it is the opened folder itself
// (os.SameFile); dir itself is the path the engine made the tree at.
func captureSources(ctx context.Context, t *crew.Tree, ix *crew.TreeIndex, commit, dir string, hints [][]dispatch.Hint) (out [][]dispatch.Source, unclassed, err error) {
	root, err := t.OpenRoot()
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	bases := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != bases[0] {
		opened, oerr := root.Stat(".")
		if fi, err := os.Stat(real); err == nil && oerr == nil && os.SameFile(fi, opened) {
			bases = append(bases, real)
		}
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
	out = make([][]dispatch.Source, len(hints))
	var asked []string
	for i, hs := range hints {
		for _, h := range hs {
			p, in := rel(h.File)
			src := dispatch.Source{Path: p, Line: h.Line, Column: h.Column, Component: h.Component, How: h.How, Outside: !in}
			if in {
				if _, err := root.Lstat(filepath.FromSlash(p)); err == nil {
					src.Found = true
				}
				// A path that begins with a colon is not handed to git,
				// which reads a leading colon as a pathspec's magic: its
				// class is unknown.
				if !slices.Contains(asked, p) && !strings.HasPrefix(p, ":") && !throughLink(root, p) {
					asked = append(asked, p)
				}
			}
			out[i] = append(out[i], src)
		}
	}
	tracked, ignored, unclassed := crew.TreeClasses(ctx, t, ix, commit, asked)
	for i := range out {
		for j := range out[i] {
			src := &out[i][j]
			switch {
			case slices.Contains(strings.Split(src.Path, "/"), "node_modules"):
				src.Class = "vendor"
			case src.Outside:
				src.Class = "untracked"
			case unclassed != nil || !slices.Contains(asked, src.Path):
				src.Class = dispatch.SourceUnknown
			case tracked[src.Path]:
				src.Class = "tracked"
			case ignored[src.Path]:
				src.Class = "ignored"
			default:
				src.Class = "untracked"
			}
		}
		slices.SortStableFunc(out[i], func(a, b dispatch.Source) int {
			return slices.Index(dispatch.SourceClasses, a.Class) - slices.Index(dispatch.SourceClasses, b.Class)
		})
	}
	return out, unclassed, nil
}

// throughLink is whether a folder on the path p (slash-separated, in the
// tree root) is a link or no folder, each looked at from the top down
// without following it; a folder that is not there ends the look.
func throughLink(root *os.Root, p string) bool {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		fi, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i], "/")))
		if err != nil {
			return false
		}
		if !fi.IsDir() {
			return true
		}
	}
	return false
}

// pngDecodes holds the decode of a screenshot and its encoding to one at a
// time: at most one 4,096-pixel image (128 MiB at 16 bits a channel) and
// its kept file in memory.
var pngDecodes = make(chan struct{}, 1)

// screenshotSize reads a screenshot's header, before a pixel is allocated
// (png.DecodeConfig): a PNG, each side 1 to dispatch.MaxCaptureImageSide.
func screenshotSize(b []byte) (int, int, *apiError) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a PNG")
	}
	if most := dispatch.MaxCaptureImageSide; cfg.Width < 1 || cfg.Height < 1 || cfg.Width > most || cfg.Height > most {
		return 0, 0, newAPIError(http.StatusBadRequest, "invalid", fmt.Sprintf("the screenshot has a side that is not 1 to %d pixels", most))
	}
	return cfg.Width, cfg.Height, nil
}

// pngKept is the server's encoding of a screenshot's pixels: zlib's
// default level, at which the kept file of Chromium's screenshots of real
// pages, 1,024 to 4,096 pixels on the long side, was no larger than
// Chromium's own.
var pngKept = png.Encoder{CompressionLevel: png.DefaultCompression}

// keptScreenshot is what the store keeps, hashes and serves of a
// screenshot, its header read first (screenshotSize): the server's own PNG
// encoding (pngKept) of the image the upload decodes to, and nothing else
// of the upload (no chunk beside the pixels', nothing after their end),
// the decode and the encoding made together one at a time (pngDecodes).
// Refused, in fixed words: an upload that is not a whole PNG (400), a
// kept file past dispatch.MaxCaptureImage (413 bound, the encoding cut
// there), and one that holds a part of a tree's secret as the scrub finds
// one (400).
func keptScreenshot(ctx context.Context, b []byte, secrets []string) ([]byte, *apiError) {
	select {
	case pngDecodes <- struct{}{}:
	case <-ctx.Done():
		return nil, newAPIError(http.StatusServiceUnavailable, "busy", "the server is checking another screenshot: save again")
	}
	defer func() { <-pngDecodes }()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, newAPIError(http.StatusBadRequest, "invalid", "the screenshot is not a whole PNG")
	}
	kept := &boundedBuffer{max: dispatch.MaxCaptureImage}
	if err := pngKept.Encode(kept, img); err != nil {
		return nil, newAPIError(http.StatusRequestEntityTooLarge, "bound", fmt.Sprintf("the screenshot is more than %d MiB as the server keeps it", dispatch.MaxCaptureImage>>20))
	}
	if holdsSecret(kept.String(), secrets) {
		return nil, newAPIError(http.StatusBadRequest, "invalid", "the screenshot holds a tree's secret")
	}
	return kept.Bytes(), nil
}

// boundedBuffer is a buffer that takes at most max bytes: a write past it
// fails and keeps nothing more.
type boundedBuffer struct {
	bytes.Buffer
	max int
}

// errPastBound is a write past a boundedBuffer's bound.
var errPastBound = errors.New("past the bound")

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errPastBound
	}
	return b.Buffer.Write(p)
}

// handleCaptures answers a page of the live captures, newest first:
// ?run=&task=&attempt= (a run's, a task's of it, an attempt's), ?ticket=
// (the captures a send was sent to), ?after= (a page's next), ?limit= (1
// to 100, 100 when absent), each at most once, attempt and limit in
// digits alone (pageNumber): {captures, next?}, each with its first
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
	// An attempt is numbered from 1, and a limit given is the page's
	// (the store refuses one that is not 1 to 100): only one absent is
	// read as none.
	given := false
	if err == nil {
		if f.Attempt, given, err = pageNumber(q, "attempt"); err == nil && given && f.Attempt < 1 {
			err = errors.New("an attempt is numbered from 1")
		}
	}
	if err == nil {
		f.Ticket, err = q.one("ticket")
	}
	if err == nil {
		after, err = q.one("after")
	}
	if err == nil {
		var n int
		if n, given, err = pageNumber(q, "limit"); given {
			limit = n
		}
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

// pageNumber is the list's parameter name read once and whole: absent
// (given false), or given once as decimal digits alone (no sign, not
// empty) within an int; refused otherwise.
func pageNumber(q dispatchQuery, name string) (n int, given bool, err error) {
	vs := q[name]
	if len(vs) == 0 {
		return 0, false, nil
	}
	if len(vs) == 1 && vs[0] != "" && strings.Trim(vs[0], "0123456789") == "" {
		if n, err = strconv.Atoi(vs[0]); err == nil {
			return n, true, nil
		}
	}
	return 0, true, fmt.Errorf("%s is given once, in digits", name)
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
	writeDispatch(w, http.StatusOK, c.Answer())
}

// handleCaptureImage serves a live capture's screenshot as an image and
// nothing else: Content-Type image/png, nosniff, a sandbox with no
// permission at all (Content-Security-Policy: sandbox), its SHA-256 in
// X-Conductor-SHA256, the whole file whatever range the request names
// (200, never a part). 404 not_found without one.
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
	if fi, err := f.Stat(); err == nil {
		h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
