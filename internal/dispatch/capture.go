package dispatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A capture is a person's evidence about work (Program item PV1,
// docs/plans/browser-evidence.md): what a preview showed at one moment, a
// screenshot when there is one, and up to MaxCaptureAnnotations
// annotations, each an element the page's bridge reported or a region of
// the screenshot, with the person's note. The client's words (NewCapture)
// and the server's reads of the tree (CaptureFacts) are checked here
// against every bound, the store working out itself what it can from
// them: the drift between the selection's read and the save's, what the
// page's declared revision says of the tree (serving), the changed paths'
// cut and how a retake is related to the capture it retakes. A send
// freezes the capture's document (CaptureDocument, conductor.capture/1) as
// a content file.

// CaptureSchemaName is a capture document's schema.
const CaptureSchemaName = "conductor.capture/1"

// A capture's words: how it was taken, where from, what tree, how its
// image was had, how a retake is related, an annotation's kind, a
// source's class, what the page serves, a send's kind and state.
const (
	CaptureViaWeb      = "web"
	CaptureViaDesktop  = "desktop"
	OriginAttempt      = "attempt"
	OriginCandidate    = "candidate"
	OriginURL          = "url"
	TreeWorktree       = "worktree"
	TreePreview        = "preview"
	ImageDesktop       = "desktop"
	ImageTab           = "tab"
	ImageNone          = "none"
	RetakeRun          = "run"
	RetakeTicket       = "ticket"
	RetakeRepo         = "repo"
	AnnotationElement  = "element"
	AnnotationRegion   = "region"
	ServingDeclared    = "declared"
	ServingStale       = "stale"
	ServingUnconfirmed = "unconfirmed"
	// SendReview and SendDecision are reserved: the store refuses them
	// until the items that own reviews and decisions lift it (the plan's
	// PR-10 and PR-11).
	SendTicket    = "ticket"
	SendBrakeman  = "brakeman"
	SendReview    = "review"
	SendDecision  = "decision"
	SendPending   = "pending"
	SendSent      = "sent"
	SendRefused   = "refused"
	SendCancelled = "cancelled"
	// holderCapture is the holder of a live capture's own file.
	holderCapture = "capture"
)

// ViewportModes is a viewport's device modes; SourceClasses a source's
// classes in the order a capture's sources take (the app's own first).
var (
	ViewportModes = []string{"fit", "desktop", "tablet", "phone"}
	SourceClasses = []string{"tracked", "untracked", "ignored", "vendor"}
)

// ErrBadReceived is a capture operation whose request digest is no
// SHA-256 (RequestDigest): answered as a bad op id is, nothing written.
var ErrBadReceived = errors.New("dispatch: a capture's operation names its request's SHA-256")

// ErrStaleCapture is a document asked of a revision that is not the
// capture's now: an earlier one is read from a send that froze it.
var ErrStaleCapture = errors.New("dispatch: the capture is at another revision now")

// declaredPattern is what a page declares it serves: a commit, and its
// content's tree id when the tree has changes, in hex.
var declaredPattern = regexp.MustCompile(`^[0-9a-f]{7,64}(\+[0-9a-f]{7,64})?$`)

// Rect is a box in pixels: an element's in the page, a rectangle's in the
// screenshot.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Viewport is the frame a capture was taken in: its device mode, its size
// in CSS pixels, its scale to the pane, the device pixel ratio, the
// colour scheme, the browser and the frame's scroll.
type Viewport struct {
	Mode        string  `json:"mode"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Scale       float64 `json:"scale"`
	DPR         float64 `json:"dpr"`
	ColorScheme string  `json:"colorScheme"`
	UserAgent   string  `json:"userAgent"`
	ScrollX     float64 `json:"scrollX"`
	ScrollY     float64 `json:"scrollY"`
}

// ElementReport is what the bridge read of an element (the plan's
// "element report"), at most MaxElementReport bytes as encoded (measure).
// Attributes and Styles are name and value pairs.
type ElementReport struct {
	Tag           string       `json:"tag"`
	ID            string       `json:"id,omitempty"`
	Classes       []string     `json:"classes,omitempty"`
	Role          string       `json:"role,omitempty"`
	Name          string       `json:"name,omitempty"`
	Text          string       `json:"text,omitempty"`
	Selector      string       `json:"selector"`
	Box           Rect         `json:"box"`
	Attributes    [][2]string  `json:"attributes,omitempty"`
	State         ElementState `json:"state"`
	Hit           ElementHit   `json:"hit"`
	HTML          string       `json:"html,omitempty"`
	Neighbourhood string       `json:"neighbourhood,omitempty"`
	Ancestors     []Ancestor   `json:"ancestors,omitempty"`
	Styles        [][2]string  `json:"styles,omitempty"`
	Hints         []Hint       `json:"hints,omitempty"`
	Cut           []string     `json:"cut,omitempty"`
}

// ElementState is an element's state at the read.
type ElementState struct {
	Disabled      bool   `json:"disabled"`
	Hidden        bool   `json:"hidden"`
	AriaDisabled  string `json:"ariaDisabled,omitempty"`
	AriaExpanded  string `json:"ariaExpanded,omitempty"`
	PointerEvents string `json:"pointerEvents,omitempty"`
	Inert         bool   `json:"inert"`
	Hovered       bool   `json:"hovered"`
	Motion        int    `json:"motion"`
}

// ElementHit is what a click at the element's centre reaches.
type ElementHit struct {
	Self     bool   `json:"self"`
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

// Ancestor is one of an element's ancestors, innermost first.
type Ancestor struct {
	Tag     string   `json:"tag"`
	ID      string   `json:"id,omitempty"`
	Classes []string `json:"classes,omitempty"`
	Role    string   `json:"role,omitempty"`
}

// Hint is what the page's dev build says drew an element.
type Hint struct {
	How       string `json:"how"`
	File      string `json:"file"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Component string `json:"component,omitempty"`
}

