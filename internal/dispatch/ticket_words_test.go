package dispatch

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// A ticket made from a capture (Program item PV1, PR-1b, issue #825):
// nothing a capture holds but the person's notes is Markdown in it. Every
// string a capture holds is filled, by reflection over its types (a field
// added later among them), with words Markdown reads as structure, and the
// description and the comment are read for where each lands;
// testdata/capture-tickets.json keeps a sample of them, which
// web/app/utils/captureTicket.test.ts renders with the ticket page's own
// renderer.

var updateCaptureTickets = flag.Bool("update-capture-tickets", false, "rewrite testdata/capture-tickets.json from describe and sendComment")

// wordsCapture is a capture that reaches every line describe writes: an
// attempt's, the tree changed since the selection, the page serving
// another revision, no screenshot, an element with what a click reaches,
// disabled, with a source, and a region.
func wordsCapture() Capture {
	return Capture{CaptureDocument: CaptureDocument{Schema: CaptureSchemaName, ID: "0123456789abcdef", Rev: 3, URL: "http://127.0.0.1:5173/app",
		Title: "App", Viewport: Viewport{Mode: "desktop", Width: 1280, Height: 800},
		Origin: CaptureOrigin{Kind: OriginAttempt, Run: "crew-0123abcd", Task: "build", Attempt: 2, Tree: TreeWorktree, Commit: rep("a", 40), Changed: 1,
			ChangedPaths: []string{"web/app.vue"}, Content: rep("c", 40), Drift: &TreeRead{Commit: rep("b", 40), Content: rep("d", 40)},
			Serving: Serving{State: ServingStale, Page: rep("e", 40)}},
		Image: CaptureImage{How: ImageNone, Why: "the person declined the tab capture"},
		Annotations: []Annotation{
			{K: 1, Kind: AnnotationElement, Note: "Broken", Element: &ElementReport{Tag: "button", Name: "Save", Selector: "button.save",
				Hit: ElementHit{Selector: "div.overlay", Tag: "div"}, State: ElementState{Disabled: true}},
				Sources: []Source{{Path: "web/app/components/SaveBar.vue", How: "vue", Class: "tracked"}}},
			{K: 2, Kind: AnnotationRegion, Note: "here"},
		}}}
}

// fillStrings sets each string reachable from v through its exported
// fields, its slices (a first element made in an empty one), arrays and
// pointers (made when nil) to what pick gives for its path, where pick
// says so. A person's note (Annotation.Note: their Markdown) and the
// capture's id (the store's own, newID) are no capture's words to fill.
func fillStrings(v reflect.Value, path string, pick func(path string) (string, bool)) {
	switch v.Kind() {
	case reflect.String:
		if w, ok := pick(path); ok {
			v.SetString(w)
		}
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem(), path, pick)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		if v.Kind() == reflect.Slice && v.Len() == 0 {
			v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
		}
		for i := range v.Len() {
			fillStrings(v.Index(i), fmt.Sprintf("%s[%d]", path, i), pick)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			p := path + "." + f.Name
			if !f.IsExported() || (f.Name == "Note" && v.Type() == reflect.TypeFor[Annotation]()) || p == ".CaptureDocument.ID" {
				continue
			}
			fillStrings(v.Field(i), p, pick)
		}
	}
}

// wordsFilled is wordsCapture with each string pick names set.
func wordsFilled(pick func(path string) (string, bool)) Capture {
	c := wordsCapture()
	fillStrings(reflect.ValueOf(&c).Elem(), "", pick)
	return c
}

// wordPaths is every path fillStrings fills, in its order.
func wordPaths() []string {
	var paths []string
	wordsFilled(func(p string) (string, bool) { paths = append(paths, p); return "", false })
	return paths
}

// gateWords are the strings that decide which lines describe writes; the
// walk fills them, and keeps them as wordsCapture has them where it needs
// every line written.
var gateWords = map[string]bool{".CaptureDocument.Origin.Kind": true, ".CaptureDocument.Origin.Serving.State": true, ".CaptureDocument.Image.How": true}

