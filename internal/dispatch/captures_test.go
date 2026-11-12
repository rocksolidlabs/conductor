package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The captures and their annotations (Program item PV1, PR-1a,
// docs/plans/browser-evidence.md). Their sends are PR-1b's (#825).

// version5Tables is the tables version 5 makes.
var version5Tables = []string{"captures", "capture_annotations", "capture_annotation_sets", "capture_sends", "capture_send_outcomes",
	"capture_send_cancels", "capture_files"}

var (
	capCommit = strings.Repeat("a", 40)
	contentA  = strings.Repeat("c", 40)
	momentAt  = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
)

// pngOf is a PNG of w by h pixels, its first byte seed so two differ.
func pngOf(t *testing.T, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Pix[0] = seed
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testViewport is a desktop frame.
func testViewport() Viewport {
	return Viewport{Mode: "desktop", Width: 1280, Height: 800, Scale: 0.5, DPR: 2, ColorScheme: "light", UserAgent: "Chromium/141"}
}

// testCapture is a capture of an attempt's worktree: an element (a button
// covered by an overlay) and, with a screenshot, a region of it; without
// one, why.
func testCapture(image bool) (NewCapture, CaptureFacts) {
	n := NewCapture{Via: CaptureViaWeb, URL: "http://127.0.0.1:5173/app", Title: "App", Viewport: testViewport(), Epoch: 3,
		Label: TreeRead{Commit: capCommit, Content: contentA, Changed: 1, ReadAt: momentAt}, SelectedAt: momentAt,
		Annotations: []NewAnnotation{{Kind: AnnotationElement, Note: "Broken", Rect: Rect{10, 10, 80, 20},
			Element: &ElementReport{Tag: "button", Name: "Save", Selector: "button.save", Box: Rect{10, 10, 80, 20},
				Hit: ElementHit{Selector: "div.overlay", Tag: "div"}}}}}
	f := CaptureFacts{Origin: OriginAttempt, Run: "crew-0123abcd", Task: "build", Attempt: 2, Tree: TreeWorktree, Commit: capCommit, Changed: 1,
		ChangedPaths: []string{"web/app.vue"}, Content: contentA, Mutable: true, Where: "native", Scope: CaptureScope{Crew: "crew", Repo: "/repo"},
		Sources: [][]Source{{{Path: "web/app/components/SaveBar.vue", How: "vue", Found: true, Class: "tracked"}}}}
	if image {
		n.ImageHow, n.ImageScale = ImageTab, 1
		n.Annotations = append(n.Annotations, NewAnnotation{Kind: AnnotationRegion, Note: "here", Rect: Rect{0, 0, 4, 4}})
		f.Sources = append(f.Sources, nil)
	} else {
		n.ImageHow, n.ImageWhy = ImageNone, "the person declined the tab capture"
	}
	moment(&n)
	return n, f
}

// moment makes every element's moment the capture's own.
func moment(n *NewCapture) {
	for i := range n.Annotations {
		if n.Annotations[i].Kind == AnnotationElement {
			n.Annotations[i].Observed = &Observed{At: momentAt, URL: n.URL, Title: n.Title, Viewport: n.Viewport, Epoch: n.Epoch, Revision: n.Revision,
				Label: RevisionLabel{n.Label.Commit, n.Label.Content}}
		}
	}
}

// digestOf is the request's digest a client's body and image make.
func digestOf(t *testing.T, body any, img []byte) string {
	t.Helper()
	sum := ""
	if img != nil {
		sum = sumOf(img)
	}
	d, err := RequestDigest(body, sum)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// create makes a capture under the op id, img its PNG (nil for none).
func create(t *testing.T, s *Store, opID string, n NewCapture, f CaptureFacts, img []byte) (Receipt, error) {
	t.Helper()
	var r io.Reader
	if img != nil {
		r = bytes.NewReader(img)
	}
	return s.CreateCapture(context.Background(), testOp(opID), digestOf(t, n, img), n, f, r)
}

// mustCreate is create, done now, and gives the capture's id.
func mustCreate(t *testing.T, s *Store, opID string, n NewCapture, f CaptureFacts, img []byte) string {
	t.Helper()
	rc, err := create(t, s, opID, n, f, img)
	want(t, rc, err, StatusDone, "")
	if rc.Repeat || !ValidID(rc.ID) || rc.Rev != 1 {
		t.Fatalf("%s: %+v", opID, rc)
	}
	return rc.ID
}

// reedit edits the capture at rev under the op id.
func reedit(t *testing.T, s *Store, opID, id string, rev int64, edits []AnnotationEdit) (Receipt, error) {
	t.Helper()
	body := struct {
		Rev   int64            `json:"expectedRev"`
		Edits []AnnotationEdit `json:"annotations"`
	}{rev, edits}
	return s.EditCapture(context.Background(), testOp(opID), digestOf(t, body, nil), id, rev, edits)
}

// kept is the edits that keep a capture's annotations as they are.
func kept(c Capture) []AnnotationEdit {
	out := []AnnotationEdit{}
	for _, a := range c.Annotations {
		e := AnnotationEdit{K: a.K, Kind: a.Kind, Note: a.Note}
		if a.Kind == AnnotationRegion {
			e.Rect = a.Rect
		}
		out = append(out, e)
	}
	return out
}

// captureOf reads the capture with the id.
func captureOf(t *testing.T, s *Store, id string) Capture {
	t.Helper()
	c, err := s.Capture(context.Background(), id)
	if err != nil {
		t.Fatalf("capture %s: %v", id, err)
	}
	return c
}

// noUploads fails unless no lease and no temp file is left.
func noUploads(t *testing.T, s *Store) {
	t.Helper()
	if n := count(t, s, "SELECT COUNT(*) FROM uploads"); n != 0 || len(tempFiles(t, s)) != 0 {
		t.Fatalf("%d leases and temp files %v left", n, tempFiles(t, s))
	}
}

// Two captures of one image hold one file, each its own row of it, the
// image's size read from its header; the rows are one transaction with
// the receipt, no lease left, OnChange told the capture once, and the
// image opens by the capture's own row.
func TestACaptureIsStoredWithItsImageOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	log := listen(s)
	img := pngOf(t, 64, 32, 1)
	n, f := testCapture(true)
	a := mustCreate(t, s, "c1", n, f, img)
	if c := log.one(t); len(c) != 1 || c[0].Entity != EntityCapture || c[0].ID != a || c[0].Ticket != "" || c[0].Project != "" {
		t.Fatalf("OnChange: %+v", c)
	}
	// The desktop client's screenshot by its own path, and one by the
	// tab's: each as the client says it was taken.
	n.Via, n.ImageHow = CaptureViaDesktop, ImageDesktop
	b := mustCreate(t, s, "c2", n, f, img)
	if got := contentFiles(t, s); !slices.Equal(got, []string{sumOf(img)}) {
		t.Fatalf("the content files: %v", got)
	}
	if got := texts(t, s, "SELECT capture_id || ' ' || holder FROM capture_files WHERE sha256 = ? ORDER BY capture_id", sumOf(img)); len(got) != 2 ||
		!slices.Contains(got, a+" capture") || !slices.Contains(got, b+" capture") {
		t.Fatalf("the files held: %v", got)
	}
	noUploads(t, s)
	ca, cb := captureOf(t, s, a), captureOf(t, s, b)
	if ca.Image != (CaptureImage{SHA256: sumOf(img), Width: 64, Height: 32, Bytes: int64(len(img)), How: ImageTab, Scale: 1}) || cb.Image.How != ImageDesktop {
		t.Fatalf("the images: %+v %+v", ca.Image, cb.Image)
	}
	n.ImageHow = ImageTab
	if c := captureOf(t, s, mustCreate(t, s, "c2-tab", n, f, img)); c.Image.How != ImageTab || c.Via != CaptureViaDesktop {
		t.Fatalf("a desktop client's tab screenshot: %+v %s", c.Image, c.Via)
	}
	if ca.Rev != 1 || len(ca.Annotations) != 2 || ca.Annotations[0].K != 1 || ca.Annotations[0].Sources[0].Path != "web/app/components/SaveBar.vue" ||
		ca.Origin.Drift != nil || ca.Origin.Serving.State != ServingUnconfirmed || ca.Actor != "test" || ca.CreatedBy != "Tester" {
		t.Fatalf("the capture: %+v", ca)
	}
	_, file, err := s.OpenCaptureImage(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(file)
	file.Close()
	if !bytes.Equal(got, img) {
		t.Fatal("the image opened is not the one taken")
	}
	// Without a screenshot: no file, the why kept.
	n, f = testCapture(false)
	c := captureOf(t, s, mustCreate(t, s, "c3", n, f, nil))
	if c.Image != (CaptureImage{How: ImageNone, Why: n.ImageWhy}) || len(contentFiles(t, s)) != 1 {
		t.Fatalf("a capture with no screenshot: %+v", c.Image)
	}
	if _, _, err := s.OpenCaptureImage(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no screenshot opened: %v", err)
	}
}

// boundCase is a bound at its edge and one past: set makes the capture
// hold k of what is bounded.
type boundCase struct {
	name    string
	edge    int
	code    string
	noImage bool
	set     func(n *NewCapture, f *CaptureFacts, k int)
}

// rep is k of s.
func rep(s string, k int) string { return strings.Repeat(s, k) }

// element is the capture's first annotation's report.
func element(n *NewCapture) *ElementReport { return n.Annotations[0].Element }

func TestCaptureBoundsAtTheirEdges(t *testing.T) {
	scopeKeys := len(`{"crew":"","repo":""}`)
	cases := []boundCase{
		{"annotations", MaxCaptureAnnotations, RefusalBound, false, func(n *NewCapture, f *CaptureFacts, k int) {
			for len(n.Annotations) < k {
				n.Annotations = append(n.Annotations, n.Annotations[len(n.Annotations)-1])
			}
			f.Sources = nil
		}},
		{"a note, in characters", MaxAnnotationNote, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Annotations[0].Note = rep("é", k) }},
		{"the URL", MaxCaptureURL, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.URL = "http://h/" + rep("a", k-9) }},
		{"the title, in characters", MaxCaptureTitle, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Title = rep("é", k) }},
		{"the user agent", MaxUserAgent, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Viewport.UserAgent = rep("a", k) }},
		{"why there is no screenshot, in characters", MaxImageWhy, RefusalBound, true, func(n *NewCapture, _ *CaptureFacts, k int) { n.ImageWhy = rep("é", k) }},
		{"the declared revision", MaxDeclaredRevision, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			n.Revision = rep("a", 64) + "+" + rep("b", k-65)
		}},
		{"the scope, as encoded", MaxCaptureScope, RefusalBound, false, func(_ *NewCapture, f *CaptureFacts, k int) {
			f.Scope = CaptureScope{Crew: rep("c", 1024), Repo: rep("r", k-1024-scopeKeys)}
		}},
		{"a rectangle's number", MaxRectCoord, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Annotations[0].Rect.X = float64(k) }},
		{"the element's text", MaxElementText, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Text = rep("a", k) }},
		{"the selector", MaxSelector, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Selector = rep("a", k) }},
		{"the tag", MaxElementName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Tag = rep("a", k) }},
		{"the accessible name", MaxElementName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Name = rep("a", k) }},
		{"the id", MaxElementName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).ID = rep("a", k) }},
		{"the role", MaxElementName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Role = rep("a", k) }},
		{"the classes, joined", MaxElementName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Classes = []string{rep("a", k/2), rep("b", k-k/2-1)}
		}},
		{"the attributes", MaxElementAttributes, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Attributes = slices.Repeat([][2]string{{"data-x", "v"}}, k)
		}},
		{"an attribute's name", MaxAttributeName, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Attributes = [][2]string{{rep("a", k), "v"}}
		}},
		{"an attribute's value", MaxAttributeValue, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Attributes = [][2]string{{"title", rep("a", k)}}
		}},
		{"the HTML", MaxElementHTML, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).HTML = rep("a", k) }},
		{"the neighbourhood", MaxNeighbourhood, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Neighbourhood = rep("a", k) }},
		{"the ancestors", MaxAncestors, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Ancestors = slices.Repeat([]Ancestor{{Tag: "div"}}, k)
		}},
		{"an ancestor, as encoded", MaxAncestor, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Ancestors = []Ancestor{{Tag: rep("a", k-len(`{"tag":""}`))}}
		}},
		{"the styles", MaxStyles, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Styles = slices.Repeat([][2]string{{"color", "red"}}, k)
		}},
		{"a style's value", MaxStyleValue, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Styles = [][2]string{{"color", rep("a", k)}}
		}},
		{"a word of the report", MaxElementWord, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).State.PointerEvents = rep("a", k) }},
		{"the hints", MaxHints, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Hints = slices.Repeat([]Hint{{How: "vue", File: "a.vue"}}, k)
		}},
		{"a hint's file", MaxHintFile, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Hints = []Hint{{How: "vue", File: rep("a", k)}}
		}},
		{"a hint's component", MaxHintComponent, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Hints = []Hint{{How: "vue", File: "a.vue", Component: rep("a", k)}}
		}},
		{"the sources", MaxHints, RefusalBound, false, func(_ *NewCapture, f *CaptureFacts, k int) {
			f.Sources[0] = slices.Repeat([]Source{{Path: "a.vue", How: "vue", Class: "tracked"}}, k)
		}},
		{"a source's path", MaxHintFile, RefusalBound, false, func(_ *NewCapture, f *CaptureFacts, k int) {
			f.Sources[0] = []Source{{Path: rep("a", k), How: "vue", Class: "tracked"}}
		}},
		{"a hint's line", MaxFindingLine, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Hints = []Hint{{How: "vue", File: "a.vue", Line: k}}
		}},
		{"a hint's column", MaxFindingLine, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			element(n).Hints = []Hint{{How: "vue", File: "a.vue", Column: k}}
		}},
		{"a source's line", MaxFindingLine, RefusalInvalid, false, func(_ *NewCapture, f *CaptureFacts, k int) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "tracked", Line: k}}
		}},
		{"a source's column", MaxFindingLine, RefusalInvalid, false, func(_ *NewCapture, f *CaptureFacts, k int) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "tracked", Column: k}}
		}},
		{"the viewport's width", MaxRectCoord, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Viewport.Width = k }},
		{"the viewport's height", MaxRectCoord, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Viewport.Height = k }},
		{"the viewport's scale", 64, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Viewport.Scale = float64(k) }},
		{"the viewport's pixel ratio", 64, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Viewport.DPR = float64(k) }},
		{"the element's motion", MaxRectCoord, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).State.Motion = k }},
		{"the cut steps", 10, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) { element(n).Cut = slices.Repeat([]string{"html"}, k) }},
		{"the selection's changed count", MaxChangedCount, RefusalInvalid, false, func(n *NewCapture, _ *CaptureFacts, k int) { n.Label.Changed = k }},
		{"the tree's changed count", MaxChangedCount, RefusalInvalid, false, func(_ *NewCapture, f *CaptureFacts, k int) { f.Changed = k }},
		{"the element's report, as measured", MaxElementReport, RefusalBound, false, func(n *NewCapture, _ *CaptureFacts, k int) {
			e := element(n)
			e.HTML = rep("h", MaxElementHTML)
			e.Neighbourhood = ""
			e.Neighbourhood = rep("n", k-measure(e)-len(`,"neighbourhood":""`))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []int{c.edge, c.edge + 1} {
				n, f := testCapture(!c.noImage)
				c.set(&n, &f, k)
				moment(&n)
				if c.name == "the element's report, as measured" && measure(element(&n)) != k {
					t.Fatalf("the report measures %d, not %d", measure(element(&n)), k)
				}
				_, _, err := checkCapture(n, f, !c.noImage)
				var ref *Refusal
				switch {
				case k == c.edge && err != nil:
					t.Fatalf("at its edge %d: %v", k, err)
				case k > c.edge && (!errors.As(err, &ref) || ref.Code != c.code):
					t.Fatalf("one past its edge, %d: %v, want %s", k, err, c.code)
				}
			}
		})
	}
	// The numbers bound from below: one past refused (a scale and a pixel
	// ratio are above 0, the rest 0 or more).
	for _, c := range []struct {
		name string
		set  func(n *NewCapture, f *CaptureFacts)
	}{
		{"the viewport's width", func(n *NewCapture, _ *CaptureFacts) { n.Viewport.Width = -1 }},
		{"the viewport's height", func(n *NewCapture, _ *CaptureFacts) { n.Viewport.Height = -1 }},
		{"the viewport's scale", func(n *NewCapture, _ *CaptureFacts) { n.Viewport.Scale = 0 }},
		{"the viewport's pixel ratio", func(n *NewCapture, _ *CaptureFacts) { n.Viewport.DPR = 0 }},
		{"the element's motion", func(n *NewCapture, _ *CaptureFacts) { element(n).State.Motion = -1 }},
		{"a hint's line", func(n *NewCapture, _ *CaptureFacts) { element(n).Hints = []Hint{{How: "vue", File: "a.vue", Line: -1}} }},
		{"a hint's column", func(n *NewCapture, _ *CaptureFacts) {
			element(n).Hints = []Hint{{How: "vue", File: "a.vue", Column: -1}}
		}},
		{"a source's line", func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "tracked", Line: -1}}
		}},
		{"a source's column", func(_ *NewCapture, f *CaptureFacts) {
			f.Sources[0] = []Source{{Path: "a.vue", How: "vue", Class: "tracked", Column: -1}}
		}},
	} {
		n, f := testCapture(true)
		c.set(&n, &f)
		moment(&n)
		var ref *Refusal
		if _, _, err := checkCapture(n, f, true); !errors.As(err, &ref) || ref.Code != RefusalInvalid {
			t.Errorf("%s below its bound: %v, want invalid", c.name, err)
		}
	}
	// The changed paths are cut, never refused: at whichever bound comes
	// first, the count whole. exact(0) encodes to MaxChangedPathsBytes
	// exactly, exact(1) one byte past.
	exact := func(past int) []string {
		paths := slices.Repeat([]string{rep("p", 4000)}, 3)
		return append(paths, rep("q", MaxChangedPathsBytes-measure(paths)-len(`,""`)+past))
	}
	if measure(exact(0)) != MaxChangedPathsBytes || measure(exact(1)) != MaxChangedPathsBytes+1 {
		t.Fatalf("the changed paths encode to %d and %d", measure(exact(0)), measure(exact(1)))
	}
	for _, c := range []struct {
		paths    []string
		kept     int
		cut      bool
		changed  int
		wantsCut bool
	}{
		{slices.Repeat([]string{"a.go"}, MaxChangedPaths), MaxChangedPaths, false, MaxChangedPaths, false},
		{slices.Repeat([]string{"a.go"}, MaxChangedPaths+1), MaxChangedPaths, true, MaxChangedPaths + 1, true},
		{slices.Repeat([]string{rep("p", 4000)}, 5), 4, true, 5, true},
		{exact(0), 4, false, 4, false},
		{exact(1), 3, true, 4, true},
		{[]string{"a.go"}, 1, false, 9, true},
	} {
		n, f := testCapture(true)
		f.ChangedPaths, f.Changed = c.paths, c.changed
		got, _, err := checkCapture(n, f, true)
		if err != nil || len(got.Origin.ChangedPaths) != c.kept || got.Origin.ChangedCut != c.wantsCut || got.Origin.Changed != c.changed ||
			measure(got.Origin.ChangedPaths) > MaxChangedPathsBytes {
			t.Fatalf("%d paths: %d kept, cut %v, %v", len(c.paths), len(got.Origin.ChangedPaths), got.Origin.ChangedCut, err)
		}
	}
	// The store's own: the image's bytes and sides, a region within it,
	// the live captures; each refusal leaves no row, no lease and no file.
	s := openStore(t, storeDir(t))
	tries := 0
	refused := func(name string, img []byte, n NewCapture, f CaptureFacts, code string) {
		t.Helper()
		tries++
		rc, err := create(t, s, opID("refused", tries), n, f, img)
		want(t, rc, err, StatusFailed, code)
		noUploads(t, s)
		if img != nil && exists(t, contentAt(s, sumOf(img))) && count(t, s, "SELECT COUNT(*) FROM capture_files WHERE sha256 = ?", sumOf(img)) == 0 {
			t.Fatalf("%s left its file", name)
		}
	}
	n, f := testCapture(true)
	small := pngOf(t, 8, 8, 2)
	atBound := append(slices.Clone(small), make([]byte, MaxCaptureImage-len(small))...)
	mustCreate(t, s, "image-at-bound", n, f, atBound)
	refused("image", append(slices.Clone(atBound), 0), n, f, RefusalBound)
	mustCreate(t, s, "side-at-bound", n, f, pngOf(t, MaxCaptureImageSide, 8, 3))
	refused("side", pngOf(t, MaxCaptureImageSide+1, 8, 4), n, f, RefusalBound)
	refused("not-png", []byte("GIF89a, not a PNG"), n, f, RefusalInvalid)
	outside := n
	outside.Annotations = slices.Clone(n.Annotations)
	outside.Annotations[1].Rect = Rect{6, 6, 3, 3}
	refused("region", small, outside, f, RefusalInvalid)
	// Each edge of the 8 by 8 screenshot on its own: one past refused, at
	// it taken.
	for i, r := range []Rect{{-1, 0, 2, 2}, {0, -1, 2, 2}, {7, 0, 2, 2}, {0, 7, 2, 2}} {
		outside.Annotations[1].Rect = r
		refused(fmt.Sprintf("a region past an edge (%d)", i), pngOf(t, 8, 8, byte(70+i)), outside, f, RefusalInvalid)
	}
	for i, r := range []Rect{{0, 0, 8, 8}, {6, 6, 2, 2}} {
		outside.Annotations[1].Rect = r
		mustCreate(t, s, opID("region-at-edge", i), outside, f, pngOf(t, 8, 8, byte(80+i)))
	}
	// MaxCaptures live: the rows past the first copied in, as a replica's
	// would be; a deleted one is not counted.
	nn, nf := testCapture(false)
	first := captureOf(t, s, mustCreate(t, s, "first", nn, nf, nil))
	cols := columnsOf(t, s.db, "captures")
	rest := strings.Join(cols[1:], ", ")
	live := count(t, s, "SELECT COUNT(*) FROM captures")
	if _, err := s.db.Exec(`WITH RECURSIVE k(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM k WHERE x < ?)
		INSERT INTO captures (id, `+rest+`) SELECT printf('%016x', x), `+rest+` FROM captures, k WHERE captures.id = ?`, MaxCaptures-live-1, first.ID); err != nil {
		t.Fatal(err)
	}
	last := mustCreate(t, s, "last", n, f, pngOf(t, 8, 8, 5))
	refused("past MaxCaptures", pngOf(t, 8, 8, 6), n, f, RefusalBound)
	if rc, err := s.DeleteCapture(context.Background(), testOp("d1"), last); err != nil || rc.Status != StatusDone {
		t.Fatalf("a delete: %+v %v", rc, err)
	}
	mustCreate(t, s, "after-a-delete", n, f, pngOf(t, 8, 8, 6))
}

