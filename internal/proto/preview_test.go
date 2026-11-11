package proto

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
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

const previewFixture = "testdata/preview-messages.json"

// The nonce the cases' hello gave, and another.
const (
	fixtureNonce = "n0nce-of-the-hello-1234"
	otherNonce   = "another-nonce-0000000"
)

type previewCase struct {
	Name    string          `json:"name"`
	Nonce   string          `json:"nonce"` // the nonce the parser holds: the hello's, or "" before any
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

type previewFile struct {
	Comment  string        `json:"comment"`
	Measures []measureCase `json:"measures"`
	Messages []previewCase `json:"messages"`
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

// pairs is n name and value pairs of value v.
func pairs(n int, v string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = []any{"p", v}
	}
	return out
}

func repeat(n int, v any) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = deepCopy(v)
	}
	return out
}

// sized is e with its neighbourhood grown so the whole measures n bytes.
func sized(e obj, n int) obj {
	e = with(e, pad(MaxElementHTML), "html")
	e = with(e, "", "neighbourhood")
	return with(e, pad(n-PreviewMeasure(e)), "neighbourhood")
}

func previewCases() previewFile {
	n := fixtureNonce
	sel := func(e obj) obj { return msg(PreviewSelected, n, page(), obj{"element": e}) }
	ready := msg(PreviewReady, n, page(), obj{"version": PreviewVersion, "hints": []any{"vue"}})
	var cs []previewCase
	add := func(name, nonce string, m any, ok bool) {
		raw, err := json.Marshal(m)
		if err != nil {
			panic(err)
		}
		cs = append(cs, previewCase{name, nonce, raw, ok})
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
	add("describe with its selector at its bound", n, msg(PreviewDescribe, n, obj{"selector": pad(MaxPreviewSelector)}), true)
	add("describe with its selector one byte past", n, msg(PreviewDescribe, n, obj{"selector": pad(MaxPreviewSelector + 1)}), false)
	add("describe with six-byte characters past its bound", n, msg(PreviewDescribe, n, obj{"selector": strings.Repeat(" ", MaxPreviewSelector/6+1)}), false)
	add("describe with an empty selector", n, msg(PreviewDescribe, n, obj{"selector": ""}), false)
	add("describe without a selector", n, msg(PreviewDescribe, n), false)
	add("a message to the bridge past 4 KiB", n, msg(PreviewDescribe, n, obj{"selector": pad(MaxPreviewToBridge)}), false)
	// The answers and the page they carry.
	add("ready", n, ready, true)
	add("ready of another version", n, with(ready, 2, "version"), false)
	add("ready with a hint kind twice", n, with(ready, []any{"vue", "vue"}, "hints"), false)
	add("ready with an unknown hint kind", n, with(ready, []any{"angular"}, "hints"), false)
	add("ready without its hints", n, with(ready, nil, "hints"), false)
	add("ready with the wrong nonce", n, with(ready, otherNonce, "nonce"), false)
	add("the URL at its bound", n, with(ready, pad(MaxPreviewURL), "url"), true)
	add("the URL one byte past", n, with(ready, pad(MaxPreviewURL+1), "url"), false)
	add("the title at its bound in characters", n, with(ready, strings.Repeat("é", MaxPreviewTitle), "title"), true)
	add("the title one character past", n, with(ready, strings.Repeat("é", MaxPreviewTitle+1), "title"), false)
	add("a revision at its bound", n, with(ready, strings.Repeat("a", 64)+"+"+strings.Repeat("b", 63), "revision"), true)
	add("a revision one byte past", n, with(ready, strings.Repeat("a", 64)+"+"+strings.Repeat("b", 64), "revision"), false)
	add("a revision in capitals", n, with(ready, "A1B2C3D", "revision"), false)
	add("a revision that is not hex", n, with(ready, "main", "revision"), false)
	add("the user agent at its bound", n, with(ready, pad(MaxPreviewUserAgent), "viewport", "userAgent"), true)
	add("the user agent one byte past", n, with(ready, pad(MaxPreviewUserAgent+1), "viewport", "userAgent"), false)
	add("another colour scheme", n, with(ready, "sepia", "viewport", "colorScheme"), false)
	add("a viewport with a field of its own", n, with(ready, 1, "viewport", "zoom"), false)
	add("a negative scroll", n, with(ready, -4, "viewport", "scrollY"), true)
	add("a device pixel ratio of 0", n, with(ready, 0, "viewport", "dpr"), false)
	add("an epoch that is not whole", n, with(ready, 1.5, "epoch"), false)
	add("a negative epoch", n, with(ready, -1, "epoch"), false)
	add("the largest epoch a number holds", n, with(ready, maxPreviewInt, "epoch"), true)
	add("a page without its URL", n, with(ready, nil, "url"), false)
	add("looked", n, msg(PreviewLooked, n, page()), true)
	add("looked without its page", n, msg(PreviewLooked, n), false)
	add("cancelled", n, msg(PreviewCancelled, n), true)
	add("cancelled with a page", n, msg(PreviewCancelled, n, page()), false)
	add("a message to the workbench past 64 KiB", n, with(ready, pad(MaxPreviewToWorkbench), "url"), false)
	// The element report.
	add("selected", n, sel(element()), true)
	add("selected without its element", n, msg(PreviewSelected, n, page()), false)
	add("an element at its bound", n, sel(sized(element(), MaxElementReport)), true)
	add("an element one byte past", n, sel(sized(element(), MaxElementReport+1)), false)
	add("an element without its hit test", n, sel(with(element(), nil, "hit")), false)
	add("an element with a field of its own", n, sel(with(element(), "x", "value")), false)
	add("the name at its bound", n, sel(with(element(), pad(MaxElementName), "name")), true)
	add("the name one byte past", n, sel(with(element(), pad(MaxElementName+1), "name")), false)
	add("the text one byte past", n, sel(with(element(), pad(MaxElementText+1), "text")), false)
	add("the selector one byte past", n, sel(with(element(), pad(MaxElementSelector+1), "selector")), false)
	add("the HTML at its bound", n, sel(with(element(), pad(MaxElementHTML), "html")), true)
	add("the HTML one byte past", n, sel(with(element(), pad(MaxElementHTML+1), "html")), false)
	add("the neighbourhood at its bound", n, sel(with(element(), pad(MaxNeighbourhood), "neighbourhood")), true)
	add("the neighbourhood one byte past", n, sel(with(element(), pad(MaxNeighbourhood+1), "neighbourhood")), false)
	add("attributes at their count", n, sel(with(element(), pairs(MaxElementAttributes, "v"), "attributes")), true)
	add("one attribute past the count", n, sel(with(element(), pairs(MaxElementAttributes+1, "v"), "attributes")), false)
	add("an attribute value at its bound", n, sel(with(element(), pairs(1, pad(MaxElementAttributeValue)), "attributes")), true)
	add("an attribute value one byte past", n, sel(with(element(), pairs(1, pad(MaxElementAttributeValue+1)), "attributes")), false)
	add("an attribute of three parts", n, sel(with(element(), []any{[]any{"a", "b", "c"}}, "attributes")), false)
	add("styles at their count", n, sel(with(element(), pairs(MaxStyles, "v"), "styles")), true)
	add("one style past the count", n, sel(with(element(), pairs(MaxStyles+1, "v"), "styles")), false)
	add("a style value one byte past", n, sel(with(element(), pairs(1, pad(MaxStyleValue+1)), "styles")), false)
	ancestor := obj{"tag": "div", "id": "", "classes": "", "role": ""}
	add("ancestors at their count", n, sel(with(element(), repeat(MaxAncestors, ancestor), "ancestors")), true)
	add("one ancestor past the count", n, sel(with(element(), repeat(MaxAncestors+1, ancestor), "ancestors")), false)
	add("an ancestor past its bound", n, sel(with(element(), []any{with(ancestor, pad(MaxAncestor), "classes")}, "ancestors")), false)
	hint := obj{"how": "attribute", "file": "app/a.vue", "line": 4, "column": 2, "component": ""}
	add("hints at their count", n, sel(with(element(), repeat(MaxHints, hint), "hints")), true)
	add("one hint past the count", n, sel(with(element(), repeat(MaxHints+1, hint), "hints")), false)
	add("a hint file at its bound", n, sel(with(element(), []any{with(hint, pad(MaxHintFile), "file")}, "hints")), true)
	add("a hint file one byte past", n, sel(with(element(), []any{with(hint, pad(MaxHintFile+1), "file")}, "hints")), false)
	add("a hint of an unknown kind", n, sel(with(element(), []any{with(hint, "angular", "how")}, "hints")), false)
	add("a hint's line that is not whole", n, sel(with(element(), []any{with(hint, 1.5, "line")}, "hints")), false)
	add("the cut's steps", n, sel(with(element(), []any{"neighbourhood", "html", "style-count"}, "cut")), true)
	add("a cut step of its own", n, sel(with(element(), []any{"everything"}, "cut")), false)
	add("a state's motion that is not whole", n, sel(with(element(), 0.5, "state", "motion")), false)
	add("a box of negative width", n, sel(with(element(), -1, "box", "width")), false)
	add("described with its element", n, msg(PreviewDescribed, n, page(), obj{"element": element()}), true)
	add("described as missing", n, msg(PreviewDescribed, n, page(), obj{"missing": true}), true)
	add("described with neither", n, msg(PreviewDescribed, n, page()), false)
	add("described with both", n, msg(PreviewDescribed, n, page(), obj{"element": element(), "missing": true}), false)
	add("described as missing false", n, msg(PreviewDescribed, n, page(), obj{"missing": false}), false)

	units := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
	var ms []measureCase
	addM := func(name string, u []uint16) {
		ms = append(ms, measureCase{name, u, measured(string(utf16.Decode(u)))})
	}
	addM("<, > and & as they are", units("<a href=\"x&y\">"))
	addM("U+2028 and U+2029 in six bytes each", units("a b "))
	addM("a lone surrogate as U+FFFD", []uint16{'a', 0xD800, 'b'})
	addM("a lone low surrogate as U+FFFD", []uint16{0xDC00})
	addM("a pair as its four bytes", units("😀"))
	addM("control characters", units("\x01\b\f\n\r\t\x7f"))
	addM("a quote and a backslash", units(`"\`))
	addM("two-byte and three-byte characters", units("é€"))
	return previewFile{
		Comment:  "Written by internal/proto's TestPreviewMessagesFixture (CONDUCTOR_WRITE_FIXTURES=1) and judged by it and by web/app/utils/protocol.test.ts: each message's verdict with the parser holding nonce, and each string's measure as Go writes it.",
		Measures: ms,
		Messages: cs,
	}
}

func encodeFixture(f previewFile) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(f); err != nil {
		panic(err)
	}
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
	for _, c := range f.Messages {
		_, err := ParsePreviewMessage(c.Message, c.Nonce)
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
	if len(f.Messages) < 80 || len(f.Measures) < 8 {
		t.Fatalf("the fixture has %d messages and %d measures", len(f.Messages), len(f.Measures))
	}
}