// hostileWords is what a page could give that Markdown reads as structure,
// each word that holds a letter marked QZ: runs of one to six backticks
// and tildes alone and at either end; the empty word and white space in
// every spelling; line breaks in every spelling, some before a fence;
// fences; a link, an image (an attachment's among them), a mention, issue
// references, a heading, list markers, a table, raw HTML, an autolink, a
// bare address, emphasis; and an address with a user, a password, a query
// and a fragment, whose SECRET words reach no ticket.
func hostileWords() []string {
	ws := []string{"", " ", "\t", "\n", "\r", "\r\n", "\v", "\f", "\u0085", "\u00a0", "\u2028", "\u2029", "\u3000", " \t\n ",
		"QZa\nQZb", "QZa\r\nQZb", "QZa\rQZb", "QZa\u2028QZb", "QZa\u0085QZb", "QZa\n```\nQZb", "QZa\n~~~\nQZb", "QZa\n   ```\nQZb", "QZa\r```\rQZb",
		"```", "~~~", "```QZfence", "~~~~ QZfence", "   ```", "``` ```", "\n```\n", "`` ` ``",
		"[QZlink](https://evil.example/login)", "![QZimage](https://evil.example/p.png)", "![QZatt](attachment:0123456789abcdef)",
		"[QZref]: https://evil.example/ref", "@QZmention", "#123 QZissue owner/repo#5", "# QZheading", "- QZlist", "1. QZlist", "> QZquote",
		"| QZa | QZb |\n|---|---|", "<b>QZhtml</b>", "<img src=x onerror=alert(1)> QZhtml", "<https://evil.example/QZauto>",
		"https://evil.example/QZbare", "**QZbold** _QZem_", "QZhard  \nbreak", "\\`QZescaped\\`",
		"https://user:PWSECRET@evil.example/QZaddr?token=QSECRET#FSECRET",
	}
	for n := 1; n <= 6; n++ {
		for _, c := range []string{"`", "~"} {
			run := strings.Repeat(c, n)
			ws = append(ws, run, run+"QZrun", "QZrun"+run)
		}
	}
	return ws
}

// hostileWord stands in every other field while one is walked: backticks,
// a link, an attachment's image, raw HTML, a mention, a fence and
// emphasis in one word.
const hostileWord = "x``[QZlink](https://evil.example/login) ![QZimg](attachment:0123456789abcdef) <img src=x onerror=alert(1)> @QZmention ~~~ **QZbold** `"

// ticketLabel is a line of a block: one of describe's labels, alone or
// before its value.
var ticketLabel = regexp.MustCompile(`^(page|title|viewport|tree|run|task|attempt|commit|changed paths|the selection read|the save read|` +
	`the page declares|no screenshot|kind|element|name|selector|a click at its centre reaches|disabled|source|source class):( \S.*)?$`)

// ticketOwnLine is a line outside the blocks: the store's own words, a
// line of a quoted note, or the cut's last.
var ticketOwnLine = regexp.MustCompile(`^(|> \*\*The tree changed between the selection and the save\.\*\*|> \*\*The page served another revision than the tree's\.\*\*|` +
	`> What the page served is not confirmed against the tree\.|> \*\*No screenshot\.\*\*|Capture [0-9a-f]{16}, revision [0-9]+, as the page and the tree gave it:|` +
	"### Annotation [0-9]+|" + ticketFence + `|> .*|…)$`)

// ticketComment is a send's comment: the store's own words alone.
var ticketComment = regexp.MustCompile("^Capture [0-9a-f]{16} r[0-9]+, attached as (`capture-[0-9a-f]{16}-r[0-9]+\\.png` and )?`capture-[0-9a-f]{16}-r[0-9]+\\.json`\\.( It has no screenshot\\.)?$")