// Every bound at once: twenty elements, each report at
// MaxElementReport as measured and its eight sources at their bound, every
// note four thousand characters that encode as six bytes each, and two
// hundred changed paths of 4,000 bytes; created and every note edited, the
// document within MaxCaptureDocument and every change row within
// MaxChangeValue.
func TestTheFullestCaptureFitsItsDocument(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	el := *element(&n)
	el.Hints = slices.Repeat([]Hint{{How: "vue", File: rep("f", MaxHintFile), Component: rep("c", MaxHintComponent)}}, MaxHints)
	el.HTML = rep("h", MaxElementHTML)
	el.Neighbourhood = rep("n", MaxElementReport-measure(&el)-len(`,"neighbourhood":""`))
	if measure(&el) != MaxElementReport {
		t.Fatalf("the report measures %d", measure(&el))
	}
	sources := slices.Repeat([]Source{{Path: rep("p", MaxHintFile), Component: rep("c", MaxHintComponent), How: "vue", Class: "tracked"}}, MaxHints)
	note := rep("\u2028", MaxAnnotationNote)
	n.Annotations, f.Sources = nil, nil
	for range MaxCaptureAnnotations {
		e := el
		n.Annotations = append(n.Annotations, NewAnnotation{Kind: AnnotationElement, Note: note, Rect: Rect{1, 1, 2, 2}, Element: &e})
		f.Sources = append(f.Sources, sources)
	}
	f.ChangedPaths, f.Changed = slices.Repeat([]string{rep("p", 4000)}, MaxChangedPaths), MaxChangedPaths
	n.Title, n.URL = rep("\u2028", MaxCaptureTitle), "http://h/"+rep("a", MaxCaptureURL-9)
	moment(&n)
	rc, err := create(t, s, "full", n, f, pngOf(t, 8, 8, 7))
	want(t, rc, err, StatusDone, "")
	c := captureOf(t, s, rc.ID)
	edits := kept(c)
	for i := range edits {
		edits[i].Note = rep("\u2029", MaxAnnotationNote)
	}
	erc, err := reedit(t, s, "full-edit", rc.ID, 1, edits)
	want(t, erc, err, StatusDone, "")
	doc, err := s.Document(ctx, rc.ID, 2)
	if err != nil || len(doc) > MaxCaptureDocument {
		t.Fatalf("the document: %d bytes, %v", len(doc), err)
	}
	var d CaptureDocument
	if err := json.Unmarshal(doc, &d); err != nil || len(d.Annotations) != MaxCaptureAnnotations || d.Annotations[19].Note != edits[19].Note || d.Rev != 2 {
		t.Fatalf("the document read back: %v", err)
	}
	for _, op := range []string{"full", "full-edit"} {
		for _, row := range changesOf(t, s, "test", op) {
			if len(row.Value) > MaxChangeValue || len(row.Prior) > MaxChangeValue {
				t.Fatalf("%s's row %d (%s) is %d bytes", op, row.N, row.Field, len(row.Value))
			}
			if row.Field == "annotations" {
				var v, p SetPointer
				if json.Unmarshal([]byte(row.Value), &v) != nil || json.Unmarshal([]byte(row.Prior), &p) != nil || v.Set == "" || v.Rev != p.Rev+1 {
					t.Fatalf("the pointer's row: %s from %s", row.Value, row.Prior)
				}
			}
		}
	}
	if !utf8.Valid(doc) {
		t.Fatal("the document is not UTF-8")
	}
	// The document's own bound, which no capture within the other bounds
	// reaches: a set whose document would be past it is refused bound, and
	// one at it is written.
	big := captureOf(t, s, rc.ID)
	for _, c := range []struct {
		past int
		code string
	}{{1, RefusalBound}, {0, ""}} {
		big.Title = ""
		big.Title = rep("t", MaxCaptureDocument-measure(documentAt(big, big.Rev+1))+c.past)
		status := StatusDone
		if c.code != "" {
			status = StatusFailed
		}
		brc, err := s.Apply(ctx, testOp(fmt.Sprintf("document-%d", c.past)), IntentCaptureEdit, captureArgs{ID: big.ID, Received: rep("e", 64)}, func(x *tx) error {
			return x.writeSet(big, big.Rev+1, big.Annotations)
		})
		want(t, brc, err, status, c.code)
	}
}

