package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// The preview bridge's messages (docs/protocol.md, Preview bridge): one
// fixture, testdata/preview-messages.json, that this test and
// web/app/utils/protocol.test.ts both judge, so the Go and the TypeScript
// definitions cannot drift. The fixture is what previewCases builds: a
// change to the cases is written with CONDUCTOR_WRITE_FIXTURES=1 and
// committed; without it the test fails when the file is not what they build.
// Each case's verdict is written here by hand, never computed by a parser.

const previewFixture = "testdata/preview-messages.json"

// The nonce the cases' hello gave, and another.
const (
	fixtureNonce = "n0nce-of-the-hello-1234"
	otherNonce   = "another-nonce-0000000"
)

// previewWays are the fixture's names of the two ways a message travels.
var previewWays = map[string]PreviewDirection{"bridge": PreviewToBridge, "workbench": PreviewToWorkbench}

type previewCase struct {
	Name  string `json:"name"`
	To    string `json:"to"`    // the way the parser is told the message travels: bridge or workbench
	Nonce string `json:"nonce"` // the nonce the parser holds: the hello's, or "" before any
	// Message is the message's text as both parsers read it, spelled as
	// written here (a number as 3.0, an escaped key). For an accepted
	// message, the content each parser gives is the content of this text.
	Message json.RawMessage `json:"message"`
	OK      bool            `json:"ok"`
	// AsText marks a text no value is (one that gives a member twice):
	// the server reads it and refuses it; the app's parser, handed values,
	// never meets it, so its suite leaves the case out.
	AsText bool `json:"asText,omitempty"`
}

// measureCase is a string given as its UTF-16 code units (a lone surrogate
// has no other form both languages read alike) and its size in a message
// as Go writes it, quotes left out.
type measureCase struct {
	Name  string   `json:"name"`
	Units []uint16 `json:"units"`
	Bytes int      `json:"bytes"`
}

// previewFile is the fixture: the bounds both parsers hold, by their names
// in preview.go, the cut's steps and the hint kinds, the measures and the
// messages.
type previewFile struct {
	Comment string         `json:"comment"`
	Bounds  map[string]int `json:"bounds"`
	// TextBounds are the bounds of a message's text only the server holds,
	// written here as numbers, not read from preview.go.
	TextBounds map[string]int `json:"textBounds"`
	Cuts       []string       `json:"cuts"`
	HintKinds  []string       `json:"hintKinds"`
	Measures   []measureCase  `json:"measures"`
	Messages   []previewCase  `json:"messages"`
}

type obj = map[string]any

func page() obj {
	return obj{"url": "http://127.0.0.1:5173/settings", "title": "Settings", "epoch": 3, "revision": "",
		"viewport": obj{"width": 1280, "height": 800, "dpr": 2, "scrollX": 0, "scrollY": 120.5, "colorScheme": "dark", "userAgent": "Mozilla/5.0"}}
}

func element() obj {
	return obj{"tag": "button", "id": "save", "classes": "btn save", "role": "", "name": "Save", "text": "Save", "selector": "#save",
		"box":        obj{"x": 10, "y": 20.25, "width": 80, "height": 32},
		"attributes": []any{[]any{"id", "save"}, []any{"class", "btn save"}},
		"state":      obj{"disabled": false, "hidden": false, "ariaDisabled": "", "ariaExpanded": "", "pointerEvents": "auto", "inert": false, "hovered": false, "motion": 0},
		"hit":        obj{"self": false, "selector": "div.overlay", "tag": "div"},
		"html":       `<button id="save" class="btn save">Save</button>`, "neighbourhood": `<div class="bar"><button id="save" class="btn save">Save</button></div>`,
		"ancestors": []any{obj{"tag": "div", "id": "", "classes": "bar", "role": ""}},
		"styles":    []any{[]any{"display", "inline-block"}, []any{"pointer-events", "auto"}},
		"hints":     []any{obj{"how": "vue", "file": "/src/app/components/SaveBar.vue", "line": 0, "column": 0, "component": "SaveBar"}},
		"cut":       []any{},
	}
}

func msg(t string, nonce string, fields ...obj) obj {
	m := obj{"conductor": PreviewMark, "t": t, "nonce": nonce}
	for _, f := range fields {
		maps.Copy(m, f)
	}
	return m
}

// set is m with path's member (an object's key, or a list's index in
// digits) set to v, or removed from its object when gone.
func set(m obj, v any, gone bool, path ...string) obj {
	out := deepCopy(m).(obj)
	var at any = out
	for _, k := range path[:len(path)-1] {
		if a, ok := at.([]any); ok {
			i, _ := strconv.Atoi(k)
			at = a[i]
		} else {
			at = at.(obj)[k]
		}
	}
	last := path[len(path)-1]
	switch c := at.(type) {
	case []any:
		i, _ := strconv.Atoi(last)
		c[i] = v
	case obj:
		if gone {
			delete(c, last)
		} else {
			c[last] = v
		}
	}
	return out
}

// with is m with path's member set to v, or removed for a nil v.
func with(m obj, v any, path ...string) obj { return set(m, v, v == nil, path...) }

// nulled is m with path's member there and null.
func nulled(m obj, path ...string) obj { return set(m, nil, false, path...) }

// writeText is v as a message's text is written: JSON with HTML escaping
// off, as JSON.stringify writes it but for U+2028, U+2029 (six bytes
// here) and a negative zero (-0 here).
func writeText(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case obj:
		out := obj{}
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// pad is a string of n measured bytes.
func pad(n int) string { return strings.Repeat("a", n) }

// width is a character and the bytes it measures in a message.
type width struct {
	name string
	c    string
	n    int
}

// byteWidths are characters of every measure a message's bounds count:
// one byte, two (a quote, escaped), three, and six (U+2028, escaped).
var byteWidths = []width{{"one-byte", "a", 1}, {"two-byte", `"`, 2}, {"three-byte", "€", 3}, {"six-byte", lineSeparator, 6}}

// lineSeparator is U+2028, which a message's measure counts as its
// six-byte escape.
const lineSeparator = "\xe2\x80\xa8"

// fill is a string of n measured bytes in w's characters, but its last
// n%w.n bytes.
func (w width) fill(n int) string { return strings.Repeat(w.c, n/w.n) + pad(n%w.n) }

func repeat(n int, v any) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = deepCopy(v)
	}
	return out
}

// sized is e with its neighbourhood grown by fill so the whole measures n bytes.
func sized(e obj, n int, fill func(int) string) obj {
	e = with(e, pad(MaxElementHTML), "html")
	e = with(e, "", "neighbourhood")
	return with(e, fill(n-PreviewMeasure(e)), "neighbourhood")
}

// The kinds of bound: a string's measured bytes (at its bound and one
// past, in one-byte and in three-byte characters), a string's characters,
// a list's items, a revision's bytes (hex), and the measure of a whole the
// member is grown to (as a string's bytes) or past only (a message's,
// which no message otherwise valid reaches).
type boundKind int

const (
	inBytes boundKind = iota
	inChars
	inItems
	inHex
	pastOnly
)

// previewBound is a bound both parsers hold and the member it bounds: its
// constant's name in preview.go and its number, and the message with the
// member at a size, filled by fill where it is a string's or a whole's.
type previewBound struct {
	member string
	bound  string
	n      int
	kind   boundKind
	build  func(size int, fill func(int) string) obj
}

var (
	ancestorBase = obj{"tag": "div", "id": "", "classes": "", "role": ""}
	hintBase     = obj{"how": "attribute", "file": "app/a.vue", "line": 4, "column": 2, "component": ""}
)

func readyMessage() obj {
	return msg(PreviewReady, fixtureNonce, page(), obj{"version": PreviewVersion, "hints": []any{"vue"}})
}

func selectedOf(e obj) obj { return msg(PreviewSelected, fixtureNonce, page(), obj{"element": e}) }