// checkTicketWords fails t unless every line of the description outside a
// block is the store's own, every block the store opened it closed, every
// line in one is a labelled value, nothing marked QZ stands outside one,
// no SECRET word of an address is anywhere, and the comment is the
// store's words alone. It gives the blocks and the QZ marks.
func checkTicketWords(t *testing.T, what, desc, comment string) (blocks, marks int) {
	t.Helper()
	in := false
	for i, l := range strings.Split(desc, "\n") {
		switch {
		case l == ticketFence:
			if in = !in; in {
				blocks++
			}
		case in && !ticketLabel.MatchString(l):
			t.Errorf("%s: line %d is in a block and no labelled value: %q", what, i, l)
		case !in && (!ticketOwnLine.MatchString(l) || strings.Contains(l, "QZ")):
			t.Errorf("%s: line %d is outside a block and not the store's: %q", what, i, l)
		}
	}
	if in {
		t.Errorf("%s: a block is left open:\n%s", what, desc)
	}
	if strings.Contains(desc+comment, "SECRET") {
		t.Errorf("%s: an address's user, password, query or fragment reached the ticket", what)
	}
	if !ticketComment.MatchString(comment) {
		t.Errorf("%s: the comment holds more than the store's words: %q", what, comment)
	}
	return blocks, strings.Count(desc, "QZ")
}

// ticketComments is the comment a send of the capture writes, with its
// screenshot's and its document's names.
func ticketComments(c Capture) string {
	names := []string{fileName(c, "json")}
	if c.Image.How != ImageNone {
		names = append([]string{fileName(c, "png")}, names...)
	}
	return sendComment(c, names)
}

// captureTicketCase is a row of testdata/capture-tickets.json: a
// description and a comment the store writes, the blocks it opened and
// the QZ marks it put in them.
type captureTicketCase struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Comment     string `json:"comment"`
	Blocks      int    `json:"blocks"`
	Marks       int    `json:"marks"`
	// Notes is whether the case's notes hold Markdown of their own, which
	// the web finds inside their quotes.
	Notes bool `json:"notes,omitempty"`
}