// documentAt is c's document at rev, as writeSet measures it.
func documentAt(c Capture, rev int64) CaptureDocument {
	d := c.CaptureDocument
	d.Rev = rev
	return d
}

// A crash between the screenshot's publication and its row: the store
// gone before the operation, the file left leased, no row; a later store
// keeps the file while the lease is young, takes both once it is older
// than its grace, and the capture made again is taken.
func TestACaptureCrashBetweenFileAndRow(t *testing.T) {
	ctx := context.Background()
	dir := storeDir(t)
	s := openStore(t, dir)
	img := pngOf(t, 8, 8, 8)
	n, f := testCapture(true)
	s.beforeCommit = func() { s.Close() }
	if _, err := create(t, s, "crash", n, f, img); !errors.Is(err, ErrClosed) {
		t.Fatalf("the crashed create: %v", err)
	}
	again := openStore(t, dir)
	if n := count(t, again, "SELECT COUNT(*) FROM captures"); n != 0 || !exists(t, contentAt(again, sumOf(img))) ||
		count(t, again, "SELECT COUNT(*) FROM uploads WHERE sha256 = ?", sumOf(img)) != 1 {
		t.Fatalf("after the crash: %d captures, the file there %v", n, exists(t, contentAt(again, sumOf(img))))
	}
	if err := again.sweep(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
		t.Fatal(err)
	}
	if exists(t, contentAt(again, sumOf(img))) || count(t, again, "SELECT COUNT(*) FROM uploads") != 0 {
		t.Fatal("the crashed upload's file or lease outlived the sweep")
	}
	if rc, _, err := again.Receipt(ctx, "test", "crash"); err != nil || rc.OpID != "" {
		t.Fatalf("a receipt of the crashed create: %+v %v", rc, err)
	}
	mustCreate(t, again, "crash", n, f, img)
}

