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
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A capture is a person's evidence about work (Program item PV1,
// docs/plans/browser-evidence.md): what a preview showed at one moment, a
// screenshot when there is one, and up to MaxCaptureAnnotations
// annotations, each an element the page's bridge reported or a region of
// the screenshot, with the person's note. The client's words (NewCapture)
// and the server's reads of the tree (CaptureFacts) are checked here
// against every bound and every relation the evidence rules state, the
// store working out itself what it can from them: the drift between the
// selection's read and the save's, what the page's declared revision says
// of the tree (serving), the changed paths' cut and how a retake is
// related to the capture it retakes. How the screenshot was taken is the
// client's word (NewCapture.ImageHow), checked against what the store
// knows and stored as given. The privacy rule compares names by their
// local names in one form (namedAs), and an attribute's name not of an
// attribute's form keeps no value near it (isAttributeName). The client's
// words are checked by one pure function (CheckNewCapture) before the
// facts are. The capture's document
// (CaptureDocument, conductor.capture/1) is what a send freezes as a
// content file (sends.go).

// CaptureSchemaName is a capture document's schema.
const CaptureSchemaName = "conductor.capture/1"

// A capture's words: how it was taken, where from, what tree, how its
// image was had, how a retake is related, an annotation's kind, a
// source's class, what the page serves.
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
// "element report"), at most MaxElementReport bytes as the bridge measures
// it (bridgeReportOf). Attributes and Styles are name and value pairs.
// Measured is the report's measure as the bridge cut it, taken by the API
// before it put the report in this shape (0 when none was taken): never
// kept, and checked beside the store's own measure.
type ElementReport struct {
	Measured      int          `json:"-"`
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
// page declared and the pane's read of the tree at the selection), how
// the screenshot was taken (ImageDesktop, ImageTab, or ImageNone with why
// there is none) and its scale, the capture it retakes, and the
// annotations in their order.
type NewCapture struct {
	Via         string          `json:"via"`
	URL         string          `json:"url"`
	Title       string          `json:"title"`
	Viewport    Viewport        `json:"viewport"`
	Epoch       int64           `json:"epoch"`
	Revision    string          `json:"revision,omitempty"`
	Label       TreeRead        `json:"label"`
	SelectedAt  time.Time       `json:"selectedAt"`
	ImageHow    string          `json:"imageHow"`
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
// set and, read alone (Capture), its sends. A deleted capture is its id,
// rev and times alone (captureTombstone) through every read the store
// offers (DeleteCapture says what a delete removes and what it hides);
// its sends are read by their own reads (CaptureSend, CaptureSends).
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

// captureTombstone is what every read gives of a deleted capture: its id,
// its revision and its times.
type captureTombstone struct {
	ID        string    `json:"id"`
	Rev       int64     `json:"rev"`
	CreatedAt time.Time `json:"createdAt"`
	DeletedAt time.Time `json:"deletedAt"`
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

// measure is v's size as the store keeps it: its JSON with HTML escaping
// off (encodeValue), the bridge's measure too.
func measure(v any) int {
	s, err := encodeValue(v)
	if err != nil {
		return math.MaxInt
	}
	return len(s)
}

// storable is whether t is a time the store writes and reads back: as
// JSON in its own zone, and as a stamp (four digits of year in UTC).
func storable(t time.Time) bool {
	_, err := t.MarshalJSON()
	return err == nil && t.UTC().Year() >= 0 && t.UTC().Year() <= 9999
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
	c.within(label, MaxRectCoord, vs...)
}

// within checks numbers are finite and within bound either way.
func (c *checker) within(label string, bound float64, vs ...float64) {
	for _, v := range vs {
		c.must(!math.IsNaN(v) && math.Abs(v) <= bound, RefusalInvalid, "%s is not a number within %.0f", label, bound)
	}
}

func (c *checker) rect(label string, r Rect) {
	c.number(label, r.X, r.Y, r.Width, r.Height)
	c.must(r.Width >= 0 && r.Height >= 0, RefusalInvalid, "%s has a negative size", label)
}

func (c *checker) viewport(label string, v Viewport) {
	c.must(slices.Contains(ViewportModes, v.Mode), RefusalInvalid, "%s's mode %q is none of %s", label, v.Mode, strings.Join(ViewportModes, ", "))
	c.must(v.Width >= 0 && v.Height >= 0 && v.Width <= MaxRectCoord && v.Height <= MaxRectCoord, RefusalInvalid, "%s's size is out of range", label)
	c.within(label+"'s scroll", MaxScroll, v.ScrollX, v.ScrollY)
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

// The names of the forms the privacy rule judges. A tag is ASCII letters,
// digits, '.', '_' and '-', from a letter, with at most one prefix of the
// same form before one colon: HTML's, SVG's and MathML's elements and
// custom elements. An attribute's name is ASCII, from a letter, '_', ':' or
// '@', on with letters, digits, '_', '.', ':', '@' and '-', and something
// after its last colon. A name of another form is kept, and the element
// that has one keeps no value (element).
var (
	tagPattern           = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*(:[A-Za-z][A-Za-z0-9._-]*)?$`)
	attributeNamePattern = regexp.MustCompile(`^[A-Za-z_:@][A-Za-z0-9_.:@-]*$`)
)

// isTag is whether s is an element's tag of the form (tagPattern).
func isTag(s string) bool { return tagPattern.MatchString(s) }

// isAttributeName is whether s is an attribute's name
// (attributeNamePattern).
func isAttributeName(s string) bool {
	return attributeNamePattern.MatchString(s) && !strings.HasSuffix(s, ":")
}

// namedAs is whether the name (an element's tag, an attribute's name) is
// the local name want, as the privacy rule compares names: what follows
// its last colon, without regard to ASCII case. Every question the rule
// asks of a name goes through it.
func namedAs(name, want string) bool {
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	return strings.EqualFold(name, want)
}

// asciiLetters is s's ASCII letters alone, lower-cased: what a type
// attribute's value is judged by.
func asciiLetters(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z':
			return r
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, s)
}

// element checks an element's report by every bound of the plan's and the
// bridge's, and the privacy rule: an input's value, and a password
// field's (a type whose letters spell password, on any element), is never
// kept. A tag and an attribute's name are any name within their bounds
// with no control character, as the bridge reports them; the rule judges
// them by their local names (namedAs). A name that is not of its form
// (isTag, isAttributeName) could hide an input or a type: an element with
// one keeps no value, and an input's or a password field's attributes are
// all of the form.
func (c *checker) element(label string, e *ElementReport) {
	c.must(!strings.ContainsFunc(e.Tag, unicode.IsControl), RefusalInvalid, "%s's tag holds a control character", label)
	c.names(label, e.Tag, e.ID, e.Role, e.Name, e.Classes)
	c.field("element.text", label+"'s text", e.Text)
	c.field("element.selector", label+"'s selector", e.Selector)
	c.box(label+"'s box", e.Box)
	c.count("attributes", len(e.Attributes), MaxElementAttributes)
	secret, odd := namedAs(e.Tag, "input"), !isTag(e.Tag)
	for _, a := range e.Attributes {
		c.field("element.attribute_name", label+"'s attribute name", a[0])
		c.field("element.attribute_value", label+"'s attribute value", a[1])
		secret = secret || (namedAs(a[0], "type") && asciiLetters(a[1]) == "password")
		odd = odd || !isAttributeName(a[0])
	}
	for _, a := range e.Attributes {
		c.must(!(secret || odd) || !namedAs(a[0], "value"), RefusalInvalid, "%s keeps an input's value, which is never kept", label)
		c.must(!secret || isAttributeName(a[0]), RefusalInvalid, "%s's attribute name is not an attribute's name, on an element whose value is never kept", label)
	}
	// A state's words are bound as the bridge bounds them (a name's bound).
	for _, w := range []string{e.State.AriaDisabled, e.State.AriaExpanded, e.State.PointerEvents} {
		c.field("element.name", label+"'s state", w)
	}
	c.must(e.State.Motion >= 0 && e.State.Motion <= MaxHintLine, RefusalInvalid, "%s's motion is out of range", label)
	c.field("element.selector", label+"'s hit test's selector", e.Hit.Selector)
	c.field("element.name", label+"'s hit test's tag", e.Hit.Tag)
	c.field("element.html", label+"'s HTML", e.HTML)
	c.field("element.neighbourhood", label+"'s neighbourhood", e.Neighbourhood)
	c.count("ancestors", len(e.Ancestors), MaxAncestors)
	for _, a := range e.Ancestors {
		c.names(label+"'s ancestor", a.Tag, a.ID, a.Role, "", a.Classes)
		c.measured(label+"'s ancestor", bridgeAncestorOf(a), MaxAncestor)
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
		c.must(h.Line >= 0 && h.Column >= 0 && h.Line <= MaxHintLine && h.Column <= MaxHintLine, RefusalInvalid, "%s's hint's line is out of range", label)
	}
	c.count("cut steps", len(e.Cut), 10)
	for _, w := range e.Cut {
		c.field("element.word", label+"'s cut", w)
	}
	if n := max(e.Measured, measure(bridgeReportOf(e))); c.err == nil && n > MaxElementReport {
		c.err = refuse(RefusalBound, "%s's report is %d bytes as the bridge measures it, more than %d", label, n, MaxElementReport)
	}
}

// box checks an element's box as the bridge bounds it: finite numbers, its
// size not negative.
func (c *checker) box(label string, r Rect) {
	c.within(label, math.MaxFloat64, r.X, r.Y, r.Width, r.Height)
	c.must(r.Width >= 0 && r.Height >= 0, RefusalInvalid, "%s has a negative size", label)
}

// bridgeReport is an element's report in the bridge's own shape
// (internal/proto's ElementReport, docs/protocol.md "Preview bridge"):
// every member written, a list empty as [], the classes joined by spaces.
// A report and its ancestors are measured in it, so the store's bound is
// the one the bridge cut the report to.
type bridgeReport struct {
	Tag           string           `json:"tag"`
	ID            string           `json:"id"`
	Classes       string           `json:"classes"`
	Role          string           `json:"role"`
	Name          string           `json:"name"`
	Text          string           `json:"text"`
	Selector      string           `json:"selector"`
	Box           Rect             `json:"box"`
	Attributes    [][2]string      `json:"attributes"`
	State         bridgeState      `json:"state"`
	Hit           bridgeHit        `json:"hit"`
	HTML          string           `json:"html"`
	Neighbourhood string           `json:"neighbourhood"`
	Ancestors     []bridgeAncestor `json:"ancestors"`
	Styles        [][2]string      `json:"styles"`
	Hints         []bridgeHint     `json:"hints"`
	Cut           []string         `json:"cut"`
}

type bridgeState struct {
	Disabled      bool   `json:"disabled"`
	Hidden        bool   `json:"hidden"`
	AriaDisabled  string `json:"ariaDisabled"`
	AriaExpanded  string `json:"ariaExpanded"`
	PointerEvents string `json:"pointerEvents"`
	Inert         bool   `json:"inert"`
	Hovered       bool   `json:"hovered"`
	Motion        int    `json:"motion"`
}

type bridgeHit struct {
	Self     bool   `json:"self"`
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
}

type bridgeAncestor struct {
	Tag     string `json:"tag"`
	ID      string `json:"id"`
	Classes string `json:"classes"`
	Role    string `json:"role"`
}

type bridgeHint struct {
	How       string `json:"how"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Component string `json:"component"`
}

// bridgeAncestorOf is a in the bridge's shape.
func bridgeAncestorOf(a Ancestor) bridgeAncestor {
	return bridgeAncestor{Tag: a.Tag, ID: a.ID, Classes: strings.Join(a.Classes, " "), Role: a.Role}
}

// bridgeReportOf is e in the bridge's shape.
func bridgeReportOf(e *ElementReport) bridgeReport {
	list := func(v [][2]string) [][2]string {
		if v == nil {
			return [][2]string{}
		}
		return v
	}
	b := bridgeReport{Tag: e.Tag, ID: e.ID, Classes: strings.Join(e.Classes, " "), Role: e.Role, Name: e.Name, Text: e.Text, Selector: e.Selector,
		Box: e.Box, Attributes: list(e.Attributes), State: bridgeState(e.State), Hit: bridgeHit(e.Hit), HTML: e.HTML, Neighbourhood: e.Neighbourhood,
		Ancestors: []bridgeAncestor{}, Styles: list(e.Styles), Hints: []bridgeHint{}, Cut: e.Cut}
	if b.Cut == nil {
		b.Cut = []string{}
	}
	for _, a := range e.Ancestors {
		b.Ancestors = append(b.Ancestors, bridgeAncestorOf(a))
	}
	for _, h := range e.Hints {
		b.Hints = append(b.Hints, bridgeHint(h))
	}
	return b
}

// ReportText is a text of an element's report as the store keeps it,
// never longer as encoded: a carriage return made a line feed, every other
// control character but a line feed and a tab made a space, and each run
// of bytes that is not UTF-8 one space. The API makes every text of a
// report so before the report is checked.
func ReportText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return '\n'
		case unicode.IsControl(r) && r != '\n' && r != '\t':
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, " "))
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
		c.must(s.Line >= 0 && s.Column >= 0 && s.Line <= MaxHintLine && s.Column <= MaxHintLine, RefusalInvalid, "%s's source's line is out of range", label)
		c.must(!(s.Outside && s.Found), RefusalInvalid, "%s's source outside the tree is not found in it", label)
		r := slices.Index(SourceClasses, s.Class)
		c.must(r >= rank, RefusalInvalid, "%s's sources are not tracked, untracked, ignored and vendor in that order, or name another class", label)
		rank = max(rank, r)
	}
}