// previewBounds is every bound the two parsers share, each with the member
// it bounds; TestEveryPreviewBoundIsInTheTable holds it to preview.go's
// constants.
func previewBounds() []previewBound {
	text := func(set func(string) obj) func(int, func(int) string) obj {
		return func(size int, fill func(int) string) obj { return set(fill(size)) }
	}
	ready := func(path ...string) func(int, func(int) string) obj {
		return text(func(s string) obj { return with(readyMessage(), s, path...) })
	}
	el := func(path ...string) func(int, func(int) string) obj {
		return text(func(s string) obj { return selectedOf(with(element(), s, path...)) })
	}
	items := func(member string, item func(string) any) func(int, func(int) string) obj {
		return text(func(s string) obj { return selectedOf(with(element(), []any{item(s)}, member)) })
	}
	count := func(member string, item any) func(int, func(int) string) obj {
		return func(k int, _ func(int) string) obj { return selectedOf(with(element(), repeat(k, item), member)) }
	}
	bs := []previewBound{
		{"the describe's selector", "MaxPreviewSelector", MaxPreviewSelector, inBytes, text(func(s string) obj { return msg(PreviewDescribe, fixtureNonce, obj{"selector": s}) })},
		{"the URL", "MaxPreviewURL", MaxPreviewURL, inBytes, ready("url")},
		{"the title", "MaxPreviewTitle", MaxPreviewTitle, inChars, ready("title")},
		{"the user agent", "MaxPreviewUserAgent", MaxPreviewUserAgent, inBytes, ready("viewport", "userAgent")},
		{"the revision", "MaxPreviewRevision", MaxPreviewRevision, inHex, func(k int, _ func(int) string) obj {
			return with(readyMessage(), strings.Repeat("a", 64)+"+"+strings.Repeat("b", k-65), "revision")
		}},
	}
	for _, k := range []string{"tag", "id", "classes", "role", "name"} {
		bs = append(bs, previewBound{"the element's " + k, "MaxElementName", MaxElementName, inBytes, el(k)})
	}
	for _, k := range []string{"ariaDisabled", "ariaExpanded", "pointerEvents"} {
		bs = append(bs, previewBound{"the state's " + k, "MaxElementName", MaxElementName, inBytes, el("state", k)})
	}
	bs = append(bs,
		previewBound{"the element's text", "MaxElementText", MaxElementText, inBytes, el("text")},
		previewBound{"the element's selector", "MaxElementSelector", MaxElementSelector, inBytes, el("selector")},
		previewBound{"the hit test's selector", "MaxElementSelector", MaxElementSelector, inBytes, el("hit", "selector")},
		previewBound{"the hit test's tag", "MaxElementName", MaxElementName, inBytes, el("hit", "tag")},
		previewBound{"the HTML", "MaxElementHTML", MaxElementHTML, inBytes, el("html")},
		previewBound{"the neighbourhood", "MaxNeighbourhood", MaxNeighbourhood, inBytes, el("neighbourhood")},
		previewBound{"the attributes", "MaxElementAttributes", MaxElementAttributes, inItems, count("attributes", []any{"p", "v"})},
		previewBound{"an attribute's name", "MaxElementAttributeName", MaxElementAttributeName, inBytes, items("attributes", func(s string) any { return []any{s, "v"} })},
		previewBound{"an attribute's value", "MaxElementAttributeValue", MaxElementAttributeValue, inBytes, items("attributes", func(s string) any { return []any{"n", s} })},
		previewBound{"the ancestors", "MaxAncestors", MaxAncestors, inItems, count("ancestors", ancestorBase)},
		// An ancestor's fields are bounded by its whole: the whole is grown.
		previewBound{"an ancestor", "MaxAncestor", MaxAncestor, inBytes, func(size int, fill func(int) string) obj {
			return selectedOf(with(element(), []any{with(ancestorBase, fill(size-PreviewMeasure(ancestorBase)), "classes")}, "ancestors"))
		}},
		previewBound{"the styles", "MaxStyles", MaxStyles, inItems, count("styles", []any{"p", "v"})},
		previewBound{"a style's name", "MaxStyleName", MaxStyleName, inBytes, items("styles", func(s string) any { return []any{s, "v"} })},
		previewBound{"a style's value", "MaxStyleValue", MaxStyleValue, inBytes, items("styles", func(s string) any { return []any{"n", s} })},
		previewBound{"the hints", "MaxHints", MaxHints, inItems, count("hints", hintBase)},
		previewBound{"a hint's file", "MaxHintFile", MaxHintFile, inBytes, items("hints", func(s string) any { return with(hintBase, s, "file") })},
		previewBound{"a hint's component", "MaxHintComponent", MaxHintComponent, inBytes, items("hints", func(s string) any { return with(hintBase, s, "component") })},
		previewBound{"an element", "MaxElementReport", MaxElementReport, inBytes, func(size int, fill func(int) string) obj { return selectedOf(sized(element(), size, fill)) }},
		previewBound{"a message to the bridge", "MaxPreviewToBridge", MaxPreviewToBridge, pastOnly, func(size int, fill func(int) string) obj {
			m := msg(PreviewDescribe, fixtureNonce, obj{"selector": ""})
			return with(m, fill(size-PreviewMeasure(m)), "selector")
		}},
		previewBound{"a message to the workbench", "MaxPreviewToWorkbench", MaxPreviewToWorkbench, pastOnly, func(size int, fill func(int) string) obj {
			m := with(readyMessage(), "", "url")
			return with(m, fill(size-PreviewMeasure(m)), "url")
		}},
	)
	return bs
}

// previewTextBounds are the bounds of preview.go only Go holds: a
// message's text before it is decoded, which the app's parser, given a
// value, never sees (TestAPreviewMessagesTextIsBoundedBeforeItIsDecoded).
var previewTextBounds = []string{"MaxPreviewTextToBridge", "MaxPreviewTextToWorkbench"}