// An edit at a revision that is not the capture's is refused
// stale_revision, kept as a receipt, nothing else written.
func TestAStaleEditIsRefused(t *testing.T) {
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 9))
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "Still broken"
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	if rc.Rev != 2 {
		t.Fatalf("the edit's rev: %+v", rc)
	}
	sets := count(t, s, "SELECT COUNT(*) FROM capture_annotation_sets")
	edits[0].Note = "Fixed?"
	rc, err = reedit(t, s, "e2", id, 1, edits)
	want(t, rc, err, StatusFailed, RefusalStaleRevision)
	if c := captureOf(t, s, id); c.Rev != 2 || c.Annotations[0].Note != "Still broken" || count(t, s, "SELECT COUNT(*) FROM capture_annotation_sets") != sets {
		t.Fatalf("a stale edit wrote: %+v", c)
	}
	if kept, found, _ := s.Receipt(context.Background(), "test", "e2"); !found || kept.Refusal != RefusalStaleRevision {
		t.Fatalf("the stale edit's receipt: %+v", kept)
	}
}

// An edit brings no element: a report is refused invalid ("Capture
// again"), and so is a new annotation that is not a region, an
// element's box moved, a region on a capture without a screenshot and a
// place named twice.
func TestAnEditRefusesANewElementReport(t *testing.T) {
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 10))
	c := captureOf(t, s, id)
	region := func(r Rect) []AnnotationEdit { e := kept(c); e[1].Rect = r; return e }
	for i, bad := range [][]AnnotationEdit{
		append(kept(c), AnnotationEdit{Kind: AnnotationElement, Note: "another", Element: &ElementReport{Tag: "a", Selector: "a"}}),
		append(kept(c), AnnotationEdit{Kind: AnnotationElement, Note: "another"}),
		func() []AnnotationEdit { e := kept(c); e[0].Element = c.Annotations[0].Element; return e }(),
		func() []AnnotationEdit { e := kept(c); e[0].Rect = Rect{1, 2, 3, 4}; return e }(),
		append(kept(c), kept(c)[0]),
		// A region's rectangle within the 8 by 8 screenshot, each edge on its
		// own, and a number.
		region(Rect{-1, 0, 2, 2}), region(Rect{0, -1, 2, 2}), region(Rect{7, 0, 2, 2}), region(Rect{0, 7, 2, 2}),
		region(Rect{math.NaN(), 0, 1, 1}), region(Rect{0, 0, -1, 1}), region(Rect{math.Inf(1), 0, 1, 1}),
		append(kept(c), AnnotationEdit{Kind: AnnotationRegion, Note: "new", Rect: Rect{6, 6, 3, 3}}),
		func() []AnnotationEdit { e := kept(c); e[1].Note = "a\x00"; return e }(),
		func() []AnnotationEdit { e := kept(c); e[0].K = 3; return e }(),
	} {
		// A digest of its own: a NaN has no JSON to digest.
		rc, err := s.EditCapture(context.Background(), testOp(opID("bad", i)), fmt.Sprintf("%064d", i), id, 1, bad)
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	// One past MaxCaptureAnnotations is refused bound; at the bound, and a
	// region at the screenshot's edges, taken.
	many := kept(c)
	for len(many) < MaxCaptureAnnotations+1 {
		many = append(many, AnnotationEdit{Kind: AnnotationRegion, Note: "more", Rect: Rect{0, 0, 1, 1}})
	}
	rc, err := reedit(t, s, "too-many", id, 1, many)
	want(t, rc, err, StatusFailed, RefusalBound)
	many = many[:MaxCaptureAnnotations]
	many[1].Rect = Rect{6, 6, 2, 2}
	rc, err = reedit(t, s, "as-many", id, 1, many)
	want(t, rc, err, StatusDone, "")
	c = captureOf(t, s, id)
	// Removing every annotation brings none and is taken.
	rc, err = reedit(t, s, "remove-all", id, c.Rev, []AnnotationEdit{})
	want(t, rc, err, StatusDone, "")
	n, f = testCapture(false)
	none := mustCreate(t, s, "c2", n, f, nil)
	// A region on a capture with no screenshot, even one of no size that
	// no screenshot's edge would refuse.
	for i, r := range []Rect{{0, 0, 1, 1}, {}} {
		rc, err = reedit(t, s, opID("region", i), none, 1, []AnnotationEdit{{K: 1, Kind: AnnotationElement, Note: "x"}, {Kind: AnnotationRegion, Note: "r", Rect: r}})
		want(t, rc, err, StatusFailed, RefusalInvalid)
	}
	if got := captureOf(t, s, none); got.Rev != 1 || len(got.Annotations) != 1 {
		t.Fatalf("a refused edit wrote: %+v", got)
	}
}

