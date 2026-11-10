package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"unicode/utf8"
)

// The preview bridge's messages (docs/protocol.md, Preview bridge;
// docs/plans/browser-evidence.md): what the workbench and the bridge, a
// script running in a previewed page, say to each other through
// postMessage. Each is a plain object with conductor "preview/1" and its
// type in t. The workbench sends hello (which gives the nonce every later
// message carries), select, describe, look and cancel; the bridge answers
// ready, selected, described, looked and cancelled, each answer with the
// page's URL, title, viewport, epoch and declared revision read at that
// moment. web/app/utils/protocol.ts holds the same definitions, and both
// parsers are held to testdata/preview-messages.json.

// PreviewMark is every message's conductor field; PreviewVersion the
// bridge's version a ready names.
const (
	PreviewMark    = "preview/1"
	PreviewVersion = 1
)

// The messages' types.
const (
	// workbench -> bridge
	PreviewHello    = "hello"
	PreviewSelect   = "select"
	PreviewDescribe = "describe"
	PreviewLook     = "look"
	PreviewCancel   = "cancel"
	// bridge -> workbench
	PreviewReady     = "ready"
	PreviewSelected  = "selected"
	PreviewDescribed = "described"
	PreviewLooked    = "looked"
	PreviewCancelled = "cancelled"
)

// The bridge's bounds, in bytes as PreviewMeasure counts them but where
// said.
const (
	// MaxPreviewToBridge bounds a message to the bridge (the largest,
	// describe, holds a selector of MaxPreviewSelector);
	// MaxPreviewToWorkbench one to the workbench; MaxElementReport the
	// element report one carries, which the bridge cuts to fit.
	MaxPreviewToBridge    = 4 << 10
	MaxPreviewToWorkbench = 64 << 10
	MaxElementReport      = 32 << 10
	// MaxPreviewSelector bounds a describe's selector, MaxPreviewURL a
	// page's URL, MaxPreviewTitle its title (in characters),
	// MaxPreviewUserAgent the browser's user agent and MaxPreviewRevision
	// the revision a page declares.
	MaxPreviewSelector  = 1024
	MaxPreviewURL       = 2048
	MaxPreviewTitle     = 200
	MaxPreviewUserAgent = 512
	MaxPreviewRevision  = 128
	// The element report's fields, each cut to its bound by the bridge:
	// the tag, the id, the role, the classes (joined) and the accessible
	// name MaxElementName each; MaxElementText the text,
	// MaxElementSelector the selector and the hit test's; at most
	// MaxElementAttributes attributes, a name of MaxElementAttributeName
	// and a value of MaxElementAttributeValue; MaxElementHTML the HTML and
	// MaxNeighbourhood its parent's; at most MaxAncestors ancestors, each
	// MaxAncestor as encoded; at most MaxStyles styles, a property of
	// MaxStyleName and a value of MaxStyleValue; at most MaxHints source
	// hints, a file of MaxHintFile and a component of MaxHintComponent.
	MaxElementName           = 1024
	MaxElementText           = 1024
	MaxElementSelector       = 1024
	MaxElementAttributes     = 32
	MaxElementAttributeName  = 64
	MaxElementAttributeValue = 512
	MaxElementHTML           = 12 << 10
	MaxNeighbourhood         = 24 << 10
	MaxAncestors             = 12
	MaxAncestor              = 512
	MaxStyles                = 64
	MaxStyleName             = 64
	MaxStyleValue            = 256
	MaxHints                 = 8
	MaxHintFile              = 1024
	MaxHintComponent         = 256
	// maxPreviewInt is the largest integer a message carries (an epoch, a
	// line), the largest a JavaScript number holds exactly.
	maxPreviewInt = 1<<53 - 1
)

// PreviewHintKinds are how a page says what drew an element, in a hint's
// how and a ready's hints: a data-conductor-source attribute, a Vue dev
// build's component, Svelte's __svelte_meta, React's _debugSource.
var PreviewHintKinds = []string{"attribute", "vue", "svelte", "react"}

// ElementCuts are the steps the bridge cuts a report by to fit
// MaxElementReport, in their order, as its cut names them.
var ElementCuts = []string{"neighbourhood", "html", "styles", "attributes", "tag", "hints", "ancestors", "attribute-count", "names", "style-count"}

var (
	// previewNonce is a hello's nonce: 16 to 64 letters, digits, - and _.
	previewNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	// previewRevision is a page's declared revision: a commit, and its
	// content after one +, lower-case hex.
	previewRevision = regexp.MustCompile(`^[0-9a-f]{4,64}(?:\+[0-9a-f]{4,64})?$`)
)