// pageOrigin is a URL's origin in one form: its scheme and host without
// regard to case and its effective port (a default port written or not),
// what a link's retake must share with the capture it retakes; "" for no
// http or https URL with a host.
func pageOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	defaults := map[string]string{"http": "80", "https": "443"}
	if _, ok := defaults[scheme]; !ok {
		return ""
	}
	if port == "" {
		port = defaults[scheme]
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + strconv.Itoa(n)
}

// CheckNewCapture checks a create's client words as they are handed in,
// with or without a screenshot (image), by every rule of the store's that
// needs nothing else: every text's bound and form, every count, each
// element's report (its measure as the bridge cut it among it), each
// annotation's kind and the one moment they were all selected at. It is
// pure (no tree, no transaction, no other effect): the API calls it before
// it reads anything for the capture, and CreateCapture calls it first, so
// a request it refuses is refused by the store with its receipt.
func CheckNewCapture(n NewCapture, image bool) error {
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
	// How the screenshot was taken is the client's word: none exactly when
	// there is none, the desktop's only from the desktop client.
	c.must((!image && n.ImageHow == ImageNone) || (image && (n.ImageHow == ImageTab || (n.ImageHow == ImageDesktop && n.Via == CaptureViaDesktop))),
		RefusalInvalid, "how the screenshot was taken, %q, is not none without one, or tab or (from the desktop client) desktop with one", n.ImageHow)
	c.must(n.Label.Changed >= 0 && n.Label.Changed <= MaxChangedCount, RefusalInvalid, "the selection's read counts changed paths not from 0 to %d", MaxChangedCount)
	// Every time is one the store writes and reads back, and the
	// selection's read was made before the capture is.
	c.must(storable(n.SelectedAt), RefusalInvalid, "the moment of selection is not a time the store keeps")
	c.must(n.Label.ReadAt.IsZero() || (storable(n.Label.ReadAt) && !n.Label.ReadAt.After(time.Now())), RefusalInvalid,
		"the selection's read is not a time the store keeps before now")
	c.count("annotations", len(n.Annotations), MaxCaptureAnnotations)
	for i, a := range n.Annotations {
		label := fmt.Sprintf("annotation %d", i+1)
		c.field("annotation.note", label+"'s note", a.Note)
		c.rect(label+"'s rectangle", a.Rect)
		switch a.Kind {
		case AnnotationElement:
			c.must(a.Element != nil && a.Observed != nil, RefusalInvalid, "%s is an element with no report or no moment", label)
			if c.err != nil {
				return c.err
			}
			c.element(label, a.Element)
			o := a.Observed
			c.must(!o.At.IsZero() && storable(o.At), RefusalInvalid, "%s names no moment the store keeps", label)
			c.field("capture.title", label+"'s title", o.Title)
			// Stability is the pixels' verdict: none without a screenshot,
			// and never stable while the element is in motion.
			c.must(o.Stable == nil || (image && (!*o.Stable || a.Element.State.Motion == 0)), RefusalInvalid,
				"%s's stability is claimed without a screenshot, or while it is in motion", label)
			// One capture is one moment: every element was selected at the
			// moment the screenshot shows.
			c.must(o.URL == n.URL && o.Viewport == n.Viewport && o.Epoch == n.Epoch && o.Revision == n.Revision &&
				o.Label == (RevisionLabel{n.Label.Commit, n.Label.Content}), RefusalInvalid,
				"%s was selected after the page changed: it is another capture", label)
		case AnnotationRegion:
			c.must(a.Element == nil && a.Observed == nil, RefusalInvalid, "%s is a region with an element's report", label)
			c.must(image, RefusalInvalid, "%s is a region, drawn on a screenshot, and this capture has none", label)
		default:
			c.must(false, RefusalInvalid, "%s's kind %q is neither element nor region", label, a.Kind)
		}
	}
	return c.err
}