// An edit writes a version of each annotation it changes and keeps the
// version of each it leaves, a set naming them in their new order, and
// the pointer; one past MaxAnnotationSets is refused bound.
func TestAnEditWritesVersionsAndASet(t *testing.T) {
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 11))
	c := captureOf(t, s, id)
	before := texts(t, s, "SELECT sha256 FROM capture_annotations WHERE capture_id = ? ORDER BY sha256", id)
	edits := kept(c)
	edits[1].Note = "a region, renamed"
	edits[0], edits[1] = edits[1], edits[0]
	rc, err := reedit(t, s, "e1", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	after := captureOf(t, s, id)
	if after.Annotations[0].Note != "a region, renamed" || after.Annotations[0].K != 1 || after.Annotations[1].Note != "Broken" ||
		after.Annotations[1].Element == nil || after.Set.Rev != 2 {
		t.Fatalf("after the edit: %+v", after)
	}
	versions := texts(t, s, "SELECT sha256 FROM capture_annotations WHERE capture_id = ? ORDER BY sha256", id)
	var members []string
	json.Unmarshal([]byte(texts(t, s, "SELECT members FROM capture_annotation_sets WHERE id = ?", after.Set.Set)[0]), &members)
	if len(versions) != 3 || !slices.Contains(versions, before[0]) || !slices.Contains(versions, before[1]) || len(members) != 2 ||
		!slices.Contains(before, members[1]) || slices.Contains(before, members[0]) {
		t.Fatalf("the versions %v, the set %v, before %v", versions, members, before)
	}
	rows := changesOf(t, s, "test", "e1")
	fields := []string{}
	for _, r := range rows {
		fields = append(fields, r.Field+" "+r.Op)
	}
	if !slices.Equal(fields, []string{"versions add", "sets add", "annotations set"}) {
		t.Fatalf("the edit's rows: %v", fields)
	}
	// Up to MaxAnnotationSets sets a capture; the next refused.
	for rev := int64(2); rev < MaxAnnotationSets; rev++ {
		rc, err := reedit(t, s, opID("more", int(rev)), id, rev, kept(captureOf(t, s, id)))
		want(t, rc, err, StatusDone, "")
	}
	rc, err = reedit(t, s, "one-past", id, MaxAnnotationSets, kept(captureOf(t, s, id)))
	want(t, rc, err, StatusFailed, RefusalBound)
	if n := count(t, s, "SELECT COUNT(*) FROM capture_annotation_sets WHERE capture_id = ?", id); n != MaxAnnotationSets {
		t.Fatalf("%d sets", n)
	}
}