// PreviewViewport is the page's window as the bridge reads it: its size
// in CSS pixels, its device pixel ratio, its scroll offsets, its colour
// scheme and the browser's user agent.
type PreviewViewport struct {
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	DPR         float64 `json:"dpr"`
	ScrollX     float64 `json:"scrollX"`
	ScrollY     float64 `json:"scrollY"`
	ColorScheme string  `json:"colorScheme"`
	UserAgent   string  `json:"userAgent"`
}

// ElementBox is an element's box (getBoundingClientRect) in CSS pixels.
type ElementBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ElementState is what an element's state says of whether it can be used.
type ElementState struct {
	Disabled      bool   `json:"disabled"`
	Hidden        bool   `json:"hidden"`
	AriaDisabled  string `json:"ariaDisabled"`
	AriaExpanded  string `json:"ariaExpanded"`
	PointerEvents string `json:"pointerEvents"`
	Inert         bool   `json:"inert"`
	Hovered       bool   `json:"hovered"`
	Motion        int64  `json:"motion"`
}

// ElementHit is the element a click at the box's centre reaches
// (elementFromPoint): Self when it is the element or inside it.
type ElementHit struct {
	Self     bool   `json:"self"`
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
}

// ElementAncestor is one of an element's ancestors, innermost first.
type ElementAncestor struct {
	Tag     string `json:"tag"`
	ID      string `json:"id"`
	Classes string `json:"classes"`
	Role    string `json:"role"`
}

// ElementHint is what a page's dev build says drew an element.
type ElementHint struct {
	How       string `json:"how"`
	File      string `json:"file"`
	Line      int64  `json:"line"`
	Column    int64  `json:"column"`
	Component string `json:"component"`
}

// ElementReport is the bridge's report of one element, every field always
// there: Attributes and Styles are name and value pairs, HTML its outer
// HTML with every input's value and password field emptied, cut, and Cut
// the steps it was cut by to fit MaxElementReport.
type ElementReport struct {
	Tag           string            `json:"tag"`
	ID            string            `json:"id"`
	Classes       string            `json:"classes"`
	Role          string            `json:"role"`
	Name          string            `json:"name"`
	Text          string            `json:"text"`
	Selector      string            `json:"selector"`
	Box           ElementBox        `json:"box"`
	Attributes    [][2]string       `json:"attributes"`
	State         ElementState      `json:"state"`
	Hit           ElementHit        `json:"hit"`
	HTML          string            `json:"html"`
	Neighbourhood string            `json:"neighbourhood"`
	Ancestors     []ElementAncestor `json:"ancestors"`
	Styles        [][2]string       `json:"styles"`
	Hints         []ElementHint     `json:"hints"`
	Cut           []string          `json:"cut"`
}

// PreviewMessage is one of the bridge's messages, of the type T: which
// fields it carries is the type's (ParsePreviewMessage).
type PreviewMessage struct {
	Conductor string           `json:"conductor"`
	T         string           `json:"t"`
	Nonce     string           `json:"nonce"`
	Selector  string           `json:"selector,omitempty"`
	Version   int              `json:"version,omitempty"`
	URL       string           `json:"url,omitempty"`
	Title     string           `json:"title,omitempty"`
	Viewport  *PreviewViewport `json:"viewport,omitempty"`
	Epoch     int64            `json:"epoch,omitempty"`
	Revision  string           `json:"revision,omitempty"`
	Hints     []string         `json:"hints,omitempty"`
	Element   *ElementReport   `json:"element,omitempty"`
	Missing   bool             `json:"missing,omitempty"`
}

// errPreview is every refusal of ParsePreviewMessage: what it names is for
// a test, never shown to the page.
var errPreview = errors.New("not a preview message")

// PreviewMeasure is v's size as the bridge and the server count it: the
// UTF-8 length of its JSON as Go writes it with HTML escaping off (U+2028,
// U+2029 and control characters but \b, \f, \n, \r and \t in six bytes,
// invalid UTF-8 as U+FFFD). web/app/utils/protocol.ts's previewMeasure
// counts the same.
func PreviewMeasure(v any) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return math.MaxInt
	}
	return b.Len() - 1 // the newline Encode ends with
}

// ParsePreviewMessage reads data as one of the bridge's messages and holds
// it to its type's schema: every field the type carries there and no
// other, each of its kind and within its bound, the whole within the
// bound of its direction (MaxPreviewToBridge or MaxPreviewToWorkbench) and
// an element within MaxElementReport. A hello gives the nonce: any
// well-formed one passes; every other message must carry nonce, the one
// the hello gave ("" before any hello refuses them all). Anything else is
// refused.
func ParsePreviewMessage(data []byte, nonce string) (PreviewMessage, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return PreviewMessage{}, errPreview
	}
	if err := checkPreview(v, nonce); err != nil {
		return PreviewMessage{}, err
	}
	var m PreviewMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return PreviewMessage{}, errPreview
	}
	return m, nil
}