// Every string a capture holds but the person's notes, filled with every
// hostile word: all together (the gates kept, and filled too), one at a
// time with the hostile word in every other, and each repeated to its
// field's bound; and the description cut at every line's end. Each lands
// on a labelled line of a block the store opened and closed, and never in
// the comment. The cases the web renders are the fixture's, which must be
// what describe writes now.
func TestNothingACaptureHoldsIsMarkdownInATicket(t *testing.T) {
	paths, words := wordPaths(), hostileWords()
	if len(paths) < 100 {
		t.Fatalf("the walk fills %d strings", len(paths))
	}
	check := func(what string, c Capture) captureTicketCase {
		t.Helper()
		desc, comment := ticketText(c), ticketComments(c)
		if len(desc) > MaxTicketDescription {
			t.Errorf("%s: the description is %d bytes", what, len(desc))
		}
		blocks, marks := checkTicketWords(t, what, desc, comment)
		return captureTicketCase{Name: what, Description: desc, Comment: comment, Blocks: blocks, Marks: marks}
	}
	// What describe writes: each path a probe reaches.
	written := []string{}
	for _, p := range paths {
		if desc := ticketText(wordsFilled(func(q string) (string, bool) { return "QZprobe", q == p })); strings.Contains(desc, "QZprobe") {
			written = append(written, p)
		}
	}
	if len(written) < 20 {
		t.Fatalf("describe writes %d of the capture's strings: %v", len(written), written)
	}
	bound := func(p string) int {
		for suffix, n := range map[string]int{".Title": MaxCaptureTitle, ".URL": MaxCaptureURL, ".Selector": MaxSelector, ".Path": MaxHintFile,
			".Why": MaxImageWhy, ".Name": MaxElementName, ".Tag": MaxElementName} {
			if strings.HasSuffix(p, suffix) {
				return n
			}
		}
		return MaxAttributeValue
	}
	var fixture []captureTicketCase
	hostile := func(p, w string) Capture {
		return wordsFilled(func(q string) (string, bool) {
			if q == p {
				return w, true
			}
			return hostileWord, !gateWords[q]
		})
	}
	baseline := ticketText(hostile("", ""))
	for wi, w := range words {
		check(fmt.Sprintf("every string %q", w), wordsFilled(func(string) (string, bool) { return w, true }))
		check(fmt.Sprintf("every string %q, the gates kept", w), wordsFilled(func(p string) (string, bool) { return w, !gateWords[p] }))
		check(fmt.Sprintf("every string %q at its bound", w), wordsFilled(func(p string) (string, bool) {
			if w == "" {
				return w, !gateWords[p]
			}
			return strings.Repeat(w, bound(p)/len([]rune(w))), !gateWords[p]
		}))
		// One at a time: a string describe writes, or one that decides what
		// it writes, with each word; any other, with every twentieth, leaves
		// the description as it was.
		for _, p := range paths {
			switch c := hostile(p, w); {
			case slices.Contains(written, p) || gateWords[p]:
				check(fmt.Sprintf("%s %q, the rest hostile", p, w), c)
			case wi%20 == 0 && ticketText(c) != baseline:
				t.Errorf("%s %q: a string describe does not write changes the description", p, w)
			}
		}
		// The sample the web renders: the words in turn across the fields,
		// each beside another, and the empty and the blank word beside the
		// hostile one in each field describe writes.
		fixture = append(fixture, check(fmt.Sprintf("the words in turn from %d", wi), wordsFilled(func(p string) (string, bool) {
			i := 0
			for i < len(paths) && paths[i] != p {
				i++
			}
			return words[(i+wi)%len(words)], !gateWords[p]
		})))
	}
	for _, w := range []string{"", " "} {
		for _, p := range written {
			fixture = append(fixture, check(fmt.Sprintf("%s %q, the rest hostile", p, w), hostile(p, w)))
		}
	}
	// Cut at every line's end, and at each bound up to the fence and the
	// "…" past it: a block cut open is closed by its fence, in the bound.
	all := hostile("", "")
	whole := describe(all).text(1 << 30)
	var cuts []captureTicketCase
	for n := len("\n…"); n < len(whole); n++ {
		if e := strings.LastIndexByte(whole[:n], '\n'); e >= 0 && n-e > len(ticketFence)+1+len("\n…")+1 {
			continue
		}
		desc := describe(all).text(n)
		if len(desc) > n {
			t.Fatalf("cut to %d: %d bytes", n, len(desc))
		}
		blocks, marks := checkTicketWords(t, fmt.Sprintf("cut to %d", n), desc, ticketComments(all))
		// Cut inside a block: its own lines end with a value, then the fence.
		if lines := strings.Split(desc, "\n"); len(lines) > 3 && lines[len(lines)-3] == ticketFence && ticketLabel.MatchString(lines[len(lines)-4]) &&
			(len(cuts) == 0 || cuts[len(cuts)-1].Description != desc) {
			cuts = append(cuts, captureTicketCase{Name: fmt.Sprintf("cut to %d, inside a block", n), Description: desc, Comment: ticketComments(all), Blocks: blocks,
				Marks: marks})
		}
	}
	if len(cuts) < 10 {
		t.Fatalf("%d cuts inside a block", len(cuts))
	}
	for i := 0; i < len(cuts); i += len(cuts) / 8 {
		fixture = append(fixture, cuts[i])
	}
	// The cut is safe for any bound: one too small for the closing fence
	// and its "…" gives an empty text.
	for n := 0; n <= 16; n++ {
		desc := describe(all).text(n)
		if len(desc) > n || (n < len(ticketFence)+1+len("\n…") && desc != "") {
			t.Errorf("cut to %d: %q", n, desc)
		}
		checkTicketWords(t, fmt.Sprintf("cut to %d", n), desc, ticketComments(all))
	}
	// A person's note that opens a fence, a list or a quote ends with its
	// quote: the next block is still the store's. A note of more than three
	// lines, a heading and a fence opened after its third, is quoted on
	// every line, also cut at each of its line ends, and the next
	// annotation's block is the store's.
	long := "one\ntwo\nthree\n# Escaped heading\n```\na fence opened after the third line\nand its last"
	noted := func(note string) Capture {
		c := all
		c.Annotations = append([]Annotation{}, all.Annotations...)
		c.Annotations[0].Note = note
		return c
	}
	for _, note := range []string{"```", "~~~~", "```\nstill open", "- a\n\n      ```", "> ```", "a\r```", long} {
		row := check(fmt.Sprintf("a note %q, the rest hostile", note), noted(note))
		row.Notes = true
		fixture = append(fixture, row)
	}
	whole = describe(noted(long)).text(1 << 30)
	start := strings.Index(whole, "> one\n")
	if start < 0 || !strings.Contains(whole[start:], "> # Escaped heading\n> ```\n") || !strings.Contains(whole[start:], "### Annotation 2") {
		t.Fatalf("the long note is not quoted line by line before the next annotation:\n%s", whole)
	}
	for e := start + strings.IndexByte(whole[start:], '\n'); e < start+strings.Index(whole[start:], "\n\n")+1; e += strings.IndexByte(whole[e+1:], '\n') + 1 {
		desc := describe(noted(long)).text(e + 1 + len("\n…"))
		if desc != whole[:e+1]+"\n…" {
			t.Fatalf("cut after the note's line ending at %d: %q", e, desc[start:])
		}
		blocks, marks := checkTicketWords(t, fmt.Sprintf("the long note cut at %d", e), desc, ticketComments(all))
		fixture = append(fixture, captureTicketCase{Name: fmt.Sprintf("a note of more than three lines, cut at its line ending at %d", e), Description: desc,
			Comment: ticketComments(all), Blocks: blocks, Marks: marks, Notes: true})
	}
	path := filepath.Join("testdata", "capture-tickets.json")
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(struct {
		Note  string              `json:"note"`
		Cases []captureTicketCase `json:"cases"`
	}{"Generated by internal/dispatch's TestNothingACaptureHoldsIsMarkdownInATicket (go test -run TestNothingACaptureHoldsIsMarkdownInATicket -update-capture-tickets): a new ticket's description and a send's comment, every string of a capture but the person's notes filled with words Markdown reads as structure, each marked QZ; read by web/app/utils/captureTicket.test.ts.", fixture}); err != nil {
		t.Fatal(err)
	}
	if *updateCaptureTickets {
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, b.Bytes()) {
		t.Fatalf("%s is not what describe writes now (%v): go test -run TestNothingACaptureHoldsIsMarkdownInATicket -update-capture-tickets", path, err)
	}
}