// A note A, then B, then A again: three sets, the first and the third of
// one digest and two ids, each with its own actor and op id, one version
// for A; the document at r3 its own; an earlier order restored is a set
// of its own too.
func TestAnAnnotationSetIsARevisionNotItsContent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 12))
	for i, note := range []string{"B", "Broken"} {
		edits := kept(captureOf(t, s, id))
		edits[0].Note = note
		rc, err := s.EditCapture(ctx, Op{Actor: fmt.Sprintf("editor%d", i), ID: opID("e", i)}, digestOf(t, edits, nil), id, int64(i+1), edits)
		want(t, rc, err, StatusDone, "")
	}
	type set struct{ id, digest, actor, op string }
	var sets []set
	rows, err := s.db.Query("SELECT id, digest, actor, op_id FROM capture_annotation_sets WHERE capture_id = ? ORDER BY rev", id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var x set
		rows.Scan(&x.id, &x.digest, &x.actor, &x.op)
		sets = append(sets, x)
	}
	rows.Close()
	if len(sets) != 3 || sets[0].digest != sets[2].digest || sets[0].id == sets[2].id || sets[1].digest == sets[0].digest ||
		sets[0].actor != "test" || sets[2].actor != "editor1" || sets[2].op != "e1" {
		t.Fatalf("the sets: %+v", sets)
	}
	if n := count(t, s, "SELECT COUNT(*) FROM capture_annotations WHERE capture_id = ?", id); n != 3 {
		t.Fatalf("%d versions: A, B and the region", n)
	}
	r3, err := s.Document(ctx, id, 3)
	if err != nil || !strings.Contains(string(r3), `"rev":3`) {
		t.Fatalf("the document at r3: %v", err)
	}
	if _, err := s.Document(ctx, id, 1); !errors.Is(err, ErrStaleCapture) {
		t.Fatalf("the document at r1: %v", err)
	}
	// An order swapped and restored: the first and the last set of one
	// digest, two ids.
	for i, swap := range []bool{true, true} {
		edits := kept(captureOf(t, s, id))
		if swap {
			edits[0], edits[1] = edits[1], edits[0]
		}
		rc, err := reedit(t, s, opID("order", i), id, int64(3+i), edits)
		want(t, rc, err, StatusDone, "")
	}
	got := texts(t, s, "SELECT digest || ' ' || id FROM capture_annotation_sets WHERE capture_id = ? AND rev IN (3, 5) ORDER BY rev", id)
	if a, b := strings.Fields(got[0]), strings.Fields(got[1]); a[0] != b[0] || a[1] == b[1] {
		t.Fatalf("an order restored: %v", got)
	}
	// Two replicas' identical edits at one rev, by two actors: both sets
	// kept, put into one store two ids of one digest, and the pointer a
	// merge picks names one of them whole.
	snap := filepath.Join(t.TempDir(), "replica.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	os.MkdirAll(dir, 0o700)
	b, _ := os.ReadFile(snap)
	os.WriteFile(filepath.Join(dir, File), b, 0o600)
	replica := openStore(t, dir)
	var pointers [2]ChangeRow
	for i, st := range []*Store{s, replica} {
		edits := kept(captureOf(t, st, id))
		edits[0].Note = "the same on both"
		rc, err := st.EditCapture(ctx, Op{Actor: fmt.Sprintf("replica%d", i), ID: "same"}, digestOf(t, edits, nil), id, 5, edits)
		want(t, rc, err, StatusDone, "")
		for _, r := range changesOf(t, st, fmt.Sprintf("replica%d", i), "same") {
			if r.Field == "annotations" {
				pointers[i] = r
			}
		}
	}
	cols := "capture_id, id, rev, digest, members, at, actor, op_id"
	row := texts(t, replica, "SELECT json_array("+cols+") FROM capture_annotation_sets WHERE capture_id = ? AND rev = 6", id)
	var vals []any
	if len(row) != 1 || json.Unmarshal([]byte(row[0]), &vals) != nil {
		t.Fatalf("the replica's set: %v", row)
	}
	if _, err := s.db.Exec("INSERT INTO capture_annotation_sets ("+cols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)", vals...); err != nil {
		t.Fatal(err)
	}
	both := texts(t, s, "SELECT digest || ' ' || id FROM capture_annotation_sets WHERE capture_id = ? AND rev = 6 ORDER BY id", id)
	if len(both) != 2 || strings.Fields(both[0])[0] != strings.Fields(both[1])[0] || strings.Fields(both[0])[1] == strings.Fields(both[1])[1] {
		t.Fatalf("both replicas' sets: %v", both)
	}
	win := pointers[0]
	if Merge(pointers[0], pointers[1]) == TakeRemote {
		win = pointers[1]
	}
	var ptr SetPointer
	if err := json.Unmarshal([]byte(win.Value), &ptr); err != nil || ptr.Rev != 6 {
		t.Fatalf("the pointer picked: %s %v", win.Value, err)
	}
	if _, err := s.db.Exec("UPDATE captures SET annotations = ? WHERE id = ?", win.Value, id); err != nil {
		t.Fatal(err)
	}
	if c := captureOf(t, s, id); c.Set != ptr || len(c.Annotations) != 2 || c.Annotations[0].Note != "the same on both" {
		t.Fatalf("the set the pointer names: %+v", c)
	}
}

// Two replicas' edits of the same rev: their sets both kept (the sets'
// and the versions' rows a log, every one kept), the pointer one scalar
// whose later at wins, then the greater actor, then the greater op id,
// so a merge picks one edit's set whole and never a mix.
func TestAnnotationsMergeAsOneScalar(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, pngOf(t, 8, 8, 13))
	snap := filepath.Join(t.TempDir(), "replica.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	os.MkdirAll(dir, 0o700)
	b, _ := os.ReadFile(snap)
	os.WriteFile(filepath.Join(dir, File), b, 0o600)
	replica := openStore(t, dir)
	var pointers [2]ChangeRow
	for i, st := range []*Store{s, replica} {
		edits := kept(captureOf(t, st, id))
		edits[0].Note = fmt.Sprintf("replica %d", i)
		rc, err := st.EditCapture(ctx, Op{Actor: fmt.Sprintf("r%d", i), ID: "same"}, digestOf(t, edits, nil), id, 1, edits)
		want(t, rc, err, StatusDone, "")
		for _, r := range changesOf(t, st, fmt.Sprintf("r%d", i), "same") {
			if r.Field == "annotations" {
				pointers[i] = r
			}
			if r.Field == "sets" || r.Field == "versions" {
				if got := Class(EntityCapture, r.Field); got != ClassLog || Merge(r, ChangeRow{Entity: EntityCapture, Field: r.Field, Op: OpAdd, Actor: "x", OpID: "y"}) != KeepBoth {
					t.Fatalf("a %s row merges as %s", r.Field, got)
				}
			}
		}
	}
	a, b2 := pointers[0], pointers[1]
	if Class(EntityCapture, "annotations") != ClassScalar || a.Value == b2.Value {
		t.Fatalf("the pointers: %s %s", a.Value, b2.Value)
	}
	at := momentAt
	for _, c := range []struct {
		local, remote ChangeRow
		want          Winner
	}{
		{row(EntityCapture, "annotations", OpSet, at, "a", "x", 0), row(EntityCapture, "annotations", OpSet, at.Add(time.Second), "a", "w", 0), TakeRemote},
		{row(EntityCapture, "annotations", OpSet, at.Add(time.Second), "a", "x", 0), row(EntityCapture, "annotations", OpSet, at, "b", "y", 0), KeepLocal},
		{row(EntityCapture, "annotations", OpSet, at, "a", "z", 0), row(EntityCapture, "annotations", OpSet, at, "b", "x", 0), TakeRemote},
		{row(EntityCapture, "annotations", OpSet, at, "a", "x", 0), row(EntityCapture, "annotations", OpSet, at, "a", "y", 0), TakeRemote},
	} {
		if got := Merge(c.local, c.remote); got != c.want {
			t.Errorf("%+v against %+v: %v, want %v", c.local, c.remote, got, c.want)
		}
		if got := Merge(c.remote, c.local); (got == TakeRemote) == (c.want == TakeRemote) {
			t.Errorf("the mirror of %+v: %v", c.local, got)
		}
	}
	// Each replica's set is whole: its pointer names a set whose members
	// are that edit's versions alone.
	for i, st := range []*Store{s, replica} {
		if c := captureOf(t, st, id); c.Annotations[0].Note != fmt.Sprintf("replica %d", i) || c.Annotations[1].Note != "here" {
			t.Fatalf("replica %d: %+v", i, c.Annotations)
		}
	}
}