// RevisionLabel is a tree's revision as the pane read it: its commit and
// its content's tree id.
type RevisionLabel struct {
	Commit  string `json:"commit"`
	Content string `json:"content"`
}

// TreeRead is the pane's read of the tree at the selection (NewCapture's
// Label), kept as the capture's drift when the save's read differs.
type TreeRead struct {
	Commit  string    `json:"commit"`
	Content string    `json:"content"`
	Changed int       `json:"changed"`
	ReadAt  time.Time `json:"readAt"`
}

// Observed is the moment an element was selected at, as the bridge and
// the pane read it: every element of a capture names the capture's own.
// Stable is the pixels' verdict, nil without pixels.
type Observed struct {
	At       time.Time     `json:"at"`
	URL      string        `json:"url"`
	Title    string        `json:"title"`
	Viewport Viewport      `json:"viewport"`
	Epoch    int64         `json:"epoch"`
	Revision string        `json:"revision,omitempty"`
	Stable   *bool         `json:"stable,omitempty"`
	Label    RevisionLabel `json:"label"`
}

// Source is a hint the API checked in the tree: its path relative to the
// tree (or as reported, Outside), whether it is there at the save, and
// its class (SourceClasses).
type Source struct {
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Component string `json:"component,omitempty"`
	How       string `json:"how"`
	Found     bool   `json:"found"`
	Outside   bool   `json:"outside,omitempty"`
	Class     string `json:"class"`
}

// NewAnnotation is an annotation as the client sends it: an element with
// its report and the moment it was observed at, or a region of the
// screenshot.
type NewAnnotation struct {
	Kind     string         `json:"kind"`
	Note     string         `json:"note"`
	Rect     Rect           `json:"rect"`
	Element  *ElementReport `json:"element,omitempty"`
	Observed *Observed      `json:"observed,omitempty"`
}

// NewCapture is what capture.create takes of the client: the page and
// its moment (URL, title, viewport, the bridge's epoch, the revision the
// page declared and the pane's read of the tree at the selection), the
// screenshot's scale or why there is none, the capture it retakes, and
// the annotations in their order.
type NewCapture struct {
	Via         string          `json:"via"`
	URL         string          `json:"url"`
	Title       string          `json:"title"`
	Viewport    Viewport        `json:"viewport"`
	Epoch       int64           `json:"epoch"`
	Revision    string          `json:"revision,omitempty"`
	Label       TreeRead        `json:"label"`
	SelectedAt  time.Time       `json:"selectedAt"`
	ImageScale  float64         `json:"imageScale,omitempty"`
	ImageWhy    string          `json:"imageWhy,omitempty"`
	RetakeOf    string          `json:"retakeOf,omitempty"`
	Annotations []NewAnnotation `json:"annotations"`
}