// Through the store's own writes: a capture whose words are blank or hold
// backticks, links and an address, sent to a new ticket and to it again,
// gives a description and comments of the store's words, the page's in
// blocks.
func TestASentCapturesWordsStandInBlocks(t *testing.T) {
	s, _ := attachmentTicket(t)
	n, f := testCapture(false)
	n.Title, n.ImageWhy = " ", "see [QZwhy](https://evil.example/fix) https://u:PWSECRET@h.example/a?token=QSECRET#FSECRET"
	n.Annotations[0].Element.Name, n.Annotations[0].Element.Selector = " ", "x`y` [QZclick](https://evil.example/login)"
	n.Annotations[0].Element.Hit.Selector = "``[QZhit](https://evil.example/)"
	f.Sources[0][0].Path = "a`b [QZsource](https://evil.example/login) **QZbold**"
	moment(&n)
	id := mustCreate(t, s, "c1", n, f, nil)
	rc, err := send(t, s, "s1", id, 1, Destination{Kind: SendTicket, Project: "CND", Title: "Words"}, SendFacts{})
	want(t, rc, err, StatusDone, "")
	mustSend(t, s, "s2", id, 1, Destination{Kind: SendTicket, Key: rc.Key}, SendFacts{})
	tk := ticketPage(t, s, rc.Key)
	if len(tk.Comments) != 2 {
		t.Fatalf("the comments: %+v", tk.Comments)
	}
	for _, cm := range tk.Comments {
		blocks, marks := checkTicketWords(t, "sent", tk.Description, cm.Body)
		if blocks != 2 || marks != 5 {
			t.Fatalf("%d blocks and %d marks in:\n%s", blocks, marks, tk.Description)
		}
	}
	for _, w := range []string{"name:\n", "title:\n", "no screenshot: see [QZwhy](https://evil.example/fix) https://h.example/a\n"} {
		if !strings.Contains(tk.Description, w) {
			t.Errorf("the description lacks %q:\n%s", w, tk.Description)
		}
	}
}