// A capture deleted unsent: its tombstone, nothing it alone held (its
// file's row, its versions and sets), its file collected by the delete
// unless another live capture holds it, gone from SnapshotHashes, and no
// read giving its words; a second delete not_found.
func TestADeletedUnsentCaptureReleasesItsImage(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	shared, own := pngOf(t, 8, 8, 14), pngOf(t, 8, 8, 15)
	n, f := testCapture(true)
	a := mustCreate(t, s, "a", n, f, shared)
	b := mustCreate(t, s, "b", n, f, shared)
	c := mustCreate(t, s, "c", n, f, own)
	for _, id := range []string{a, c} {
		rc, err := s.DeleteCapture(ctx, testOp("delete-"+id), id)
		want(t, rc, err, StatusDone, "")
	}
	rc, err := s.DeleteCapture(ctx, testOp("again"), a)
	want(t, rc, err, StatusFailed, RefusalNotFound)
	for _, table := range []string{"capture_annotations", "capture_annotation_sets", "capture_files"} {
		if n := count(t, s, "SELECT COUNT(*) FROM "+table+" WHERE capture_id IN (?, ?)", a, c); n != 0 {
			t.Fatalf("%d rows of %s left", n, table)
		}
	}
	if !exists(t, contentAt(s, sumOf(shared))) || exists(t, contentAt(s, sumOf(own))) {
		t.Fatal("the deletes kept the wrong files")
	}
	snap := filepath.Join(t.TempDir(), "snap.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if sums, err := SnapshotHashes(ctx, snap); err != nil || !slices.Equal(sums, []string{sumOf(shared)}) {
		t.Fatalf("the snapshot's hashes: %v %v", sums, err)
	}
	got := captureOf(t, s, a)
	words, _ := json.Marshal(got)
	if got.DeletedAt.IsZero() || got.URL != "" || got.Title != "" || got.Annotations != nil || strings.Contains(string(words), "Broken") || strings.Contains(string(words), "127.0.0.1") {
		t.Fatalf("a deleted capture read: %s", words)
	}
	if _, err := s.Document(ctx, a, got.Rev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted capture's document: %v", err)
	}
	if _, _, err := s.OpenCaptureImage(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted capture's image: %v", err)
	}
	page, err := s.Captures(ctx, CaptureFilter{}, "", MaxCapturePage)
	if err != nil || len(page.Captures) != 1 || page.Captures[0].ID != b {
		t.Fatalf("the list: %+v %v", page, err)
	}
	edits := kept(captureOf(t, s, b))
	rc, err = reedit(t, s, "edit-deleted", a, got.Rev, edits)
	want(t, rc, err, StatusFailed, RefusalNotFound)
}

// The sweep keeps every file a capture_files row names, whatever its age,
// and only while the row is there: a live capture's, and a row of another
// holder (a send's, which PR-1b writes) after its capture's delete.
func TestTheSweepKeepsACapturesFiles(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	s.sweepGrace = 0
	live, held := pngOf(t, 8, 8, 16), pngOf(t, 8, 8, 17)
	n, f := testCapture(true)
	mustCreate(t, s, "live", n, f, live)
	gone := mustCreate(t, s, "gone", n, f, held)
	if _, err := s.db.Exec("INSERT INTO capture_files (capture_id, sha256, holder) VALUES (?, ?, ?)", gone, sumOf(held), newID()); err != nil {
		t.Fatal(err)
	}
	rc, err := s.DeleteCapture(ctx, testOp("d"), gone)
	want(t, rc, err, StatusDone, "")
	if err := s.sweep(ctx, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !exists(t, contentAt(s, sumOf(live))) || !exists(t, contentAt(s, sumOf(held))) {
		t.Fatal("the sweep took a file a row holds")
	}
	if _, err := s.db.Exec("DELETE FROM capture_files WHERE capture_id = ?", gone); err != nil {
		t.Fatal(err)
	}
	if err := s.sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if exists(t, contentAt(s, sumOf(held))) {
		t.Fatal("the sweep kept a file nothing holds")
	}
}

// SnapshotHashes names every capture_files hash of a copy, and reads a
// version 4 copy, which has none.
func TestSnapshotHashesNameCaptureFiles(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 18)
	n, f := testCapture(true)
	mustCreate(t, s, "c1", n, f, img)
	snap := filepath.Join(t.TempDir(), "snap.sqlite")
	if err := s.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if sums, err := SnapshotHashes(ctx, snap); err != nil || !slices.Equal(sums, []string{sumOf(img)}) {
		t.Fatalf("the hashes: %v %v", sums, err)
	}
	v4 := filepath.Join(t.TempDir(), "v4.sqlite")
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v4.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(v4, b, 0o600)
	if _, err := SnapshotHashes(ctx, v4); err != nil {
		t.Fatalf("a version 4 copy: %v", err)
	}
}