// CaptureScope is the run's crew, repository and the project of the
// ticket it was launched from, kept so a grant can judge a capture after
// the run has gone.
type CaptureScope struct {
	Crew    string `json:"crew,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Project string `json:"project,omitempty"`
}

// CaptureFacts is what the API read at the save, stored and never
// fingerprinted: the origin and its tree (commit, content, the changed
// paths counted and listed, mutable, the candidate an intact checkout
// is, where it ran), the run's scope, and each annotation's checked
// sources in the annotations' order (none at all, or one list each).
type CaptureFacts struct {
	Origin       string       `json:"origin"`
	Run          string       `json:"run,omitempty"`
	Task         string       `json:"task,omitempty"`
	Attempt      int          `json:"attempt,omitempty"`
	Tree         string       `json:"tree,omitempty"`
	Commit       string       `json:"commit,omitempty"`
	Changed      int          `json:"changed,omitempty"`
	ChangedPaths []string     `json:"changedPaths,omitempty"`
	Content      string       `json:"content,omitempty"`
	Mutable      bool         `json:"mutable,omitempty"`
	Candidate    string       `json:"candidate,omitempty"`
	Where        string       `json:"where,omitempty"`
	Scope        CaptureScope `json:"scope"`
	Sources      [][]Source   `json:"sources,omitempty"`
}

// AnnotationEdit is one annotation of an edit's list, in its new place:
// an annotation the capture holds named by K, its place in the live set
// from 1, with its note (and a region's rectangle), or a new region (K
// 0). An element is never new: Element set is refused.
type AnnotationEdit struct {
	K       int            `json:"k"`
	Kind    string         `json:"kind"`
	Note    string         `json:"note"`
	Rect    Rect           `json:"rect"`
	Element *ElementReport `json:"element,omitempty"`
}

// Destination is where a send goes as the person named it: a new ticket
// (Project and Title), an existing one (Key), or Brakeman.
type Destination struct {
	Kind    string `json:"kind"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title,omitempty"`
	Key     string `json:"key,omitempty"`
}

// SendFacts is what the API made of a send: for Brakeman the request as
// the desk will be asked it (JSON, at most MaxSendRequest bytes), which a
// recovery replays whole.
type SendFacts struct {
	Request json.RawMessage `json:"request,omitempty"`
}

// Annotation is one annotation of a capture as a set names it: K its
// place from 1 (0 in a version's content, which is the rest).
type Annotation struct {
	K        int            `json:"k,omitempty"`
	Kind     string         `json:"kind"`
	Note     string         `json:"note"`
	Rect     Rect           `json:"rect"`
	Element  *ElementReport `json:"element,omitempty"`
	Observed *Observed      `json:"observed,omitempty"`
	Sources  []Source       `json:"sources,omitempty"`
}

// Serving is what the page's declared revision says of the tree.
type Serving struct {
	State string `json:"state"`
	Page  string `json:"page,omitempty"`
}

// CaptureOrigin is where a capture was taken, as its document says.
type CaptureOrigin struct {
	Kind         string    `json:"kind"`
	Run          string    `json:"run,omitempty"`
	Task         string    `json:"task,omitempty"`
	Attempt      int       `json:"attempt,omitempty"`
	Tree         string    `json:"tree,omitempty"`
	Commit       string    `json:"commit,omitempty"`
	Changed      int       `json:"changed"`
	ChangedPaths []string  `json:"changedPaths"`
	ChangedCut   bool      `json:"changedCut"`
	Content      string    `json:"content,omitempty"`
	Mutable      bool      `json:"mutable"`
	Candidate    string    `json:"candidate,omitempty"`
	Where        string    `json:"where,omitempty"`
	Drift        *TreeRead `json:"drift,omitempty"`
	Serving      Serving   `json:"serving"`
}

// CaptureImage is a capture's screenshot, or why there is none.
type CaptureImage struct {
	SHA256 string  `json:"sha256,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	Bytes  int64   `json:"bytes,omitempty"`
	How    string  `json:"how"`
	Why    string  `json:"why,omitempty"`
	Scale  float64 `json:"scale,omitempty"`
}

// CaptureDocument is the JSON a send freezes and a ticket's attachment
// carries (conductor.capture/1), at most MaxCaptureDocument bytes.
type CaptureDocument struct {
	Schema      string        `json:"schema"`
	ID          string        `json:"id"`
	Rev         int64         `json:"rev"`
	URL         string        `json:"url"`
	Title       string        `json:"title"`
	Viewport    Viewport      `json:"viewport"`
	SelectedAt  time.Time     `json:"selectedAt"`
	SavedAt     time.Time     `json:"savedAt"`
	Origin      CaptureOrigin `json:"origin"`
	Image       CaptureImage  `json:"image"`
	Annotations []Annotation  `json:"annotations"`
	RetakeOf    string        `json:"retakeOf,omitempty"`
	RetakeVia   string        `json:"retakeVia,omitempty"`
}

// SetPointer names a capture's live annotation set: its rev and its id.
type SetPointer struct {
	Rev int64  `json:"rev"`
	Set string `json:"set"`
}

// Capture is a capture as a read gives it: its document at its revision
// now, who made it and how, the bridge's epoch, the run's scope, its live
// set and its sends. A deleted capture is its id, rev, times and sends
// alone: its words are gone with it.
type Capture struct {
	CaptureDocument
	CreatedAt time.Time     `json:"createdAt"`
	CreatedBy string        `json:"createdBy,omitempty"`
	Actor     string        `json:"actor,omitempty"`
	Via       string        `json:"via,omitempty"`
	Epoch     int64         `json:"epoch,omitempty"`
	Scope     CaptureScope  `json:"scope"`
	Set       SetPointer    `json:"set"`
	DeletedAt time.Time     `json:"deletedAt,omitzero"`
	Sends     []CaptureSend `json:"sends,omitempty"`
}

// SendOutcome is what became of a send: sent (with its target and the
// attachments it added), refused (why) or cancelled.
type SendOutcome struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Target string    `json:"target,omitempty"`
	Files  []string  `json:"files,omitempty"`
	Why    string    `json:"why,omitempty"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	OpID   string    `json:"opId"`
}