// CheckCaptureEdit checks an edit's annotations as they are handed in by
// every rule of the store's that needs no capture: their count, each one's
// kind and note, a region's rectangle, and no new element's report. It is
// pure, as CheckNewCapture is, and EditCapture calls it first.
func CheckCaptureEdit(edits []AnnotationEdit) error {
	c := &checker{}
	c.count("annotations", len(edits), MaxCaptureAnnotations)
	for i, e := range edits {
		label := fmt.Sprintf("annotation %d", i+1)
		c.must(e.Element == nil, RefusalInvalid, "%s brings a new element: a new element is a new capture: Capture again", label)
		c.must(e.Kind == AnnotationElement || e.Kind == AnnotationRegion, RefusalInvalid, "%s's kind %q is neither element nor region", label, e.Kind)
		c.must(e.K >= 0, RefusalInvalid, "%s names annotation %d, which is not one of the capture's", label, e.K)
		c.field("annotation.note", label+"'s note", e.Note)
		if e.Kind == AnnotationRegion {
			c.rect(label+"'s rectangle", e.Rect)
		}
	}
	return c.err
}

// checkCapture checks a create's client words (CheckNewCapture, first) and
// the server's facts by every bound and rule that needs no transaction,
// and gives the capture's row as made of them (its id, times, image and
// retake's relation the operation's to fill) with its annotations in their
// order.
func checkCapture(n NewCapture, f CaptureFacts, image bool) (Capture, []Annotation, error) {
	if err := CheckNewCapture(n, image); err != nil {
		return Capture{}, nil, err
	}
	c := &checker{}
	// The origin and its tree, the server's.
	switch f.Origin {
	case OriginURL:
		c.must(f.Run == "" && f.Task == "" && f.Attempt == 0 && f.Tree == "" && f.Commit == "" && f.Content == "" && f.Changed == 0 &&
			len(f.ChangedPaths) == 0 && !f.Mutable && f.Candidate == "" && f.Where == "" && f.Scope == (CaptureScope{}) && len(f.Sources) == 0 &&
			n.Label.Commit == "" && n.Label.Content == "", RefusalInvalid,
			"a link's capture names no run, no tree, no sources and no run's scope, and its selection no commit and no content")
	case OriginAttempt, OriginCandidate:
		c.must(validRunID(f.Run) && taskIDPattern.MatchString(f.Task) && f.Attempt >= 1 && f.Attempt <= MaxFindingLine, RefusalInvalid,
			"the capture's attempt is not a run's, a task's and a number from 1")
		c.hexID("the tree's commit", f.Commit, true)
		c.hexID("the tree's content", f.Content, false)
		c.must(whereIDPattern.MatchString(f.Where), RefusalInvalid, "where the attempt ran is not a runtime's name")
		c.must(f.Changed >= len(f.ChangedPaths) && f.Changed <= MaxChangedCount, RefusalInvalid, "the tree's changed paths are more than its count, or its count more than %d", MaxChangedCount)
		for _, p := range f.ChangedPaths {
			c.field("capture.changed_path", "a changed path", p)
		}
		// A drift kept says when its read was made.
		c.must(drift(n.Label, f) == nil || !n.Label.ReadAt.IsZero(), RefusalInvalid, "the selection's read, kept as the drift, names no time")
		if f.Origin == OriginAttempt {
			c.must(f.Tree == TreeWorktree && f.Mutable && f.Candidate == "", RefusalInvalid, "an attempt's capture is of its worktree, mutable, and names no candidate")
		} else {
			c.must(f.Tree == TreePreview && f.Candidate != "", RefusalInvalid, "a candidate's capture is of its preview checkout and names its candidate")
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
		if a.Kind == AnnotationElement {
			c.sources(label, sources)
		} else {
			c.must(len(sources) == 0, RefusalInvalid, "%s is a region with an element's sources", label)
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
		Origin: origin, Image: CaptureImage{How: n.ImageHow, Why: n.ImageWhy, Scale: n.ImageScale}, RetakeOf: n.RetakeOf}
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
// the tree has changes) and the tree's content was read, stale when it
// names another, unconfirmed when it names none, the tree's content was
// not read (unknown, as the drift takes it), a commit alone over a tree
// with changes, or there is no tree.
func serving(page string, f CaptureFacts) Serving {
	commit, content, plus := strings.Cut(page, "+")
	state := ServingDeclared
	switch {
	case page == "" || f.Origin == OriginURL:
		state = ServingUnconfirmed
	case !strings.HasPrefix(f.Commit, commit):
		state = ServingStale
	case f.Content == "":
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