// fixtureV4 is a copy of testdata/dispatch-v4.sqlite in a store directory
// of the test's: a version 4 database main's version 4 code wrote (the
// version 3 fixture brought to 4 and given an imported ticket, its pages
// then compacted at 1 KiB).
func fixtureV4(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dispatch-v4.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	dir := storeDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, File), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A version 4 database is brought to version 5 at open: the seven tables
// and five indexes made, empty, every row of every table as it was, no
// migrate row, the schema a fresh store's; a capture is then made; and a
// binary that writes version 4 opens it read-only.
func TestMigrationToCapturesKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	dir := fixtureV4(t)
	raw := openRaw(t, filepath.Join(dir, File))
	var version int
	if err := raw.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != captureSchema-1 {
		t.Fatalf("the fixture is version %d %v", version, err)
	}
	before, cols := map[string][]string{}, map[string][]string{}
	for _, table := range tables {
		if slices.Contains(version5Tables, table) || table == "tickets_fts" || table == "schema_version" {
			continue
		}
		cols[table] = columnsOf(t, raw, table)
		before[table] = tableRows(t, raw, table, cols[table])
	}
	if len(before["tickets"]) != 4 || len(before["store_instance"]) != 1 || len(before["changes"]) == 0 {
		t.Fatalf("the fixture: %d tickets", len(before["tickets"]))
	}
	raw.Close()
	s, err := OpenOptions(dir, Options{Systems: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ro, why := s.ReadOnly(); ro {
		t.Fatalf("read-only: %s", why)
	}
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != captureSchema {
		t.Fatalf("version %d %v", version, err)
	}
	for table, c := range cols {
		if got := tableRows(t, s.db, table, c); !slices.Equal(got, before[table]) {
			t.Errorf("%s's rows changed", table)
		}
	}
	for _, table := range version5Tables {
		if n := countOf(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%d rows in %s", n, table)
		}
	}
	if n := count(t, s, "SELECT COUNT(*) FROM changes WHERE op = ?", OpMigrate); n != 0 {
		t.Fatalf("%d migrate rows", n)
	}
	if got := texts(t, s, "SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'capture%' ORDER BY name"); !slices.Equal(got,
		[]string{"capture_files_sha256", "capture_send_outcomes_target", "capture_sends_capture_created", "captures_attempt", "captures_created"}) {
		t.Fatalf("the version 5 indexes: %v", got)
	}
	fresh := openStore(t, storeDir(t))
	if got, want := schemaShape(t, s.db), schemaShape(t, fresh.db); !slices.Equal(got, want) {
		t.Fatalf("the migrated schema is not a fresh store's:\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	n, f := testCapture(true)
	id := mustCreate(t, s, "capture-1", n, f, pngOf(t, 8, 8, 19))
	if _, err := s.Ticket(ctx, "CND-4"); err != nil {
		t.Fatalf("the imported ticket: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	older, err := openWith(dir, func(s *Store) { s.writes = captureSchema - 1 })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { older.Close() })
	if ro, why := older.ReadOnly(); !ro || !strings.Contains(why, fmt.Sprintf("version %d, written by a newer Conductor", captureSchema)) {
		t.Fatalf("read-only %v: %s", ro, why)
	}
	rc, err := create(t, older, "capture-2", n, f, nil)
	want(t, rc, err, StatusFailed, RefusalReadOnly)
	if c, err := older.Capture(ctx, id); err != nil || c.ID != id {
		t.Fatalf("a read of the newer database: %+v %v", c, err)
	}
}

// Every column of the seven tables has its class: a capture's page,
// origin and image immutable, its pointer and its tombstone scalars, its
// versions' and sets' rows and a send's outcomes and cancels logs, the
// files held a set, every column of a row of those tables immutable, and
// rev a replica's own count.
func TestCaptureFieldClasses(t *testing.T) {
	s := openStore(t, storeDir(t))
	entityOf := map[string]string{"captures": EntityCapture, "capture_sends": "capture_send"}
	for _, table := range version5Tables {
		entity := table
		if e, ok := entityOf[table]; ok {
			entity = e
		}
		for _, col := range columnsOf(t, s.db, table) {
			got := Class(entity, col)
			want := ClassImmutable
			switch {
			case table == "captures" && col == "rev":
				want = ""
			case table == "captures" && (col == "annotations" || col == "deleted_at"):
				want = ClassScalar
			}
			if got != want {
				t.Errorf("Class(%s, %s) = %q, want %q", entity, col, got, want)
			}
		}
	}
	for _, c := range []struct {
		entity, field string
		want          FieldClass
	}{
		{EntityCapture, "versions", ClassLog}, {EntityCapture, "sets", ClassLog}, {EntityCapture, "files", ClassSet},
		{"capture_send", "outcomes", ClassLog}, {"capture_send", "cancels", ClassLog}, {"capture_send", "files", ClassSet},
		{EntityCapture, "nothing", ""},
	} {
		if got := Class(c.entity, c.field); got != c.want {
			t.Errorf("Class(%s, %s) = %q, want %q", c.entity, c.field, got, c.want)
		}
	}
}

// A capture's resend is answered before anything is uploaded or written:
// the same id and digest the same receipt (Repeat), another digest
// op_conflict; once the receipt is pruned, the capture's own fingerprint
// answers it, done with its id, or op_conflict. An edit's resend is its
// receipt's.
func TestReplayAnswersACaptureResend(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 20)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, img)
	rc, err := create(t, s, "c1", n, f, img)
	if err != nil || !rc.Repeat || rc.ID != id || rc.Status != StatusDone {
		t.Fatalf("a resend: %+v %v", rc, err)
	}
	other := n
	other.Title, other.Annotations = "Another", slices.Clone(n.Annotations)
	moment(&other)
	rc, err = create(t, s, "c1", other, f, img)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if got, found, err := s.Replay(ctx, testOp("c1"), IntentCaptureCreate, captureArgs{Received: digestOf(t, n, img)}); err != nil || !found || got.ID != id {
		t.Fatalf("Replay: %+v %v %v", got, found, err)
	}
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	rc, err = create(t, s, "c1", n, f, img)
	if err != nil || !rc.Repeat || rc.ID != id || rc.Status != StatusDone {
		t.Fatalf("a resend, its receipt pruned: %+v %v", rc, err)
	}
	rc, err = create(t, s, "c1", other, f, img)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if n := count(t, s, "SELECT COUNT(*) FROM captures"); n != 1 {
		t.Fatalf("%d captures", n)
	}
	noUploads(t, s)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "edited"
	first, err := reedit(t, s, "e1", id, 1, edits)
	want(t, first, err, StatusDone, "")
	again, err := reedit(t, s, "e1", id, 1, edits)
	if err != nil || !again.Repeat || again.Rev != first.Rev {
		t.Fatalf("an edit's resend: %+v %v", again, err)
	}
	// Once the receipts are pruned, one request has one answer: an edit
	// and a delete answered only by their own kind and fingerprint, else
	// op_conflict, nothing performed.
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	again, err = reedit(t, s, "e1", id, 1, edits)
	if err != nil || !again.Repeat || again.Status != StatusDone {
		t.Fatalf("an edit's resend, its receipt pruned: %+v %v", again, err)
	}
	other2 := kept(captureOf(t, s, id))
	other2[0].Note = "second"
	rc, err = reedit(t, s, "e1", id, 2, other2)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.DeleteCapture(ctx, testOp("c1"), id)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = s.DeleteCapture(ctx, testOp("e1"), id)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if c := captureOf(t, s, id); c.Rev != 2 || c.Annotations[0].Note != "edited" || !c.DeletedAt.IsZero() {
		t.Fatalf("a pruned id's other request was performed: %+v", c)
	}
	rc, err = s.DeleteCapture(ctx, testOp("d1"), id)
	want(t, rc, err, StatusDone, "")
	n2, f2 := testCapture(false)
	second := mustCreate(t, s, "c2", n2, f2, nil)
	if _, err := s.db.Exec("DELETE FROM ops"); err != nil {
		t.Fatal(err)
	}
	if rc, err = s.DeleteCapture(ctx, testOp("d1"), id); err != nil || !rc.Repeat || rc.Status != StatusDone {
		t.Fatalf("a delete's resend, its receipt pruned: %+v %v", rc, err)
	}
	rc, err = s.DeleteCapture(ctx, testOp("d1"), second)
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	rc, err = reedit(t, s, "d1", second, 1, kept(captureOf(t, s, second)))
	want(t, rc, err, StatusFailed, RefusalOpConflict)
	if again, err = reedit(t, s, "e1", id, 1, edits); err != nil || !again.Repeat || again.Status != StatusDone {
		t.Fatalf("an edit's resend after its capture's delete: %+v %v", again, err)
	}
	if c := captureOf(t, s, second); c.Rev != 1 || !c.DeletedAt.IsZero() {
		t.Fatalf("the second capture: %+v", c)
	}
	if _, err := s.CreateCapture(ctx, testOp("c9"), "not a digest", n, f, nil); !errors.Is(err, ErrBadReceived) {
		t.Fatalf("a bad digest: %v", err)
	}
}