// SendCancel is a person's cancel of a send, kept before anything acts on
// it.
type SendCancel struct {
	OpID  string    `json:"opId"`
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
}

// CaptureSend is a send as a read gives it: its input, its state by
// sendState with the deciding outcome's target, files and why, its
// outcomes and its cancels. A list leaves its request out.
type CaptureSend struct {
	ID        string          `json:"id"`
	Capture   string          `json:"capture"`
	Rev       int64           `json:"rev"`
	Kind      string          `json:"kind"`
	Asked     json.RawMessage `json:"asked"`
	Digest    string          `json:"digest"`
	Request   json.RawMessage `json:"request,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	CreatedBy string          `json:"createdBy,omitempty"`
	Actor     string          `json:"actor"`
	OpID      string          `json:"opId"`
	State     string          `json:"state"`
	Target    string          `json:"target,omitempty"`
	Files     []string        `json:"files,omitempty"`
	Why       string          `json:"why,omitempty"`
	Outcomes  []SendOutcome   `json:"outcomes"`
	Cancels   []SendCancel    `json:"cancels"`
}

// RequestDigest is a capture operation's request as the API received it,
// before it scrubs anything: the SHA-256 of body re-encoded as JSON with
// the image's SHA-256 after it ("" for none). It is the operation's
// content (its fingerprint), so a resend is answered by its receipt
// whatever the tree has become.
func RequestDigest(body any, image string) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(b)
	h.Write([]byte{0})
	h.Write([]byte(image))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sendState is a send's state, the one projection every read makes of
// its outcomes, the same in any order of arrival: none is pending; any
// sent makes it sent, the earliest sent deciding its target and files;
// else the earliest refusal or cancellation, by (at, actor, op_id, id).
// It returns the deciding outcome, nil for pending.
func sendState(outcomes []SendOutcome) (string, *SendOutcome) {
	earlier := func(a, b *SendOutcome) bool {
		if b == nil {
			return true
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.Actor+"\x00"+a.OpID+"\x00"+a.ID < b.Actor+"\x00"+b.OpID+"\x00"+b.ID
	}
	var sent, other *SendOutcome
	for i := range outcomes {
		o := &outcomes[i]
		switch {
		case o.Kind == SendSent && earlier(o, sent):
			sent = o
		case (o.Kind == SendRefused || o.Kind == SendCancelled) && earlier(o, other):
			other = o
		}
	}
	switch {
	case sent != nil:
		return SendSent, sent
	case other != nil:
		return other.Kind, other
	}
	return SendPending, nil
}

// measure is v's size as the store keeps it: its JSON with HTML escaping
// off (encodeValue), the bridge's measure too.
func measure(v any) int {
	s, err := encodeValue(v)
	if err != nil {
		return math.MaxInt
	}
	return len(s)
}

// checker keeps the first refusal of a run of checks.
type checker struct{ err error }

func (c *checker) field(key, label, s string) {
	if c.err == nil {
		c.err = checkField(key, label, s)
	}
}

func (c *checker) must(ok bool, code, format string, args ...any) {
	if c.err == nil && !ok {
		c.err = refuse(code, format, args...)
	}
}

func (c *checker) count(what string, n, max int) {
	if c.err == nil {
		c.err = checkCount(what, n, max)
	}
}

func (c *checker) measured(label string, v any, max int) {
	if n := measure(v); c.err == nil && n > max {
		c.err = refuse(RefusalBound, "%s is %d bytes as encoded, more than %d", label, n, max)
	}
}

// number checks numbers are finite and within MaxRectCoord either way.
func (c *checker) number(label string, vs ...float64) {
	for _, v := range vs {
		c.must(!math.IsNaN(v) && math.Abs(v) <= MaxRectCoord, RefusalInvalid, "%s is not a number within %d", label, MaxRectCoord)
	}
}

func (c *checker) rect(label string, r Rect) {
	c.number(label, r.X, r.Y, r.Width, r.Height)
	c.must(r.Width >= 0 && r.Height >= 0, RefusalInvalid, "%s has a negative size", label)
}

func (c *checker) viewport(label string, v Viewport) {
	c.must(slices.Contains(ViewportModes, v.Mode), RefusalInvalid, "%s's mode %q is none of %s", label, v.Mode, strings.Join(ViewportModes, ", "))
	c.must(v.Width >= 0 && v.Height >= 0 && v.Width <= MaxRectCoord && v.Height <= MaxRectCoord, RefusalInvalid, "%s's size is out of range", label)
	c.number(label, v.ScrollX, v.ScrollY)
	c.must(v.Scale > 0 && v.Scale <= 64 && v.DPR > 0 && v.DPR <= 64, RefusalInvalid, "%s's scale and pixel ratio are not above 0 and at most 64", label)
	c.must(v.ColorScheme == "light" || v.ColorScheme == "dark", RefusalInvalid, "%s's colour scheme is neither light nor dark", label)
	c.field("capture.user_agent", label+"'s user agent", v.UserAgent)
}

func (c *checker) hexID(label, s string, required bool) {
	c.must((s == "" && !required) || objectIDPattern.MatchString(s), RefusalInvalid, "%s %q is not a full object id", label, s)
}

// classes checks an element's or an ancestor's classes, joined by spaces.
func (c *checker) names(label string, tag, id, role, name string, classes []string) {
	for _, f := range [][2]string{{"tag", tag}, {"id", id}, {"role", role}, {"accessible name", name}, {"classes", strings.Join(classes, " ")}} {
		c.field("element.name", label+"'s "+f[0], f[1])
	}
}

// element checks an element's report by every bound of the plan's, and
// the privacy rule: an input's value, and a password field's, is never
// kept.
func (c *checker) element(label string, e *ElementReport) {
	c.must(e.Tag != "", RefusalInvalid, "%s names no tag", label)
	c.names(label, e.Tag, e.ID, e.Role, e.Name, e.Classes)
	c.field("element.text", label+"'s text", e.Text)
	c.field("element.selector", label+"'s selector", e.Selector)
	c.rect(label+"'s box", e.Box)
	c.count("attributes", len(e.Attributes), MaxElementAttributes)
	secret := strings.EqualFold(e.Tag, "input")
	for _, a := range e.Attributes {
		c.field("element.attribute_name", label+"'s attribute name", a[0])
		c.field("element.attribute_value", label+"'s attribute value", a[1])
		secret = secret || (strings.EqualFold(a[0], "type") && strings.EqualFold(a[1], "password"))
	}
	for _, a := range e.Attributes {
		c.must(!secret || !strings.EqualFold(a[0], "value"), RefusalInvalid, "%s keeps an input's value, which is never kept", label)
	}
	for _, w := range []string{e.State.AriaDisabled, e.State.AriaExpanded, e.State.PointerEvents} {
		c.field("element.word", label+"'s state", w)
	}
	c.must(e.State.Motion >= 0 && e.State.Motion <= MaxRectCoord, RefusalInvalid, "%s's motion is out of range", label)
	c.field("element.selector", label+"'s hit test's selector", e.Hit.Selector)
	c.field("element.name", label+"'s hit test's tag", e.Hit.Tag)
	c.field("element.html", label+"'s HTML", e.HTML)
	c.field("element.neighbourhood", label+"'s neighbourhood", e.Neighbourhood)
	c.count("ancestors", len(e.Ancestors), MaxAncestors)
	for _, a := range e.Ancestors {
		c.names(label+"'s ancestor", a.Tag, a.ID, a.Role, "", a.Classes)
		c.measured(label+"'s ancestor", a, MaxAncestor)
	}
	c.count("styles", len(e.Styles), MaxStyles)
	for _, st := range e.Styles {
		c.field("element.word", label+"'s style's property", st[0])
		c.field("element.style_value", label+"'s style's value", st[1])
	}
	c.count("hints", len(e.Hints), MaxHints)
	for _, h := range e.Hints {
		c.field("element.word", label+"'s hint's how", h.How)
		c.field("element.hint_file", label+"'s hint's file", h.File)
		c.field("element.hint_component", label+"'s hint's component", h.Component)
		c.must(h.Line >= 0 && h.Column >= 0 && h.Line <= MaxFindingLine && h.Column <= MaxFindingLine, RefusalInvalid, "%s's hint's line is out of range", label)
	}
	c.count("cut steps", len(e.Cut), 10)
	for _, w := range e.Cut {
		c.field("element.word", label+"'s cut", w)
	}
	c.measured(label+"'s report", e, MaxElementReport)
}

// sources checks an element's checked sources: within the hints' bounds,
// each of a class, the classes in their order.
func (c *checker) sources(label string, ss []Source) {
	c.count("sources", len(ss), MaxHints)
	rank := 0
	for _, s := range ss {
		c.must(s.Path != "", RefusalInvalid, "%s's source names no path", label)
		c.field("element.hint_file", label+"'s source's path", s.Path)
		c.field("element.hint_component", label+"'s source's component", s.Component)
		c.field("element.word", label+"'s source's how", s.How)
		c.must(s.Line >= 0 && s.Column >= 0 && s.Line <= MaxFindingLine && s.Column <= MaxFindingLine, RefusalInvalid, "%s's source's line is out of range", label)
		c.must(!(s.Outside && s.Found), RefusalInvalid, "%s's source outside the tree is not found in it", label)
		r := slices.Index(SourceClasses, s.Class)
		c.must(r >= rank, RefusalInvalid, "%s's sources are not tracked, untracked, ignored and vendor in that order, or name another class", label)
		rank = max(rank, r)
	}
}

// pageOrigin is a URL's scheme, host and port: what a link's retake must
// share with the capture it retakes.
func pageOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// checkCapture checks a create's client words and the server's facts by
// every bound and rule that needs no transaction, and gives the capture's
// row as made of them (its id, times, image and retake's relation the
// operation's to fill) with its annotations in their order.
func checkCapture(n NewCapture, f CaptureFacts, image bool) (Capture, []Annotation, error) {
	c := &checker{}
	c.must(n.Via == CaptureViaWeb || n.Via == CaptureViaDesktop, RefusalInvalid, "via %q is neither web nor desktop", n.Via)
	c.field("capture.url", "the page's URL", n.URL)
	c.field("capture.title", "the page's title", n.Title)
	c.viewport("the viewport", n.Viewport)
	c.must(n.Epoch >= 0, RefusalInvalid, "the page's epoch is negative")
	c.must(n.Revision == "" || (len(n.Revision) <= MaxDeclaredRevision && declaredPattern.MatchString(n.Revision)), RefusalInvalid,
		"the page's declared revision is not <commit>[+<content>] in hex within %d bytes", MaxDeclaredRevision)
	c.hexID("the selection's commit", n.Label.Commit, false)
	c.hexID("the selection's content", n.Label.Content, false)
	c.must(!n.SelectedAt.IsZero(), RefusalInvalid, "the capture names no moment of selection")
	c.must(n.RetakeOf == "" || ValidID(n.RetakeOf), RefusalInvalid, "the capture retaken is no capture's id")
	c.must(image == (n.ImageWhy == ""), RefusalInvalid, "a capture with no screenshot says why, and one with a screenshot says nothing")
	c.field("capture.image_why", "why there is no screenshot", n.ImageWhy)
	c.must((image && n.ImageScale > 0 && n.ImageScale <= 64) || (!image && n.ImageScale == 0), RefusalInvalid, "the screenshot's scale is not above 0 and at most 64")
	// The origin and its tree, the server's.
	switch f.Origin {
	case OriginURL:
		c.must(f.Run == "" && f.Task == "" && f.Attempt == 0 && f.Tree == "" && f.Commit == "" && f.Content == "" && f.Changed == 0 &&
			len(f.ChangedPaths) == 0 && !f.Mutable && f.Candidate == "" && f.Where == "", RefusalInvalid, "a link's capture names no run and no tree")
	case OriginAttempt, OriginCandidate:
		c.must(validRunID(f.Run) && taskIDPattern.MatchString(f.Task) && f.Attempt >= 1 && f.Attempt <= MaxFindingLine, RefusalInvalid,
			"the capture's attempt is not a run's, a task's and a number from 1")
		c.hexID("the tree's commit", f.Commit, true)
		c.hexID("the tree's content", f.Content, false)
		c.must(whereIDPattern.MatchString(f.Where), RefusalInvalid, "where the attempt ran is not a runtime's name")
		c.must(f.Changed >= len(f.ChangedPaths), RefusalInvalid, "the tree's changed paths are more than its count")
		for _, p := range f.ChangedPaths {
			c.must(p != "" && utf8.ValidString(p), RefusalInvalid, "a changed path is empty or not valid UTF-8")
		}
		if f.Origin == OriginAttempt {
			c.must(f.Tree == TreeWorktree && f.Mutable && f.Candidate == "", RefusalInvalid, "an attempt's capture is of its worktree, mutable, and names no candidate")
		} else {
			c.must(f.Tree == TreePreview, RefusalInvalid, "a candidate's capture is of its preview checkout")
		}
		if f.Candidate != "" {
			cand, err := ParseCandidate(f.Candidate)
			c.must(err == nil && cand.Run == f.Run && cand.Task == f.Task && cand.Attempt == f.Attempt && cand.Commit == f.Commit, RefusalInvalid,
				"the candidate %q is not the checkout's attempt at its commit", f.Candidate)
		}
	default:
		c.must(false, RefusalInvalid, "the origin %q is none of attempt, candidate and url", f.Origin)
	}
	for _, s := range []string{f.Scope.Crew, f.Scope.Repo, f.Scope.Project} {
		c.field("element.hint_file", "the run's scope", s)
	}
	c.measured("the run's scope", f.Scope, MaxCaptureScope)
	c.count("annotations", len(n.Annotations), MaxCaptureAnnotations)
	c.must(len(f.Sources) == 0 || len(f.Sources) == len(n.Annotations), RefusalInvalid, "the sources name %d annotations of %d", len(f.Sources), len(n.Annotations))
	if c.err != nil {
		return Capture{}, nil, c.err
	}
	anns := make([]Annotation, len(n.Annotations))
	for i, a := range n.Annotations {
		label := fmt.Sprintf("annotation %d", i+1)
		var sources []Source
		if len(f.Sources) > 0 {
			sources = f.Sources[i]
		}
		c.field("annotation.note", label+"'s note", a.Note)
		c.rect(label+"'s rectangle", a.Rect)
		switch a.Kind {
		case AnnotationElement:
			c.must(a.Element != nil && a.Observed != nil, RefusalInvalid, "%s is an element with no report or no moment", label)
			if c.err != nil {
				return Capture{}, nil, c.err
			}
			c.element(label, a.Element)
			c.sources(label, sources)
			o := a.Observed
			c.must(!o.At.IsZero(), RefusalInvalid, "%s names no moment", label)
			c.field("capture.title", label+"'s title", o.Title)
			// One capture is one moment: every element was selected at the
			// moment the screenshot shows.
			c.must(o.URL == n.URL && o.Viewport == n.Viewport && o.Epoch == n.Epoch && o.Revision == n.Revision &&
				o.Label == (RevisionLabel{n.Label.Commit, n.Label.Content}), RefusalInvalid,
				"%s was selected after the page changed: it is another capture", label)
		case AnnotationRegion:
			c.must(a.Element == nil && a.Observed == nil && len(sources) == 0, RefusalInvalid, "%s is a region with an element's report", label)
			c.must(image, RefusalInvalid, "%s is a region, drawn on a screenshot, and this capture has none", label)
		default:
			c.must(false, RefusalInvalid, "%s's kind %q is neither element nor region", label, a.Kind)
		}
		anns[i] = Annotation{K: i + 1, Kind: a.Kind, Note: a.Note, Rect: a.Rect, Element: a.Element, Observed: a.Observed, Sources: sources}
	}
	if c.err != nil {
		return Capture{}, nil, c.err
	}
	paths, cut := cutChanged(f.ChangedPaths)
	origin := CaptureOrigin{Kind: f.Origin, Run: f.Run, Task: f.Task, Attempt: f.Attempt, Tree: f.Tree, Commit: f.Commit, Changed: f.Changed,
		ChangedPaths: paths, ChangedCut: cut || len(paths) < f.Changed, Content: f.Content, Mutable: f.Mutable, Candidate: f.Candidate,
		Where: f.Where, Drift: drift(n.Label, f), Serving: serving(n.Revision, f)}
	doc := CaptureDocument{Schema: CaptureSchemaName, URL: n.URL, Title: n.Title, Viewport: n.Viewport, SelectedAt: n.SelectedAt.UTC(),
		Origin: origin, Image: CaptureImage{How: ImageNone, Why: n.ImageWhy}, RetakeOf: n.RetakeOf}
	if image {
		doc.Image = CaptureImage{How: ImageTab, Scale: n.ImageScale}
		if n.Via == CaptureViaDesktop {
			doc.Image.How = ImageDesktop
		}
	}
	return Capture{CaptureDocument: doc, Via: n.Via, Epoch: n.Epoch, Scope: f.Scope}, anns, nil
}

// cutChanged is the first of the changed paths within MaxChangedPaths and
// MaxChangedPathsBytes as encoded, at whichever comes first, and whether
// any was left out.
func cutChanged(paths []string) ([]string, bool) {
	out := []string{}
	size := len("[]")
	for _, p := range paths {
		n := measure(p)
		if len(out) > 0 {
			n++
		}
		if len(out) == MaxChangedPaths || size+n > MaxChangedPathsBytes {
			return out, true
		}
		out, size = append(out, p), size+n
	}
	return out, false
}

// drift is the pane's read at the selection when the save's differs from
// it: another commit or another content, or either with none.
func drift(sel TreeRead, f CaptureFacts) *TreeRead {
	if f.Origin == OriginURL || (sel.Commit == f.Commit && sel.Content == f.Content && sel.Content != "") {
		return nil
	}
	sel.ReadAt = sel.ReadAt.UTC()
	return &sel
}

// serving is what the page's declared revision says against the save's
// read: declared when it names the tree's commit (and its content when
// the tree has changes), stale when it names another, unconfirmed when it
// names none, a commit alone over a tree with changes, or there is no
// tree.
func serving(page string, f CaptureFacts) Serving {
	commit, content, plus := strings.Cut(page, "+")
	state := ServingDeclared
	switch {
	case page == "" || f.Origin == OriginURL:
		state = ServingUnconfirmed
	case !strings.HasPrefix(f.Commit, commit):
		state = ServingStale
	case plus && f.Content == "":
		state = ServingUnconfirmed
	case plus && !strings.HasPrefix(f.Content, content):
		state = ServingStale
	case !plus && f.Changed > 0:
		state = ServingUnconfirmed
	}
	return Serving{State: state, Page: page}
}

// pngSize reads the PNG at path's header, before a pixel is allocated: its
// size, each side 1 to MaxCaptureImageSide; invalid when it is no PNG.
// The whole image's decode, every chunk's CRC, is the API's.
func pngSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, refuse(RefusalInvalid, "the screenshot is not a PNG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxCaptureImageSide || cfg.Height > MaxCaptureImageSide {
		return 0, 0, refuse(RefusalBound, "the screenshot is %d by %d pixels; a side is at most %d", cfg.Width, cfg.Height, MaxCaptureImageSide)
	}
	return cfg.Width, cfg.Height, nil
}

// inImage refuses a region whose rectangle is not within the screenshot.
func inImage(label string, r Rect, img CaptureImage) error {
	if r.X < 0 || r.Y < 0 || r.X+r.Width > float64(img.Width) || r.Y+r.Height > float64(img.Height) {
		return refuse(RefusalInvalid, "%s's rectangle is not within the %d by %d screenshot", label, img.Width, img.Height)
	}
	return nil
}

// fileName is the name a send's file takes on a ticket.
func fileName(c Capture, ext string) string {
	return fmt.Sprintf("capture-%s-r%d.%s", c.ID, c.Rev, ext)
}

// ticketText is a ticket's description made from a capture: its warnings
// first (the drift, what the page serves, no screenshot), the page and
// its revision, then each annotation's note with its element, what a click
// reaches and its first source; cut to MaxTicketDescription at a
// character, so the warnings always stand.
func ticketText(c Capture) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	o := c.Origin
	if o.Drift != nil {
		line("> The worktree changed between the selection and the save.")
	}
	switch o.Serving.State {
	case ServingStale:
		line("> The page reports %s; the tree holds %s.", o.Serving.Page, short(o.Commit))
	case ServingUnconfirmed:
		line("> What the page shows is not confirmed against the tree.")
	}
	if c.Image.How == ImageNone {
		line("> No screenshot: %s", c.Image.Why)
	}
	line("Captured from %s (%s), %s %dx%d.", c.URL, c.Title, c.Viewport.Mode, c.Viewport.Width, c.Viewport.Height)
	if o.Kind != OriginURL {
		line("Revision: %s %s/%s#%d at %s, %d changed files.", o.Tree, o.Run, o.Task, o.Attempt, short(o.Commit), o.Changed)
	}
	for _, a := range c.Annotations {
		line("\n### %d. %s", a.K, a.Kind)
		if e := a.Element; e != nil {
			line("`%s` %q, `%s`", e.Tag, e.Name, e.Selector)
			if !e.Hit.Self && e.Hit.Selector != "" {
				line("A click at its centre reaches `%s`.", e.Hit.Selector)
			}
			if e.State.Disabled {
				line("It is disabled.")
			}
		}
		if len(a.Sources) > 0 {
			line("Source: `%s` (%s)", a.Sources[0].Path, a.Sources[0].Class)
		}
		line("%s", a.Note)
	}
	return cutBytes(fitText(b.String(), true), MaxTicketDescription)
}
