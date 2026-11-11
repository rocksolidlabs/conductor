package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math"
	"os"
	"reflect"
	"slices"
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
	// written here (a number as 3.0, a member twice).
	Message json.RawMessage `json:"message"`
	OK      bool            `json:"ok"`
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
	Comment   string         `json:"comment"`
	Bounds    map[string]int `json:"bounds"`
	Cuts      []string       `json:"cuts"`
	HintKinds []string       `json:"hintKinds"`
	Measures  []measureCase  `json:"measures"`
	Messages  []previewCase  `json:"messages"`
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

// with is m with path (keys, the last one set) set to v, or removed for a nil v.
func with(m obj, v any, path ...string) obj {
	out := deepCopy(m).(obj)
	at := out
	for _, k := range path[:len(path)-1] {
		at = at[k].(obj)
	}
	if v == nil {
		delete(at, path[len(path)-1])
	} else {
		at[path[len(path)-1]] = v
	}
	return out
}

// nulled is m with path's member there and null.
func nulled(m obj, path ...string) obj {
	out := deepCopy(m).(obj)
	at := out
	for _, k := range path[:len(path)-1] {
		at = at[k].(obj)
	}
	at[path[len(path)-1]] = nil
	return out
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

// wide is a string of n measured bytes in three-byte characters, but its
// last n%3 bytes.
func wide(n int) string { return strings.Repeat("€", n/3) + pad(n%3) }

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
	marshal := func(m any) string {
		raw, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		return string(raw)
	}
	addText := func(name, to, nonce, text string, ok bool) {
		cs = append(cs, previewCase{name, to, nonce, json.RawMessage(text), ok})
	}
	addTo := func(name, to, nonce string, m any, ok bool) { addText(name, to, nonce, marshal(m), ok) }
	add := func(name, nonce string, m any, ok bool) { addTo(name, way(m), nonce, m, ok) }
	// spelled is m's text with old, there once, written as new.
	spelled := func(name, nonce string, m any, old, new string, ok bool) {
		text := marshal(m)
		if strings.Count(text, old) != 1 {
			panic(name + ": " + old + " is not in the message once")
		}
		addText(name, way(m), nonce, strings.Replace(text, old, new, 1), ok)
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
	add("a device pixel ratio of 0", n, with(ready, 0, "viewport", "dpr"), false)
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
			add(b.member+" at its bound", n, b.build(b.n, pad), true)
			add(b.member+" one byte past", n, b.build(b.n+1, pad), false)
			add(b.member+" at its bound in three-byte characters", n, b.build(b.n, wide), true)
			add(b.member+" one three-byte character past", n, b.build(b.n+3, wide), false)
		case inChars:
			add(b.member+" at its bound in characters", n, b.build(b.n, func(k int) string { return strings.Repeat("é", k) }), true)
			add(b.member+" one character past", n, b.build(b.n+1, func(k int) string { return strings.Repeat("é", k) }), false)
			add(b.member+" at its bound in characters of two code units", n, b.build(b.n, func(k int) string { return strings.Repeat("😀", k) }), true)
			add(b.member+" one character of two code units past", n, b.build(b.n+1, func(k int) string { return strings.Repeat("😀", k) }), false)
		case inItems, inHex:
			add(b.member+" at their bound", n, b.build(b.n, nil), true)
			add(b.member+" one past", n, b.build(b.n+1, nil), false)
		case pastOnly:
			add(b.member+" one byte past its bound", n, b.build(b.n+1, pad), false)
		}
	}
	// A number is its value, never its spelling; a negative zero measures
	// as 0 in both; a member given twice is its last; a key is exactly its
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
	spelled("a selector given twice, the last one empty", n, describe, `"selector":"#x"`, `"selector":"#x","selector":""`, false)
	spelled("a selector given twice, the first one empty", n, describe, `"selector":"#x"`, `"selector":"","selector":"#x"`, true)
	spelled("a nonce given twice, the wrong one first", n, describe, `"nonce":"`+n+`"`, `"nonce":"`+otherNonce+`","nonce":"`+n+`"`, true)
	spelled("a nonce given twice, the right one first", n, describe, `"nonce":"`+n+`"`, `"nonce":"`+n+`","nonce":"`+otherNonce+`"`, false)
	spelled("a nonce given twice, a number first", n, describe, `"nonce":"`+n+`"`, `"nonce":1,"nonce":"`+n+`"`, true)
	spelled("a type given twice, the last one the message's", n, describe, `"t":"describe"`, `"t":"hello","t":"describe"`, true)
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
	bounds := map[string]int{}
	for _, b := range previewBounds() {
		bounds[b.bound] = b.n
	}
	return previewFile{
		Comment:   "Written by internal/proto's TestPreviewMessagesFixture (CONDUCTOR_WRITE_FIXTURES=1) and judged by it and by web/app/utils/protocol.test.ts: the bounds both parsers hold, by their names in preview.go; each message's verdict with the parser told its way and holding nonce, its text as both read it; and each string's measure as Go writes it.",
		Bounds:    bounds,
		Cuts:      ElementCuts,
		HintKinds: PreviewHintKinds,
		Measures:  ms,
		Messages:  cs,
	}
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
	}{{"comment", f.Comment}, {"bounds", f.Bounds}, {"cuts", f.Cuts}, {"hintKinds", f.HintKinds}} {
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
		_, err := ParsePreviewMessage(c.Message, to, c.Nonce)
		if (err == nil) != c.OK {
			t.Errorf("%s: ok %v, want %v (%v)", c.Name, err == nil, c.OK, err)
		}
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
// way's measured bound, and refused when it is not UTF-8 (no honest
// sender writes either); within its bound, whitespace and escapes cost
// nothing a value pays, so a text that measures within its bound once
// decoded passes.
func TestAPreviewMessagesTextIsBoundedBeforeItIsDecoded(t *testing.T) {
	selectText, _ := json.Marshal(msg(PreviewSelect, fixtureNonce))
	readyText, _ := json.Marshal(readyMessage())
	padded := func(text []byte, size int) []byte {
		return append(append([]byte{'{'}, bytes.Repeat([]byte(" "), size-len(text))...), text[1:]...)
	}
	for _, tc := range []struct {
		name string
		to   PreviewDirection
		text []byte
		ok   bool
	}{
		{"a select padded to its text's bound", PreviewToBridge, padded(selectText, MaxPreviewTextToBridge), true},
		{"a select padded one byte past", PreviewToBridge, padded(selectText, MaxPreviewTextToBridge+1), false},
		{"a select padded with a mebibyte", PreviewToBridge, padded(selectText, 1<<20), false},
		{"a ready padded to its text's bound", PreviewToWorkbench, padded(readyText, MaxPreviewTextToWorkbench), true},
		{"a ready padded one byte past", PreviewToWorkbench, padded(readyText, MaxPreviewTextToWorkbench+1), false},
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
		text, _ := json.Marshal(m)
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
// a number by its value whatever its spelling, a member given twice by its
// last.
func TestAPreviewMessageIsBuiltFromTheValueJudged(t *testing.T) {
	text, _ := json.Marshal(selectedOf(element()))
	text = bytes.Replace(text, []byte(`"epoch":3`), []byte(`"epoch":3e0`), 1)
	text = bytes.Replace(text, []byte(`"line":0`), []byte(`"line":4.0`), 1)
	text = bytes.Replace(text, []byte(`"title":"Settings"`), []byte(`"title":"First","title":"Settings"`), 1)
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
	readyText, _ := json.Marshal(with(readyMessage(), []any{"vue", "react"}, "hints"))
	r, err := ParsePreviewMessage(bytes.Replace(readyText, []byte(`"version":1`), []byte(`"version":1.0`), 1), PreviewToWorkbench, fixtureNonce)
	if err != nil || r.Version != PreviewVersion || !slices.Equal(r.Hints, []string{"vue", "react"}) {
		t.Fatalf("%+v %v", r, err)
	}
	d, err := ParsePreviewMessage([]byte(`{"conductor":"preview/1","t":"describe","nonce":"`+fixtureNonce+`","selector":"#a","selector":"#b"}`), PreviewToBridge, fixtureNonce)
	if err != nil || d.Selector != "#b" {
		t.Fatalf("%+v %v", d, err)
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