// previewFields are the fields each type carries beside conductor, t and
// nonce; described carries element or missing, one of them.
var previewFields = map[string][]string{
	PreviewHello: nil, PreviewSelect: nil, PreviewLook: nil, PreviewCancel: nil, PreviewCancelled: nil,
	PreviewDescribe:  {"selector"},
	PreviewReady:     {"version", "url", "title", "viewport", "epoch", "revision", "hints"},
	PreviewSelected:  {"element", "url", "title", "viewport", "epoch", "revision"},
	PreviewDescribed: {"url", "title", "viewport", "epoch", "revision"},
	PreviewLooked:    {"url", "title", "viewport", "epoch", "revision"},
}

// toBridge reports whether t is a type the workbench sends.
func toBridge(t string) bool {
	switch t {
	case PreviewHello, PreviewSelect, PreviewDescribe, PreviewLook, PreviewCancel:
		return true
	}
	return false
}

func checkPreview(v any, nonce string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errPreview
	}
	t, _ := m["t"].(string)
	fields, known := previewFields[t]
	if !known || m["conductor"] != PreviewMark {
		return fmt.Errorf("%w: type %q, mark %v", errPreview, t, m["conductor"])
	}
	most := MaxPreviewToWorkbench
	if toBridge(t) {
		most = MaxPreviewToBridge
	}
	if PreviewMeasure(v) > most {
		return fmt.Errorf("%w: past %d bytes", errPreview, most)
	}
	keys := append([]string{"conductor", "t", "nonce"}, fields...)
	if t == PreviewDescribed {
		if _, el := m["element"]; el {
			keys = append(keys, "element")
		} else {
			keys = append(keys, "missing")
		}
	}
	if !exactKeys(m, keys) {
		return fmt.Errorf("%w: %s's fields", errPreview, t)
	}
	got, _ := m["nonce"].(string)
	if !previewNonce.MatchString(got) || (t != PreviewHello && got != nonce) {
		return fmt.Errorf("%w: the nonce", errPreview)
	}
	switch t {
	case PreviewDescribe:
		if s, ok := m["selector"].(string); !ok || s == "" || measured(s) > MaxPreviewSelector {
			return fmt.Errorf("%w: the selector", errPreview)
		}
	case PreviewReady, PreviewSelected, PreviewDescribed, PreviewLooked:
		if !pageFields(m) {
			return fmt.Errorf("%w: the page", errPreview)
		}
	}
	switch t {
	case PreviewReady:
		if n, ok := m["version"].(float64); !ok || n != PreviewVersion || !hintKinds(m["hints"]) {
			return fmt.Errorf("%w: the version or the hints", errPreview)
		}
	case PreviewSelected:
		if !validElement(m["element"]) {
			return fmt.Errorf("%w: the element", errPreview)
		}
	case PreviewDescribed:
		if el, ok := m["element"]; ok && !validElement(el) || !ok && m["missing"] != true {
			return fmt.Errorf("%w: the element", errPreview)
		}
	}
	return nil
}