func previewCases() previewFile {
	n := fixtureNonce
	sel := selectedOf
	ready := readyMessage()
	var cs []previewCase
	way := func(m any) string {
		if o, ok := m.(obj); ok {
			if t, _ := o["t"].(string); toBridge(t) {
				return "bridge"
			}
		}
		return "workbench"
	}
	marshal := func(m any) string { return string(writeText(m)) }
	addText := func(name, to, nonce, text string, ok bool) {
		cs = append(cs, previewCase{name, to, nonce, json.RawMessage(text), ok, false})
	}
	addTo := func(name, to, nonce string, m any, ok bool) { addText(name, to, nonce, marshal(m), ok) }
	add := func(name, nonce string, m any, ok bool) { addTo(name, way(m), nonce, m, ok) }
	// respell is m's text with old, there once, written as new.
	respell := func(name string, m any, old, new string) string {
		text := marshal(m)
		if strings.Count(text, old) != 1 {
			panic(name + ": " + old + " is not in the message once")
		}
		return strings.Replace(text, old, new, 1)
	}
	spelled := func(name, nonce string, m any, old, new string, ok bool) {
		addText(name, way(m), nonce, respell(name, m, old, new), ok)
	}
	// twice is m's text with a member given twice, old written as new: a
	// text no writer writes and no value holds, refused.
	twice := func(name, nonce string, m any, old, new string) {
		cs = append(cs, previewCase{name + " (no writer writes a member twice)", way(m), nonce, json.RawMessage(respell(name, m, old, new)), false, true})
	}
	// The handshake and the nonce.
	add("hello", "", msg(PreviewHello, n), true)
	add("a second hello replaces the nonce", n, msg(PreviewHello, otherNonce), true)
	add("hello with a nonce too short", "", msg(PreviewHello, "short"), false)
	add("hello with a nonce of another alphabet", "", msg(PreviewHello, "n0nce of the hello 1234"), false)
	add("hello with a field of another type", "", msg(PreviewHello, n, obj{"selector": "#x"}), false)
	add("hello without its nonce", "", with(msg(PreviewHello, n), nil, "nonce"), false)
	add("the wrong conductor mark", "", with(msg(PreviewHello, n), "preview/2", "conductor"), false)
	add("no conductor mark", "", with(msg(PreviewHello, n), nil, "conductor"), false)
	add("an unknown type", n, msg("eval", n), false)
	add("not an object", n, []any{msg(PreviewSelect, n)}, false)
	add("select", n, msg(PreviewSelect, n), true)
	add("select before any hello", "", msg(PreviewSelect, n), false)
	add("select with the wrong nonce", n, msg(PreviewSelect, otherNonce), false)
	add("select without its nonce", n, with(msg(PreviewSelect, n), nil, "nonce"), false)
	add("look", n, msg(PreviewLook, n), true)
	add("cancel", n, msg(PreviewCancel, n), true)
	add("describe with six-byte characters past its bound", n, msg(PreviewDescribe, n, obj{"selector": strings.Repeat(" ", MaxPreviewSelector/6+1)}), false)
	add("describe with an empty selector", n, msg(PreviewDescribe, n, obj{"selector": ""}), false)
	add("describe without a selector", n, msg(PreviewDescribe, n), false)
	// Each way's types alone: a parser told the other way refuses them.
	addTo("a hello sent to the workbench", "workbench", "", msg(PreviewHello, n), false)
	addTo("a select sent to the workbench", "workbench", n, msg(PreviewSelect, n), false)
	addTo("a ready sent to the bridge", "bridge", n, ready, false)
	addTo("a cancelled sent to the bridge", "bridge", n, msg(PreviewCancelled, n), false)
	// The answers and the page they carry.
	add("ready", n, ready, true)
	add("ready of another version", n, with(ready, 2, "version"), false)
	add("ready with every hint kind", n, with(ready, []any{"attribute", "vue", "svelte", "react"}, "hints"), true)
	add("ready with a hint kind twice", n, with(ready, []any{"vue", "vue"}, "hints"), false)
	add("ready with one hint kind past their count", n, with(ready, []any{"attribute", "vue", "svelte", "react", "vue"}, "hints"), false)
	add("ready with an unknown hint kind", n, with(ready, []any{"angular"}, "hints"), false)
	add("ready without its hints", n, with(ready, nil, "hints"), false)
	add("ready with the wrong nonce", n, with(ready, otherNonce, "nonce"), false)
	add("a revision in capitals", n, with(ready, "A1B2C3D", "revision"), false)
	add("a revision that is not hex", n, with(ready, "main", "revision"), false)
	add("another colour scheme", n, with(ready, "sepia", "viewport", "colorScheme"), false)
	add("a colour scheme in capitals", n, with(ready, "Dark", "viewport", "colorScheme"), false)
	add("a viewport with a field of its own", n, with(ready, 1, "viewport", "zoom"), false)
	add("a negative scroll", n, with(ready, -4, "viewport", "scrollY"), true)
	add("a negative horizontal scroll", n, with(ready, -4, "viewport", "scrollX"), true)
	add("a viewport of negative width", n, with(ready, -1, "viewport", "width"), false)
	add("a viewport of negative height", n, with(ready, -1, "viewport", "height"), false)
	add("an epoch that is not whole", n, with(ready, 1.5, "epoch"), false)
	add("a negative epoch", n, with(ready, -1, "epoch"), false)
	add("the largest epoch a number holds", n, with(ready, maxPreviewInt, "epoch"), true)
	add("an epoch one past the largest a number holds", n, with(ready, maxPreviewInt+1, "epoch"), false)
	add("an epoch that is a string", n, with(ready, "3", "epoch"), false)
	add("a page without its URL", n, with(ready, nil, "url"), false)
	add("looked", n, msg(PreviewLooked, n, page()), true)
	add("looked without its page", n, msg(PreviewLooked, n), false)
	add("cancelled", n, msg(PreviewCancelled, n), true)
	add("cancelled with a page", n, msg(PreviewCancelled, n, page()), false)
	// The element report.
	add("selected", n, sel(element()), true)
	add("selected without its element", n, msg(PreviewSelected, n, page()), false)
	add("an element without its hit test", n, sel(with(element(), nil, "hit")), false)
	add("an element with a field of its own", n, sel(with(element(), "x", "value")), false)
	add("an attribute of three parts", n, sel(with(element(), []any{[]any{"a", "b", "c"}}, "attributes")), false)
	add("an attribute of one part", n, sel(with(element(), []any{[]any{"a"}}, "attributes")), false)
	add("an ancestor with a field of its own", n, sel(with(element(), []any{with(ancestorBase, "x", "lang")}, "ancestors")), false)
	add("a hint of an unknown kind", n, sel(with(element(), []any{with(hintBase, "angular", "how")}, "hints")), false)
	add("a hint's line that is not whole", n, sel(with(element(), []any{with(hintBase, 1.5, "line")}, "hints")), false)
	add("a hint's line that is negative", n, sel(with(element(), []any{with(hintBase, -1, "line")}, "hints")), false)
	add("a hint's column that is not whole", n, sel(with(element(), []any{with(hintBase, 1.5, "column")}, "hints")), false)
	add("a hint's column that is negative", n, sel(with(element(), []any{with(hintBase, -1, "column")}, "hints")), false)
	add("a state's motion that is not whole", n, sel(with(element(), 0.5, "state", "motion")), false)
	add("a state's motion that is negative", n, sel(with(element(), -1, "state", "motion")), false)
	for _, k := range []string{"disabled", "hidden", "inert", "hovered"} {
		add("a state's "+k+" that is a string", n, sel(with(element(), "false", "state", k)), false)
	}
	add("a hit test's self that is a string", n, sel(with(element(), "false", "hit", "self")), false)
	add("a box of negative width", n, sel(with(element(), -1, "box", "width")), false)
	add("a box of negative height", n, sel(with(element(), -1, "box", "height")), false)
	add("a box at negative coordinates", n, sel(with(with(element(), -40, "box", "x"), -8.5, "box", "y")), true)
	// The cut: the first steps of ElementCuts, in their order.
	add("a cut of its first two steps", n, sel(with(element(), []any{"neighbourhood", "html"}, "cut")), true)
	add("a cut of every step", n, sel(with(element(), toAny(ElementCuts), "cut")), true)
	add("a cut of one step past every step", n, sel(with(element(), append(toAny(ElementCuts), "neighbourhood"), "cut")), false)
	add("a cut that skips steps", n, sel(with(element(), []any{"neighbourhood", "html", "style-count"}, "cut")), false)
	add("a cut that does not start at the first step", n, sel(with(element(), []any{"html"}, "cut")), false)
	add("a cut with a step twice", n, sel(with(element(), []any{"neighbourhood", "neighbourhood"}, "cut")), false)
	add("a cut out of order", n, sel(with(element(), []any{"html", "neighbourhood"}, "cut")), false)
	add("a cut step of its own", n, sel(with(element(), []any{"everything"}, "cut")), false)
	add("described with its element", n, msg(PreviewDescribed, n, page(), obj{"element": element()}), true)
	add("described as missing", n, msg(PreviewDescribed, n, page(), obj{"missing": true}), true)
	add("described with neither", n, msg(PreviewDescribed, n, page()), false)
	add("described with both", n, msg(PreviewDescribed, n, page(), obj{"element": element(), "missing": true}), false)
	add("described as missing false", n, msg(PreviewDescribed, n, page(), obj{"missing": false}), false)
	// Every bound both parsers hold, at it and one past.
	for _, b := range previewBounds() {
		switch b.kind {
		case inBytes:
			for _, w := range byteWidths {
				add(b.member+" at its bound in "+w.name+" characters", n, b.build(b.n, w.fill), true)
				add(b.member+" one "+w.name+" character past", n, b.build(b.n+w.n, w.fill), false)
			}
		case inChars:
			for _, c := range []string{"a", `"`, "é", lineSeparator, "😀"} {
				chars := func(k int) string { return strings.Repeat(c, k) }
				add(b.member+" at its bound in characters "+strconv.QuoteToASCII(c), n, b.build(b.n, chars), true)
				add(b.member+" one character "+strconv.QuoteToASCII(c)+" past", n, b.build(b.n+1, chars), false)
			}
		case inItems, inHex:
			add(b.member+" at their bound", n, b.build(b.n, nil), true)
			add(b.member+" one past", n, b.build(b.n+1, nil), false)
		case pastOnly:
			add(b.member+" one byte past its bound", n, b.build(b.n+1, pad), false)
		}
	}
	// A number is its value, never its spelling; a negative zero measures
	// as 0 in both; a member given twice is refused; a key is exactly its
	// own letters; null is no value of any field.
	spelled("a version written 1.0", n, ready, `"version":1`, `"version":1.0`, true)
	spelled("a version written 1e0", n, ready, `"version":1`, `"version":1e0`, true)
	spelled("an epoch written 3.0", n, ready, `"epoch":3`, `"epoch":3.0`, true)
	spelled("an epoch written 3e0", n, ready, `"epoch":3`, `"epoch":3e0`, true)
	spelled("an epoch written 0.3e1", n, ready, `"epoch":3`, `"epoch":0.3e1`, true)
	spelled("an epoch written -0", n, ready, `"epoch":3`, `"epoch":-0`, true)
	spelled("an epoch written -0.0", n, ready, `"epoch":3`, `"epoch":-0.0`, true)
	spelled("the largest epoch written with a fraction", n, ready, `"epoch":3`, `"epoch":9007199254740991.0`, true)
	spelled("an epoch written 3.5e0", n, ready, `"epoch":3`, `"epoch":3.5e0`, false)
	spelled("a motion written 0.0", n, sel(element()), `"motion":0`, `"motion":0.0`, true)
	spelled("a hint's line written 0e0", n, sel(element()), `"line":0`, `"line":0e0`, true)
	negZero := math.Copysign(0, -1)
	add("an element at its bound with a negative zero", n, sel(sized(with(element(), negZero, "box", "x"), MaxElementReport, pad)), true)
	add("an element one byte past with a negative zero", n, sel(sized(with(element(), negZero, "box", "x"), MaxElementReport+1, pad)), false)
	add("an element at its bound with a negative zero in each number", n, sel(sized(with(with(with(element(), negZero, "box", "y"), negZero, "box", "height"), negZero, "state", "motion"), MaxElementReport, pad)), true)
	describe := msg(PreviewDescribe, n, obj{"selector": "#x"})
	twice("a selector given twice, the same both times", n, describe, `"selector":"#x"`, `"selector":"#x","selector":"#x"`)
	twice("a selector given twice, the first one empty", n, describe, `"selector":"#x"`, `"selector":"","selector":"#x"`)
	twice("a nonce given twice, the wrong one first", n, describe, `"nonce":"`+n+`"`, `"nonce":"`+otherNonce+`","nonce":"`+n+`"`)
	twice("a nonce given twice, a number first", n, describe, `"nonce":"`+n+`"`, `"nonce":1,"nonce":"`+n+`"`)
	twice("a type given twice", n, describe, `"t":"describe"`, `"t":"hello","t":"describe"`)
	twice("an epoch given twice, the first past a double", n, ready, `"epoch":3`, `"epoch":1e400,"epoch":3`)
	twice("a box's x given twice", n, sel(element()), `"x":10`, `"x":10,"x":10`)
	// A string is well-formed: one that escapes half of a surrogate pair
	// alone is refused by both (the server reads the escape; the app's
	// parser is handed the lone code unit), a pair escaped passes.
	for _, s := range []struct {
		name, text string
		ok         bool
	}{
		{"a high surrogate alone", `#\ud800`, false},
		{"a low surrogate alone", `#\udc00x`, false},
		{"a pair reversed", `#\udc00\ud800`, false},
		{"a high surrogate at the end", `#x\udbff`, false},
		{"a pair escaped", `#\` + `ud83d\` + `ude00`, true},
		{"a backslash before a u", `#\\ud800`, true},
	} {
		spelled("a selector holding "+s.name, n, describe, `"#x"`, `"`+s.text+`"`, s.ok)
		spelled("an element's selector holding "+s.name, n, sel(element()), `"#save"`, `"`+s.text+`"`, s.ok)
	}
	// The corners of the surrogates' halves, each escape in lower, upper
	// and mixed case: each half alone refused, the pairs of the first high
	// and the first low half and of the last of each passed.
	esc := func(hex string) string { return `\` + "u" + hex }
	cases := func(hex string) []string {
		mixed := []byte(hex)
		for i := range mixed {
			if i%2 == 0 {
				mixed[i] = strings.ToUpper(hex)[i]
			}
		}
		return slices.Compact([]string{hex, strings.ToUpper(hex), string(mixed)})
	}
	for _, half := range []string{"d800", "dbff", "dc00", "dfff"} {
		for _, h := range cases(half) {
			spelled("a selector holding the escape u"+h+" alone", n, describe, `"#x"`, `"#`+esc(h)+`"`, false)
			spelled("an element's selector holding the escape u"+h+" alone", n, sel(element()), `"#save"`, `"#`+esc(h)+`"`, false)
		}
	}
	for _, p := range [][2]string{{"d800", "dc00"}, {"dbff", "dfff"}} {
		hs, ls := cases(p[0]), cases(p[1])
		for i := range hs {
			pair := esc(hs[i]) + esc(ls[min(i, len(ls)-1)])
			spelled("a selector holding the pair u"+hs[i]+" u"+ls[min(i, len(ls)-1)], n, describe, `"#x"`, `"#`+pair+`"`, true)
			spelled("an element's selector holding the pair u"+hs[i]+" u"+ls[min(i, len(ls)-1)], n, sel(element()), `"#save"`, `"#`+pair+`"`, true)
		}
	}
	// lone is m, whose member holds loneMark alone, with that member holding
	// a low surrogate alone: refused, whatever the member.
	const loneMark = "a-member-to-hold-a-lone-surrogate"
	lone := func(what string, m obj) {
		spelled(what+" holding a lone surrogate", n, m, `"`+loneMark+`"`, `"a`+esc("dfff")+`"`, false)
	}
	// The schema itself: every object of a message (each type's own, the
	// viewport and each object of the element report) with a member of its
	// own, and each of its members null and of another kind; each refused.
	other := map[reflect.Kind]any{reflect.String: 0, reflect.Float64: "0", reflect.Int64: "0", reflect.Int: "0", reflect.Bool: 0,
		reflect.Slice: obj{}, reflect.Array: obj{}, reflect.Pointer: []any{}, reflect.Struct: []any{}}
	kinds := func(t reflect.Type) map[string]reflect.Kind {
		out := map[string]reflect.Kind{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			out[name] = f.Type.Kind()
		}
		return out
	}
	// The strings the schema refuses empty, by their keys: the mark, the
	// type, the nonce, the colour scheme and a hint's kind (each one of a
	// set or of a pattern no empty string is in), and a describe's selector.
	// Every other string may be empty: a page without a title, an element
	// without text, a hint without a file.
	refusesEmpty := map[string]bool{"conductor": true, "t": true, "nonce": true, "colorScheme": true, "how": true}
	members := func(what string, m obj, path []string, k map[string]reflect.Kind, keys []string) {
		add(what+" with a member of its own", n, with(m, "x", append(slices.Clone(path), "own")...), false)
		for _, key := range keys {
			at := append(slices.Clone(path), key)
			kind, ok := k[key]
			if !ok {
				panic(what + ": no kind for " + key)
			}
			add(what+"'s "+key+" of null", n, nulled(m, at...), false)
			add(what+"'s "+key+" of another kind", n, with(m, other[kind], at...), false)
			// Every string member with a lone surrogate and empty; every flag
			// (but missing, which is true alone) given its other value, so
			// that each takes both across the accepted rows.
			switch {
			case kind == reflect.String:
				lone(what+"'s "+key, with(m, loneMark, at...))
				add(what+"'s "+key+" empty", n, with(m, "", at...), !refusesEmpty[key] && !(what == "a describe" && key == "selector"))
			case kind == reflect.Bool && key != "missing":
				add(what+"'s "+key+" true", n, with(m, true, at...), true)
			}
		}
	}
	message := kinds(reflect.TypeFor[PreviewMessage]())
	for _, m := range []obj{msg(PreviewHello, n), msg(PreviewSelect, n), msg(PreviewLook, n), msg(PreviewCancel, n), describe, ready, sel(element()),
		msg(PreviewDescribed, n, page(), obj{"element": element()}), msg(PreviewDescribed, n, page(), obj{"missing": true}),
		msg(PreviewLooked, n, page()), msg(PreviewCancelled, n)} {
		what := "a " + m["t"].(string)
		if _, ok := m["missing"]; ok {
			what += " as missing"
		}
		members(what, m, nil, message, slices.Sorted(maps.Keys(m)))
	}
	for _, o := range []struct {
		what string
		t    reflect.Type
		m    obj
		path []string
	}{
		{"the viewport", reflect.TypeFor[PreviewViewport](), ready, []string{"viewport"}},
		{"the element", reflect.TypeFor[ElementReport](), sel(element()), []string{"element"}},
		{"the box", reflect.TypeFor[ElementBox](), sel(element()), []string{"element", "box"}},
		{"the state", reflect.TypeFor[ElementState](), sel(element()), []string{"element", "state"}},
		{"the hit test", reflect.TypeFor[ElementHit](), sel(element()), []string{"element", "hit"}},
		{"an ancestor", reflect.TypeFor[ElementAncestor](), sel(element()), []string{"element", "ancestors", "0"}},
		{"a hint", reflect.TypeFor[ElementHint](), sel(element()), []string{"element", "hints", "0"}},
	} {
		k := kinds(o.t)
		members(o.what, o.m, o.path, k, slices.Sorted(maps.Keys(k)))
	}
	// The items of each list, of another kind.
	add("an attribute of another kind", n, sel(with(element(), []any{"id"}, "attributes")), false)
	add("an attribute's name of another kind", n, sel(with(element(), []any{[]any{1, "save"}}, "attributes")), false)
	add("a style's value of another kind", n, sel(with(element(), []any{[]any{"display", 1}}, "styles")), false)
	add("an ancestor of another kind", n, sel(with(element(), []any{"div"}, "ancestors")), false)
	add("a hint of another kind", n, sel(with(element(), []any{"vue"}, "hints")), false)
	add("a cut step of another kind", n, sel(with(element(), []any{0}, "cut")), false)
	add("a ready's hint kind of another kind", n, with(ready, []any{0}, "hints"), false)
	// The strings of each list, each with a lone surrogate.
	for _, l := range []string{"attributes", "styles"} {
		lone("an item of "+l+"'s name", sel(with(element(), loneMark, l, "0", "0")))
		lone("an item of "+l+"'s value", sel(with(element(), loneMark, l, "0", "1")))
	}
	lone("a cut step", sel(with(element(), []any{loneMark}, "cut")))
	lone("a ready's hint kind", with(ready, []any{loneMark}, "hints"))
	// A second value for the members every other accepted row gives one:
	// the other colour scheme, an ancestor of another tag with an id and a
	// role, a nonce of every kind of character its alphabet has (and one of
	// each character next to it, refused), a revision of every hex digit.
	add("a ready in the light colour scheme", n, with(ready, "light", "viewport", "colorScheme"), true)
	add("an ancestor of another tag with an id and a role", n, sel(with(element(), []any{obj{"tag": "section", "id": "bar", "classes": "bar wide", "role": "toolbar"}}, "ancestors")), true)
	add("hello with a nonce of every kind of character", "", msg(PreviewHello, "AZaz09_-AZaz09_-"), true)
	// The characters next to each end of the alphabet's ranges, written
	// here from its definition (A-Z a-z 0-9 _ -), and a few further off.
	nonceRanges := [][2]rune{{'A', 'Z'}, {'a', 'z'}, {'0', '9'}, {'_', '_'}, {'-', '-'}}
	inNonce := func(c rune) bool {
		return slices.ContainsFunc(nonceRanges, func(r [2]rune) bool { return r[0] <= c && c <= r[1] })
	}
	neighbours := []string{"+", "=", "~"}
	for _, r := range nonceRanges {
		for _, c := range []rune{r[0] - 1, r[1] + 1} {
			if !inNonce(c) && !slices.Contains(neighbours, string(c)) {
				neighbours = append(neighbours, string(c))
			}
		}
	}
	for _, c := range neighbours {
		add("hello with a nonce holding "+c, "", msg(PreviewHello, "AZaz09_-AZaz09_-"+c), false)
	}
	// The lower edge of every member, from its definition as the bounds
	// above are its upper one: each list empty and of one item (each one's
	// bound is above), the strings of each list's items empty (beside the
	// objects' strings, made empty with the schema above), a cut of exactly
	// one step, and the smallest page and element, every list empty and
	// every string that may be empty empty.
	add("ready with no hint kind", n, with(ready, []any{}, "hints"), true)
	add("ready with one hint kind", n, with(ready, []any{"react"}, "hints"), true)
	add("a ready's hint kind empty", n, with(ready, []any{""}, "hints"), false)
	for _, l := range []struct {
		member string
		one    any
	}{
		{"attributes", []any{"id", "save"}},
		{"styles", []any{"display", "block"}},
		{"ancestors", ancestorBase},
		{"hints", hintBase},
	} {
		add("an element with no "+l.member, n, sel(with(element(), []any{}, l.member)), true)
		add("an element with one of its "+l.member, n, sel(with(element(), []any{l.one}, l.member)), true)
	}
	for _, l := range []string{"attributes", "styles"} {
		add("an item of "+l+" with an empty name", n, sel(with(element(), []any{[]any{"", "v"}}, l)), true)
		add("an item of "+l+" with an empty value", n, sel(with(element(), []any{[]any{"n", ""}}, l)), true)
		add("an item of "+l+" with both empty", n, sel(with(element(), []any{[]any{"", ""}}, l)), true)
	}
	add("a cut of no step", n, sel(with(element(), []any{}, "cut")), true)
	add("a cut of its first step", n, sel(with(element(), []any{ElementCuts[0]}, "cut")), true)
	add("a cut step empty", n, sel(with(element(), []any{""}, "cut")), false)
	smallestPage := obj{"url": "", "title": "", "epoch": 0, "revision": "",
		"viewport": obj{"width": 0, "height": 0, "dpr": math.SmallestNonzeroFloat64, "scrollX": 0, "scrollY": 0, "colorScheme": "light", "userAgent": ""}}
	smallestElement := obj{"tag": "", "id": "", "classes": "", "role": "", "name": "", "text": "", "selector": "",
		"box":        obj{"x": 0, "y": 0, "width": 0, "height": 0},
		"attributes": []any{},
		"state":      obj{"disabled": false, "hidden": false, "ariaDisabled": "", "ariaExpanded": "", "pointerEvents": "", "inert": false, "hovered": false, "motion": 0},
		"hit":        obj{"self": false, "selector": "", "tag": ""},
		"html":       "", "neighbourhood": "", "ancestors": []any{}, "styles": []any{}, "hints": []any{}, "cut": []any{},
	}
	add("the smallest ready", n, msg(PreviewReady, n, smallestPage, obj{"version": PreviewVersion, "hints": []any{}}), true)
	add("the smallest selected", n, msg(PreviewSelected, n, smallestPage, obj{"element": smallestElement}), true)
	add("the smallest described", n, msg(PreviewDescribed, n, smallestPage, obj{"element": smallestElement}), true)
	add("the smallest looked", n, msg(PreviewLooked, n, smallestPage), true)
	add("the smallest page's element with an ancestor and a hint of empty strings", n, msg(PreviewSelected, n, smallestPage, obj{"element": with(with(smallestElement,
		[]any{obj{"tag": "", "id": "", "classes": "", "role": ""}}, "ancestors"), []any{obj{"how": "attribute", "file": "", "line": 0, "column": 0, "component": ""}}, "hints")}), true)
	add("describe with a selector of one character", n, msg(PreviewDescribe, n, obj{"selector": "a"}), true)
	add("a revision of every hex digit", n, with(ready, "0123456789abcdef+fedcba9876543210", "revision"), true)
	add("a revision of a commit in digits", n, with(ready, "0123456789", "revision"), true)
	// A string at its bound in a control character with no short escape,
	// and in characters outside the basic plane from each end of their
	// range, each one past refused.
	for _, b := range previewBounds() {
		if b.member != "the describe's selector" && b.member != "the element's selector" {
			continue
		}
		for _, w := range []width{{"vertical-tab", "\v", 6}, {"U+10000", string(rune(0x10000)), 4}, {"U+10FFFF", string(rune(0x10ffff)), 4}} {
			add(b.member+" at its bound in "+w.name+" characters", n, b.build(b.n, w.fill), true)
			add(b.member+" one "+w.name+" character past", n, b.build(b.n+w.n, w.fill), false)
		}
	}
	// A number is the double its text rounds to, and every range is judged
	// on that double: these are what both parsers give such texts.
	spelled("an epoch whose text is negative and rounds to zero", n, ready, `"epoch":3`, `"epoch":-1e-400`, true)
	spelled("an epoch whose text is past zero and rounds to zero", n, ready, `"epoch":3`, `"epoch":1e-400`, true)
	spelled("an epoch whose text has a fraction that rounds away", n, ready, `"epoch":3`, `"epoch":9007199254740991.4`, true)
	spelled("an epoch whose text is below 3 and rounds to 3", n, ready, `"epoch":3`, `"epoch":2.99999999999999999999`, true)
	spelled("a version whose text is past 1 and rounds to 1", n, ready, `"version":1`, `"version":1.0000000000000001`, true)
	spelled("a version whose text is below 1 and rounds to 1", n, ready, `"version":1`, `"version":0.99999999999999999`, true)
	spelled("a device pixel ratio whose text is above zero and rounds to zero", n, ready, `"dpr":2`, `"dpr":2e-324`, false)
	spelled("a device pixel ratio whose text rounds to the smallest double", n, ready, `"dpr":2`, `"dpr":2.4703282292062328e-324`, true)
	// The flag missing is true, not merely truthy.
	for _, v := range []any{1, "true", []any{}, obj{}} {
		add("described as missing "+marshal(v), n, msg(PreviewDescribed, n, page(), obj{"missing": v}), false)
	}
	// The nonce and the revision's parts at their bounds and one off.
	for _, k := range []int{15, 16, 64, 65} {
		add("hello with a nonce of "+strconv.Itoa(k), "", msg(PreviewHello, strings.Repeat("n", k)), k >= 16 && k <= 64)
	}
	hex := func(k int) string { return strings.Repeat("a", k) }
	for _, r := range []struct {
		name, rev string
		ok        bool
	}{
		{"a commit of 3 digits", hex(3), false}, {"a commit of 4 digits", hex(4), true},
		{"a commit of 64 digits", hex(64), true}, {"a commit of 65 digits", hex(65), false},
		{"content of 3 digits", hex(4) + "+" + hex(3), false}, {"content of 4 digits", hex(4) + "+" + hex(4), true},
		{"content of 64 digits", hex(63) + "+" + hex(64), true}, {"content of 65 digits", hex(4) + "+" + hex(65), false},
	} {
		add("a revision of "+r.name, n, with(ready, r.rev, "revision"), r.ok)
	}
	// Every numeric rule at its edges: a whole number from 0 to 2^53-1 (an
	// epoch, a motion, a hint's line and column), a size at least 0 (the
	// viewport's and the box's), a device pixel ratio above 0, a scroll and
	// a box's place any number; every number finite (1e400 is past a
	// double: Go refuses the text, JSON.parse makes it Infinity).
	for _, e := range []struct {
		what   string
		m      obj
		path   []string
		ok, no []any
	}{
		{"an epoch", ready, []string{"epoch"}, []any{0, maxPreviewInt}, []any{-1, 0.5, maxPreviewInt + 1}},
		{"a motion", sel(element()), []string{"element", "state", "motion"}, []any{0, maxPreviewInt}, []any{-1, 0.5, maxPreviewInt + 1}},
		{"a hint's line", sel(element()), []string{"element", "hints", "0", "line"}, []any{0, maxPreviewInt}, []any{-1, 0.5, maxPreviewInt + 1}},
		{"a hint's column", sel(element()), []string{"element", "hints", "0", "column"}, []any{0, maxPreviewInt}, []any{-1, 0.5, maxPreviewInt + 1}},
		{"a viewport's width", ready, []string{"viewport", "width"}, []any{0, 0.5, math.MaxFloat64}, []any{-1, -0.5}},
		{"a viewport's height", ready, []string{"viewport", "height"}, []any{0, 0.5, math.MaxFloat64}, []any{-1, -0.5}},
		{"a box's width", sel(element()), []string{"element", "box", "width"}, []any{0, 0.5, math.MaxFloat64}, []any{-1, -0.5}},
		{"a box's height", sel(element()), []string{"element", "box", "height"}, []any{0, 0.5, math.MaxFloat64}, []any{-1, -0.5}},
		{"a device pixel ratio", ready, []string{"viewport", "dpr"}, []any{math.SmallestNonzeroFloat64, 0.5, math.MaxFloat64}, []any{0, -1}},
		{"a scroll", ready, []string{"viewport", "scrollX"}, []any{-math.MaxFloat64, math.MaxFloat64}, nil},
		{"a vertical scroll", ready, []string{"viewport", "scrollY"}, []any{-math.MaxFloat64, math.MaxFloat64}, nil},
		{"a box's x", sel(element()), []string{"element", "box", "x"}, []any{-math.MaxFloat64, math.MaxFloat64}, nil},
		{"a box's y", sel(element()), []string{"element", "box", "y"}, []any{-math.MaxFloat64, math.MaxFloat64}, nil},
	} {
		for _, v := range e.ok {
			add(e.what+" of "+marshal(v), n, with(e.m, v, e.path...), true)
		}
		for _, v := range e.no {
			add(e.what+" of "+marshal(v), n, with(e.m, v, e.path...), false)
		}
		const marker = 1234567.25
		spelled(e.what+" of 1e400", n, with(e.m, marker, e.path...), "1234567.25", "1e400", false)
	}
	// The largest message to the workbench, every member at its bound, as
	// the browser's writer writes it: past a text bound of 34 KiB.
	largest := sel(sized(element(), MaxElementReport, pad))
	largest = with(with(with(largest, pad(MaxPreviewURL), "url"), strings.Repeat("😀", MaxPreviewTitle), "title"), pad(MaxPreviewUserAgent), "viewport", "userAgent")
	if len(writeText(largest)) <= 34<<10 {
		panic("the largest message is not past 34 KiB")
	}
	add("the largest message to the workbench", n, largest, true)
	spelled("a selector's key in another case", n, describe, `"selector"`, `"Selector"`, false)
	spelled("a selector's key with a letter that folds to its own", n, describe, `"selector"`, `"ſelector"`, false)
	spelled("a nonce's key in capitals", n, describe, `"nonce"`, `"NONCE"`, false)
	spelled("a selector's key written with an escape, the same key", n, describe, `"selector"`, `"`+`\`+`u0073elector"`, true)
	add("a selector of null", n, nulled(describe, "selector"), false)
	add("a nonce of null", n, nulled(describe, "nonce"), false)
	add("a page without its revision", n, with(ready, nil, "revision"), false)
	add("a revision of null", n, nulled(ready, "revision"), false)
	add("a title of null", n, nulled(ready, "title"), false)
	add("hints of null", n, nulled(ready, "hints"), false)
	add("a viewport of null", n, nulled(ready, "viewport"), false)
	add("a viewport's width of null", n, nulled(ready, "viewport", "width"), false)
	add("an element of null", n, nulled(sel(element()), "element"), false)
	add("described as missing null", n, nulled(msg(PreviewDescribed, n, page(), obj{"missing": true}), "missing"), false)
	add("attributes of null", n, sel(nulled(element(), "attributes")), false)
	add("an attribute of null", n, sel(with(element(), []any{nil}, "attributes")), false)
	add("an attribute's value of null", n, sel(with(element(), []any{[]any{"id", nil}}, "attributes")), false)
	add("a box's x of null", n, sel(nulled(element(), "box", "x")), false)
	add("a cut of null", n, sel(nulled(element(), "cut")), false)
	add("a cut step of null", n, sel(with(element(), []any{nil}, "cut")), false)
	add("a hint of null", n, sel(with(element(), []any{nil}, "hints")), false)
	add("an ancestor of null", n, sel(with(element(), []any{nil}, "ancestors")), false)

	units := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
	var ms []measureCase
	addM := func(name string, u []uint16) {
		ms = append(ms, measureCase{name, u, measured(string(utf16.Decode(u)))})
	}
	addM("<, > and & as they are", units("<a href=\"x&y\">"))
	addM("U+2028 and U+2029 in six bytes each", units("a b "))
	addM("a lone surrogate as U+FFFD", []uint16{'a', 0xD800, 'b'})
	addM("a lone low surrogate as U+FFFD", []uint16{0xDC00})
	addM("a lone high surrogate at the end as U+FFFD", []uint16{'a', 0xDBFF})
	addM("a pair as its four bytes", units("😀"))
	addM("control characters", units("\x01\b\f\n\r\t\x7f"))
	addM("NUL and U+001F", units("\x00\x1f"))
	addM("a quote and a backslash", units(`"\`))
	addM("two-byte and three-byte characters", units("é€߿ࠀ￿"))
	// The measure from its definition: each code unit at the edge of a
	// class (every control character, DEL, the first and the last two-byte
	// one, the first three-byte one, the separators and their neighbours,
	// the plane's last) alone, and the pairs at the corners of the halves.
	for c := uint16(0); c < 0x20; c++ {
		addM(fmt.Sprintf("U+%04X alone", c), []uint16{c})
	}
	for _, c := range []uint16{0x7f, 0x80, 0x7ff, 0x800, 0x2027, 0x2028, 0x2029, 0x202a, 0xffff, 0xdfff} {
		addM(fmt.Sprintf("U+%04X alone", c), []uint16{c})
	}
	for _, p := range [][]uint16{{0xd800, 0xdc00}, {0xd800, 0xdfff}, {0xdbff, 0xdc00}, {0xdbff, 0xdfff}} {
		addM(fmt.Sprintf("the pair U+%04X U+%04X", p[0], p[1]), p)
	}
	bounds := map[string]int{}
	for _, b := range previewBounds() {
		bounds[b.bound] = b.n
	}
	return previewFile{
		Comment:    "Written by internal/proto's TestPreviewMessagesFixture (CONDUCTOR_WRITE_FIXTURES=1) and judged by it and by web/app/utils/protocol.test.ts: the bounds both parsers hold, by their names in preview.go, and the text's the server alone holds; each message's verdict with the parser told its way and holding nonce, its text as both read it (asText: a text no value is, the server's alone), an accepted one's content the content of its text; and each string's measure as Go writes it.",
		Bounds:     bounds,
		TextBounds: map[string]int{"MaxPreviewTextToBridge": 8192, "MaxPreviewTextToWorkbench": 131072},
		Cuts:       ElementCuts,
		HintKinds:  PreviewHintKinds,
		Measures:   ms,
		Messages:   cs,
	}
}

// previewContent is p as a message's content: the members its type
// carries, as JSON decodes them.
func previewContent(p PreviewMessage) any {
	all := obj{"selector": p.Selector, "version": p.Version, "url": p.URL, "title": p.Title, "viewport": p.Viewport, "epoch": p.Epoch,
		"revision": p.Revision, "hints": p.Hints, "element": p.Element, "missing": p.Missing}
	m := obj{"conductor": p.Conductor, "t": p.T, "nonce": p.Nonce}
	keys := previewFields[p.T]
	if p.T == PreviewDescribed {
		keys = slices.Concat(keys, []string{map[bool]string{true: "element", false: "missing"}[p.Element != nil]})
	}
	for _, k := range keys {
		m[k] = all[k]
	}
	var v any
	if err := json.Unmarshal(writeText(m), &v); err != nil {
		panic(err)
	}
	return v
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// encodeFixture writes f with one line for each measure and each message,
// a message's text as it was spelled.
func encodeFixture(f previewFile) []byte {
	line := func(v any) []byte {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
		return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for _, field := range []struct {
		name string
		v    any
	}{{"comment", f.Comment}, {"bounds", f.Bounds}, {"textBounds", f.TextBounds}, {"cuts", f.Cuts}, {"hintKinds", f.HintKinds}} {
		b.WriteString(` "` + field.name + `": `)
		b.Write(line(field.v))
		b.WriteString(",\n")
	}
	list := func(name string, n int, item func(int) any, last bool) {
		b.WriteString(` "` + name + "\": [\n")
		for i := range n {
			b.WriteString("  ")
			b.Write(line(item(i)))
			if i < n-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(" ]")
		if !last {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	list("measures", len(f.Measures), func(i int) any { return f.Measures[i] }, false)
	list("messages", len(f.Messages), func(i int) any { return f.Messages[i] }, true)
	b.WriteString("}\n")
	return b.Bytes()
}

func TestPreviewMessagesFixture(t *testing.T) {
	want := encodeFixture(previewCases())
	if os.Getenv("CONDUCTOR_WRITE_FIXTURES") == "1" {
		if err := os.WriteFile(previewFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(previewFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not what the cases build: write it again with CONDUCTOR_WRITE_FIXTURES=1", previewFixture)
	}
	var f previewFile
	if err := json.Unmarshal(got, &f); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range f.Messages {
		if names[c.Name+c.To] {
			t.Errorf("two cases named %q", c.Name)
		}
		names[c.Name+c.To] = true
		to, ok := previewWays[c.To]
		if !ok {
			t.Fatalf("%s: no way %q", c.Name, c.To)
		}
		p, err := ParsePreviewMessage(c.Message, to, c.Nonce)
		if (err == nil) != c.OK {
			t.Errorf("%s: ok %v, want %v (%v)", c.Name, err == nil, c.OK, err)
		}
		if err != nil {
			continue
		}
		// What was parsed is the content of the text, field by field.
		var want any
		if err := json.Unmarshal(c.Message, &want); err != nil {
			t.Fatal(err)
		}
		if got := previewContent(p); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: parsed as %s", c.Name, writeText(got))
		}
	}
	// The text's bounds are the numbers the fixture writes, not read from
	// preview.go.
	if got := map[string]int{"MaxPreviewTextToBridge": MaxPreviewTextToBridge, "MaxPreviewTextToWorkbench": MaxPreviewTextToWorkbench}; !maps.Equal(got, f.TextBounds) {
		t.Errorf("the text's bounds %v, the fixture's %v", got, f.TextBounds)
	}
	// The measure: the fixture's strings decoded as Go's JSON decoder reads
	// them (a lone surrogate is U+FFFD), measured as a message holds them.
	for _, m := range f.Measures {
		var s string
		raw, _ := json.Marshal(string(utf16.Decode(m.Units)))
		_ = json.Unmarshal(raw, &s)
		if got := measured(s); got != m.Bytes {
			t.Errorf("%s: %d bytes, the fixture says %d", m.Name, got, m.Bytes)
		}
	}
	if len(f.Messages) < 200 || len(f.Measures) < 10 {
		t.Fatalf("the fixture has %d messages and %d measures", len(f.Messages), len(f.Measures))
	}
}

// Every bound preview.go names (a Max constant) is in the table the
// fixture's cases are made from, with the number it has there, or is one
// of the text's bounds only Go holds; so no bound is left without a case
// at it and one past it.
func TestEveryPreviewBoundIsInTheTable(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "preview.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var consts []string
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.GenDecl); ok && d.Tok == token.CONST {
			for _, s := range d.Specs {
				for _, id := range s.(*ast.ValueSpec).Names {
					if strings.HasPrefix(id.Name, "Max") {
						consts = append(consts, id.Name)
					}
				}
			}
		}
		return true
	})
	table := previewCases().Bounds
	for _, name := range consts {
		if _, ok := table[name]; !ok && !slices.Contains(previewTextBounds, name) {
			t.Errorf("%s is in no case of the fixture", name)
		}
	}
	if len(consts) != len(table)+len(previewTextBounds) {
		t.Fatalf("preview.go has %d bounds, the table %d and the text's %d", len(consts), len(table), len(previewTextBounds))
	}
}

// A message's text is bounded before anything decodes it, at twice its
// way's measured bound (the fixture's numbers, 8 KiB and 128 KiB, written
// there, not read from the constants), and refused when it is not UTF-8;
// within its bound, whitespace and escapes cost nothing a value pays, so a
// text that measures within its bound once decoded passes. The bound is
// the browser's writer's: a valid report json.Marshal writes, escaping
// each < in six bytes, is past it and refused, by design; written with
// HTML escaping off it passes.
func TestAPreviewMessagesTextIsBoundedBeforeItIsDecoded(t *testing.T) {
	selectText := writeText(msg(PreviewSelect, fixtureNonce))
	readyText := writeText(readyMessage())
	padded := func(text []byte, size int) []byte {
		return append(append([]byte{'{'}, bytes.Repeat([]byte(" "), size-len(text))...), text[1:]...)
	}
	angled := selectedOf(sized(with(element(), strings.Repeat("<", MaxElementHTML), "html"), MaxElementReport, func(k int) string { return strings.Repeat(">", k) }))
	marshalled, _ := json.Marshal(angled)
	for _, tc := range []struct {
		name string
		to   PreviewDirection
		text []byte
		ok   bool
	}{
		{"a select padded to 8,192 bytes", PreviewToBridge, padded(selectText, 8192), true},
		{"a select padded to 8,193 bytes", PreviewToBridge, padded(selectText, 8193), false},
		{"a select padded with a mebibyte", PreviewToBridge, padded(selectText, 1<<20), false},
		{"a ready padded to 131,072 bytes", PreviewToWorkbench, padded(readyText, 131072), true},
		{"a ready padded to 131,073 bytes", PreviewToWorkbench, padded(readyText, 131073), false},
		{"a report of angle brackets written with HTML escaping off", PreviewToWorkbench, writeText(angled), true},
		{"the same report as json.Marshal writes it", PreviewToWorkbench, marshalled, false},
		{"a selector of a thousand escapes, within the text's bound", PreviewToBridge,
			[]byte(`{"conductor":"preview/1","t":"describe","nonce":"` + fixtureNonce + `","selector":"` + strings.Repeat(`\`+`u0061`, MaxPreviewSelector) + `"}`), true},
		{"a selector holding a byte that is not UTF-8", PreviewToBridge,
			[]byte(`{"conductor":"preview/1","t":"describe","nonce":"` + fixtureNonce + `","selector":"#a` + "\xff" + `"}`), false},
		{"a title of cut three-byte sequences", PreviewToWorkbench, bytes.Replace(readyText, []byte(`"Settings"`), append(append([]byte(`"`), bytes.Repeat([]byte{0xe2, 0x82}, 100)...), '"'), 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePreviewMessage(tc.text, tc.to, fixtureNonce)
			if (err == nil) != tc.ok {
				t.Fatalf("ok %v, want %v (%v)", err == nil, tc.ok, err)
			}
		})
	}
	if _, err := ParsePreviewMessage(selectText, 0, fixtureNonce); err == nil {
		t.Fatal("a message of no way passed")
	}
}

// The walk of a message's text goes as deep as the schema's deepest value
// (the message, its element, a list of the element's, an item of it: four
// levels) and refuses the text at the first object or list past it, where
// it meets it, reading nothing further: the level is read off the
// decoder's own offset, the bytes it has taken.
func TestAPreviewMessagesTextIsRefusedPastTheSchemasDepth(t *testing.T) {
	deepest := writeText(selectedOf(element()))
	if _, err := ParsePreviewMessage(deepest, PreviewToWorkbench, fixtureNonce); err != nil {
		t.Fatalf("the deepest shape the schema allows: %v", err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"an attribute holding a list", `["id","save"]`, `[["id"],"save"]`},
		{"an attribute holding an object", `["id","save"]`, `[{"id":"save"},"save"]`},
		{"an ancestor's tag a list", `{"classes":"bar","id":"","role":"","tag":"div"}`, `{"classes":"bar","id":"","role":"","tag":[]}`},
		{"an ancestor's tag an object", `{"classes":"bar","id":"","role":"","tag":"div"}`, `{"classes":"bar","id":"","role":"","tag":{}}`},
	} {
		text := bytes.Replace(deepest, []byte(tc.old), []byte(tc.new), 1)
		if bytes.Equal(text, deepest) {
			t.Fatalf("%s: not in the message", tc.name)
		}
		if _, err := decodeOnce(text); err == nil {
			t.Errorf("%s: one level past the schema's passed the walk", tc.name)
		}
		if _, err := ParsePreviewMessage(text, PreviewToWorkbench, fixtureNonce); err == nil {
			t.Errorf("%s: passed", tc.name)
		}
	}
	// offset is how far the walk read into text before it answered.
	offset := func(text []byte) (int64, error) {
		dec := json.NewDecoder(bytes.NewReader(text))
		_, err := decodeValue(dec, 1)
		return dec.InputOffset(), err
	}
	for _, tc := range []struct {
		name  string
		text  []byte
		ok    bool
		taken int64
	}{
		{"four lists", []byte("[[[[]]]]"), true, 8},
		{"four objects", []byte(`{"a":{"a":{"a":{}}}}`), true, 20},
		{"five lists", []byte("[[[[[]]]]]"), false, 5},
		{"five objects", []byte(`{"a":{"a":{"a":{"a":{}}}}}`), false, 21},
		{"lists to the bridge's text bound", bytes.Repeat([]byte("["), MaxPreviewTextToBridge), false, 5},
		{"lists to the workbench's text bound", bytes.Repeat([]byte("["), MaxPreviewTextToWorkbench), false, 5},
		{"objects to the workbench's text bound", bytes.Repeat([]byte(`{"a":`), MaxPreviewTextToWorkbench/5), false, 21},
	} {
		taken, err := offset(tc.text)
		if (err == nil) != tc.ok || taken != tc.taken {
			t.Errorf("%s: ok %v after %d bytes, want %v after %d (%v)", tc.name, err == nil, taken, tc.ok, tc.taken, err)
		}
		if !tc.ok {
			for _, to := range []PreviewDirection{PreviewToBridge, PreviewToWorkbench} {
				if _, err := ParsePreviewMessage(tc.text, to, fixtureNonce); err == nil {
					t.Errorf("%s: passed", tc.name)
				}
			}
		}
	}
}

// A message's text is one JSON value: white space may follow it, and
// nothing else, neither a further value nor a byte.
func TestAPreviewMessagesTextIsOneValue(t *testing.T) {
	text := string(writeText(msg(PreviewSelect, fixtureNonce)))
	for _, tc := range []struct {
		after string
		ok    bool
	}{
		{"", true}, {" ", true}, {" \t\r\n", true},
		{"{}", false}, {" {}", false}, {"x", false}, {" x", false}, {"1", false}, {"null", false}, {`""`, false}, {"[]", false},
		{"]", false}, {"}", false}, {",", false}, {":", false}, {"/", false}, {"\x00", false}, {"\xef\xbb\xbf", false}, {" " + text, false},
	} {
		if _, err := ParsePreviewMessage([]byte(text+tc.after), PreviewToBridge, fixtureNonce); (err == nil) != tc.ok {
			t.Errorf("a select followed by %q: ok %v, want %v (%v)", tc.after, err == nil, tc.ok, err)
		}
	}
}

// A refusal names nothing the message chose: its words are fixed, so a
// caller may answer with them or log them.
func TestAPreviewRefusalNamesNothingOfTheMessage(t *testing.T) {
	const chosen = "<img src=x onerror=alert(1)>"
	for _, m := range []obj{
		msg(chosen, fixtureNonce),
		with(msg(PreviewSelect, fixtureNonce), chosen, "conductor"),
		msg(PreviewSelect, chosen),
		msg(PreviewDescribe, fixtureNonce, obj{"selector": chosen + pad(MaxPreviewSelector)}),
		with(readyMessage(), chosen, "revision"),
		msg(PreviewSelect, fixtureNonce, obj{chosen: chosen}),
	} {
		text := writeText(m)
		_, err := ParsePreviewMessage(text, PreviewToBridge, fixtureNonce)
		if err == nil || !errors.Is(err, errPreview) {
			t.Fatalf("%s: %v", text, err)
		}
		_, err2 := ParsePreviewMessage(text, PreviewToWorkbench, fixtureNonce)
		for _, e := range []error{err, err2} {
			if e != nil && (strings.Contains(e.Error(), "onerror") || strings.Contains(e.Error(), "img")) {
				t.Fatalf("a refusal quotes the message: %v", e)
			}
		}
	}
}

// The message is built from the value judged: every field the text gave,
// a number by its value whatever its spelling. A member given twice is
// refused, whichever comes first.
func TestAPreviewMessageIsBuiltFromTheValueJudged(t *testing.T) {
	text := writeText(selectedOf(element()))
	text = bytes.Replace(text, []byte(`"epoch":3`), []byte(`"epoch":3e0`), 1)
	text = bytes.Replace(text, []byte(`"line":0`), []byte(`"line":4.0`), 1)
	if _, err := ParsePreviewMessage(bytes.Replace(text, []byte(`"title":"Settings"`), []byte(`"title":"First","title":"Settings"`), 1), PreviewToWorkbench, fixtureNonce); err == nil {
		t.Fatal("a title given twice passed")
	}
	got, err := ParsePreviewMessage(text, PreviewToWorkbench, fixtureNonce)
	if err != nil {
		t.Fatal(err)
	}
	want := PreviewMessage{Conductor: PreviewMark, T: PreviewSelected, Nonce: fixtureNonce, URL: "http://127.0.0.1:5173/settings", Title: "Settings", Epoch: 3,
		Viewport: &PreviewViewport{Width: 1280, Height: 800, DPR: 2, ScrollY: 120.5, ColorScheme: "dark", UserAgent: "Mozilla/5.0"},
		Element: &ElementReport{Tag: "button", ID: "save", Classes: "btn save", Name: "Save", Text: "Save", Selector: "#save",
			Box: ElementBox{X: 10, Y: 20.25, Width: 80, Height: 32}, Attributes: [][2]string{{"id", "save"}, {"class", "btn save"}},
			State: ElementState{PointerEvents: "auto"}, Hit: ElementHit{Selector: "div.overlay", Tag: "div"},
			HTML: `<button id="save" class="btn save">Save</button>`, Neighbourhood: `<div class="bar"><button id="save" class="btn save">Save</button></div>`,
			Ancestors: []ElementAncestor{{Tag: "div", Classes: "bar"}}, Styles: [][2]string{{"display", "inline-block"}, {"pointer-events", "auto"}},
			Hints: []ElementHint{{How: "vue", File: "/src/app/components/SaveBar.vue", Line: 4, Component: "SaveBar"}}, Cut: []string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	readyText := writeText(with(readyMessage(), []any{"vue", "react"}, "hints"))
	r, err := ParsePreviewMessage(bytes.Replace(readyText, []byte(`"version":1`), []byte(`"version":1.0`), 1), PreviewToWorkbench, fixtureNonce)
	if err != nil || r.Version != PreviewVersion || !slices.Equal(r.Hints, []string{"vue", "react"}) {
		t.Fatalf("%+v %v", r, err)
	}
	for _, s := range []string{`"selector":"#a","selector":"#b"`, `"selector":"#b","selector":"#b"`, `"selector":1e400,"selector":"#b"`} {
		if _, err := ParsePreviewMessage([]byte(`{"conductor":"preview/1","t":"describe","nonce":"`+fixtureNonce+`",`+s+`}`), PreviewToBridge, fixtureNonce); err == nil {
			t.Fatalf("%s passed", s)
		}
	}
}

// One measure on both sides: a negative zero is one byte, as
// JSON.stringify writes it, in a value decoded from JSON and in a typed
// one alike.
func TestPreviewMeasureCountsANegativeZeroAsZero(t *testing.T) {
	negZero := math.Copysign(0, -1)
	if got := PreviewMeasure(negZero); got != 1 {
		t.Fatalf("-0: %d", got)
	}
	if got, want := PreviewMeasure(obj{"x": negZero, "y": []any{negZero, 0.0}}), PreviewMeasure(obj{"x": 0, "y": []any{0, 0}}); got != want {
		t.Fatalf("a value with negative zeros: %d, want %d", got, want)
	}
	if got, want := PreviewMeasure(ElementBox{X: negZero, Y: negZero}), PreviewMeasure(ElementBox{}); got != want {
		t.Fatalf("a typed value with negative zeros: %d, want %d", got, want)
	}
}

// Every member of a message, taken from its Go type, is accepted at its
// edges in some row of the fixture: each list empty, of one item and at its
// bound; each string empty but those the schema refuses empty; each number
// at the least and the greatest of its range; each flag both ways, or one
// way where the schema has it so. A member the types gain fails here until
// its edges are in previewCases, and one of a kind the test does not judge
// fails until the test judges it (the strings' upper bounds are
// previewBounds', held by TestEveryPreviewBoundIsInTheTable).
func TestEveryPreviewMemberIsAcceptedAtItsEdges(t *testing.T) {
	lists := map[string]int{"hints": len(PreviewHintKinds), "element.attributes": MaxElementAttributes, "element.ancestors": MaxAncestors,
		"element.styles": MaxStyles, "element.hints": MaxHints, "element.cut": len(ElementCuts)}
	whole, size, place := [2]float64{0, maxPreviewInt}, [2]float64{0, math.MaxFloat64}, [2]float64{-math.MaxFloat64, math.MaxFloat64}
	numbers := map[string][2]float64{"version": {PreviewVersion, PreviewVersion}, "epoch": whole, "element.state.motion": whole,
		"element.hints.*.line": whole, "element.hints.*.column": whole, "viewport.width": size, "viewport.height": size,
		"element.box.width": size, "element.box.height": size, "viewport.dpr": {math.SmallestNonzeroFloat64, math.MaxFloat64},
		"viewport.scrollX": place, "viewport.scrollY": place, "element.box.x": place, "element.box.y": place}
	nonEmpty := map[string]bool{"conductor": true, "t": true, "nonce": true, "selector": true, "viewport.colorScheme": true,
		"element.hints.*.how": true, "hints.*": true, "element.cut.*": true}
	// A flag the schema has one way only: a described's missing is true or absent.
	oneWay := map[string]bool{"missing": true}
	type seen struct {
		lens     map[int]bool
		empty    bool
		least    float64
		greatest float64
		numbers  bool
		flags    map[bool]bool
	}
	at := map[string]*seen{}
	get := func(p string) *seen {
		if at[p] == nil {
			at[p] = &seen{lens: map[int]bool{}, flags: map[bool]bool{}}
		}
		return at[p]
	}
	join := func(p, k string) string { return strings.TrimPrefix(p+"."+k, ".") }
	var walk func(v any, p string)
	walk = func(v any, p string) {
		switch x := v.(type) {
		case obj:
			for k, e := range x {
				walk(e, join(p, k))
			}
		case []any:
			get(p).lens[len(x)] = true
			for _, e := range x {
				walk(e, join(p, "*"))
			}
		case string:
			get(p).empty = get(p).empty || x == ""
		case bool:
			get(p).flags[x] = true
		case float64:
			s := get(p)
			if !s.numbers {
				s.least, s.greatest, s.numbers = x, x, true
			}
			s.least, s.greatest = min(s.least, x), max(s.greatest, x)
		}
	}
	for _, c := range previewCases().Messages {
		var v any
		if c.OK && json.Unmarshal(c.Message, &v) == nil {
			walk(v, "")
		}
	}
	var member func(typ reflect.Type, p string)
	member = func(typ reflect.Type, p string) {
		switch typ.Kind() {
		case reflect.Pointer:
			member(typ.Elem(), p)
		case reflect.Struct:
			for i := range typ.NumField() {
				name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
				member(typ.Field(i).Type, join(p, name))
			}
		case reflect.Slice:
			if s, bound := get(p), lists[p]; bound == 0 || !s.lens[0] || !s.lens[1] || !s.lens[bound] {
				t.Errorf("%s: accepted at the lengths %v, want 0, 1 and its bound %d", p, slices.Sorted(maps.Keys(s.lens)), bound)
			}
			member(typ.Elem(), join(p, "*"))
		case reflect.Array: // a pair, two strings
			member(typ.Elem(), join(p, "*"))
		case reflect.String:
			if !nonEmpty[p] && !get(p).empty {
				t.Errorf("%s: never accepted empty", p)
			}
		case reflect.Float64, reflect.Int, reflect.Int64:
			if r, ok := numbers[p]; !ok || !get(p).numbers || get(p).least != r[0] || get(p).greatest != r[1] {
				t.Errorf("%s: accepted from %v to %v, want its range %v", p, get(p).least, get(p).greatest, r)
			}
		case reflect.Bool:
			if f := get(p).flags; !f[true] || f[false] == oneWay[p] {
				t.Errorf("%s: accepted %v, want true and, but where the schema has it one way, false", p, f)
			}
		default:
			t.Errorf("%s: a member of a kind (%s) this test does not judge: give it a case and its rows", p, typ.Kind())
		}
	}
	member(reflect.TypeFor[PreviewMessage](), "")
}

// The bridge's own writing (web/app/utils/previewBridge): the two report
// fixtures report.test.ts writes (every field at its bound, and the
// largest-tag shape), each within the element report's schema and its bound
// by the shared measure and, in a selected, the deepest shape the bridge
// writes, within the walk's depth; and its answers (bridge.test.ts) on a
// plain page (no hint kind, no title, an element without ancestors), on a
// page larger than every bound of its reads, and on a page of fields,
// scripts, styles and a template, each taken by this parser as the bridge's
// parser took it. Each is written with HTML escaping off, as the browser's
// writer writes it.
func TestTheBridgesOwnMessagesParse(t *testing.T) {
	read := func(name string, into any) {
		raw, err := os.ReadFile(filepath.Join("..", "api", "testdata", name))
		if err == nil {
			err = json.Unmarshal(raw, into)
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"fullest-report.json", "largest-tag-report.json"} {
		var f struct {
			Report obj `json:"report"`
			Bytes  int `json:"bytes"`
		}
		read(name, &f)
		if got := PreviewMeasure(f.Report); got != f.Bytes || got > MaxElementReport {
			t.Errorf("%s: %d bytes by Go's measure, the bridge's %d, the bound %d", name, got, f.Bytes, MaxElementReport)
		}
		if !validElement(f.Report) {
			t.Errorf("%s is not an element report", name)
		}
		if _, err := ParsePreviewMessage(writeText(selectedOf(f.Report)), PreviewToWorkbench, fixtureNonce); err != nil {
			t.Errorf("%s in a selected: %v", name, err)
		}
	}
	answers := func(name string) []string {
		var page struct {
			Nonce    string `json:"nonce"`
			Messages []obj  `json:"messages"`
		}
		read(name, &page)
		var types []string
		for i, m := range page.Messages {
			p, err := ParsePreviewMessage(writeText(m), PreviewToWorkbench, page.Nonce)
			if err != nil {
				t.Fatalf("%s, answer %d (%v): %v", name, i, m["t"], err)
			}
			types = append(types, p.T)
		}
		return types
	}
	if types := answers("plain-page-messages.json"); !slices.Equal(types, []string{PreviewReady, PreviewDescribed, PreviewSelected}) {
		t.Fatalf("the plain page's answers: %v", types)
	}
	// A page larger than every bound of the bridge's reads, and a page of fields, scripts, styles and a template.
	if types := answers("large-page-messages.json"); len(types) != 6 {
		t.Fatalf("the large page's answers: %v", types)
	}
	if types := answers("fields-page-messages.json"); len(types) != 24 {
		t.Fatalf("the fields page's answers: %v", types)
	}
}