// exactKeys reports whether m has every key of keys and no other.
func exactKeys(m map[string]any, keys []string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// measured is s's size in a message, quotes left out.
func measured(s string) int { return PreviewMeasure(s) - 2 }

// str reports whether v is a string of at most most bytes as measured.
func str(v any, most int) bool {
	s, ok := v.(string)
	return ok && measured(s) <= most
}

// num reports whether v is a number, at least min.
func num(v any, min float64) bool {
	n, ok := v.(float64)
	return ok && n >= min
}

// integer reports whether v is a whole number of 0 to maxPreviewInt.
func integer(v any) bool {
	n, ok := v.(float64)
	return ok && n >= 0 && n <= maxPreviewInt && n == math.Trunc(n)
}

func boolean(v any) bool { _, ok := v.(bool); return ok }

// object is v as an object with exactly keys, or nil.
func object(v any, keys ...string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || !exactKeys(m, keys) {
		return nil
	}
	return m
}

// list is v as an array of at most most items, each passing item.
func list(v any, most int, item func(any) bool) bool {
	a, ok := v.([]any)
	if !ok || len(a) > most {
		return false
	}
	for _, x := range a {
		if !item(x) {
			return false
		}
	}
	return true
}

// pair is a name and value pair within its bounds.
func pair(name, value int) func(any) bool {
	return func(v any) bool {
		a, ok := v.([]any)
		return ok && len(a) == 2 && str(a[0], name) && str(a[1], value)
	}
}

// pageFields checks what every answer carries of the page: its URL, its
// title, its viewport, its epoch and its declared revision.
func pageFields(m map[string]any) bool {
	title, ok := m["title"].(string)
	if !ok || utf8.RuneCountInString(title) > MaxPreviewTitle || !str(m["url"], MaxPreviewURL) || !integer(m["epoch"]) {
		return false
	}
	if rev, ok := m["revision"].(string); !ok || rev != "" && (len(rev) > MaxPreviewRevision || !previewRevision.MatchString(rev)) {
		return false
	}
	vp := object(m["viewport"], "width", "height", "dpr", "scrollX", "scrollY", "colorScheme", "userAgent")
	if vp == nil || !num(vp["width"], 0) || !num(vp["height"], 0) || !num(vp["dpr"], math.SmallestNonzeroFloat64) ||
		!num(vp["scrollX"], -math.MaxFloat64) || !num(vp["scrollY"], -math.MaxFloat64) || !str(vp["userAgent"], MaxPreviewUserAgent) {
		return false
	}
	return vp["colorScheme"] == "light" || vp["colorScheme"] == "dark"
}

// hintKinds reports whether v is a list of distinct hint kinds.
func hintKinds(v any) bool {
	a, ok := v.([]any)
	if !ok || len(a) > len(PreviewHintKinds) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		s, _ := x.(string)
		if !slices.Contains(PreviewHintKinds, s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// validElement holds v to the element report's schema, each field within
// its bound, and the whole within MaxElementReport.
func validElement(v any) bool {
	e := object(v, "tag", "id", "classes", "role", "name", "text", "selector", "box", "attributes", "state", "hit", "html", "neighbourhood", "ancestors", "styles", "hints", "cut")
	if e == nil || PreviewMeasure(e) > MaxElementReport {
		return false
	}
	for _, k := range []string{"tag", "id", "classes", "role", "name"} {
		if !str(e[k], MaxElementName) {
			return false
		}
	}
	if !str(e["text"], MaxElementText) || !str(e["selector"], MaxElementSelector) || !str(e["html"], MaxElementHTML) || !str(e["neighbourhood"], MaxNeighbourhood) {
		return false
	}
	box := object(e["box"], "x", "y", "width", "height")
	if box == nil || !num(box["x"], -math.MaxFloat64) || !num(box["y"], -math.MaxFloat64) || !num(box["width"], 0) || !num(box["height"], 0) {
		return false
	}
	st := object(e["state"], "disabled", "hidden", "ariaDisabled", "ariaExpanded", "pointerEvents", "inert", "hovered", "motion")
	if st == nil || !boolean(st["disabled"]) || !boolean(st["hidden"]) || !boolean(st["inert"]) || !boolean(st["hovered"]) || !integer(st["motion"]) ||
		!str(st["ariaDisabled"], MaxElementName) || !str(st["ariaExpanded"], MaxElementName) || !str(st["pointerEvents"], MaxElementName) {
		return false
	}
	hit := object(e["hit"], "self", "selector", "tag")
	if hit == nil || !boolean(hit["self"]) || !str(hit["selector"], MaxElementSelector) || !str(hit["tag"], MaxElementName) {
		return false
	}
	ancestor := func(v any) bool {
		a := object(v, "tag", "id", "classes", "role")
		return a != nil && str(a["tag"], MaxAncestor) && str(a["id"], MaxAncestor) && str(a["classes"], MaxAncestor) && str(a["role"], MaxAncestor) && PreviewMeasure(a) <= MaxAncestor
	}
	hint := func(v any) bool {
		h := object(v, "how", "file", "line", "column", "component")
		how, _ := h["how"].(string)
		return h != nil && slices.Contains(PreviewHintKinds, how) && str(h["file"], MaxHintFile) && integer(h["line"]) && integer(h["column"]) && str(h["component"], MaxHintComponent)
	}
	cut := func(v any) bool { s, _ := v.(string); return slices.Contains(ElementCuts, s) }
	return list(e["attributes"], MaxElementAttributes, pair(MaxElementAttributeName, MaxElementAttributeValue)) &&
		list(e["ancestors"], MaxAncestors, ancestor) && list(e["styles"], MaxStyles, pair(MaxStyleName, MaxStyleValue)) &&
		list(e["hints"], MaxHints, hint) && list(e["cut"], len(ElementCuts), cut)
}
