package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/ext/exttest"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// The captures of a preview (Program item PV1, docs/plans/browser-
// evidence.md), over HTTP. Each test's preview is a run of its own over a
// repository of its own (previewEnv), ended before its folders go.

var captureOps atomic.Int64

// body is a capture of the attempt's page at the moment label was read,
// its annotations each observed at it.
func (p *previewed) body(t *testing.T, label map[string]any, reports ...proto.ElementReport) captureCreateBody {
	t.Helper()
	readAt, err := time.Parse(time.RFC3339Nano, label["readAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	vp := dispatch.Viewport{Mode: "fit", Width: 800, Height: 600, Scale: 1, DPR: 1, ColorScheme: "light", UserAgent: "test"}
	b := captureCreateBody{OpID: fmt.Sprintf("cap-%d", captureOps.Add(1)), From: captureFrom{Run: p.run, Task: "build", Attempt: 1},
		NewCapture: dispatch.NewCapture{Via: "web", URL: p.page, Title: "Page", Viewport: vp, Epoch: 3, SelectedAt: readAt,
			Label:    dispatch.TreeRead{Commit: label["commit"].(string), Content: label["content"].(string), Changed: int(label["changed"].(float64)), ReadAt: readAt},
			ImageHow: "none", ImageWhy: "the test takes none"}}
	for _, r := range reports {
		b.Annotations = append(b.Annotations, observedAt(b, r))
	}
	return b
}

// observedAt is an element annotation of r observed at b's moment.
func observedAt(b captureCreateBody, r proto.ElementReport) captureAnnotation {
	return captureAnnotation{Kind: "element", Note: "Broken", Rect: dispatch.Rect{X: 1, Y: 1, Width: 2, Height: 2}, Element: &r,
		Observed: &dispatch.Observed{At: b.SelectedAt, URL: b.URL, Title: b.Title, Viewport: b.Viewport, Epoch: b.Epoch, Revision: b.Revision,
			Label: dispatch.RevisionLabel{Commit: b.Label.Commit, Content: b.Label.Content}}}
}

// moment observes every element annotation of b at b's moment again.
func moment(b *captureCreateBody) {
	for i, a := range b.Annotations {
		if a.Kind == "element" && a.Element != nil {
			b.Annotations[i] = observedAt(*b, *a.Element)
			b.Annotations[i].Note = a.Note
		}
	}
}

// button is a report of a plain button.
func button() proto.ElementReport {
	return proto.ElementReport{Tag: "button", Classes: "save primary", Name: "Save", Text: "Save", Selector: "#save",
		Box: proto.ElementBox{X: 10, Y: 10, Width: 80, Height: 20}, Attributes: [][2]string{{"type", "button"}},
		Hit: proto.ElementHit{Self: true, Selector: "#save", Tag: "button"}, HTML: "<button>Save</button>",
		Ancestors: []proto.ElementAncestor{{Tag: "div", Classes: "bar wide"}}, Styles: [][2]string{{"display", "block"}}}
}

// postCapture creates a capture of body, with the screenshot when one is
// given, as the admin.
func postCapture(t *testing.T, e *testEnv, body captureCreateBody, png []byte) (int, map[string]any) {
	t.Helper()
	status, data := postCaptureRaw(t, e, body, png)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return status, out
}

// postCaptureRaw is postCapture's status and body as sent.
func postCaptureRaw(t *testing.T, e *testEnv, body captureCreateBody, png []byte) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormField("capture")
	part.Write(encodeNoHTML(t, body))
	if png != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="image"; filename="shot.png"`)
		h.Set("Content-Type", "image/png")
		w, _ := mw.CreatePart(h)
		w.Write(png)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.http.URL+"/api/captures", &buf)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// created posts body and returns the capture made, failing otherwise.
func created(t *testing.T, e *testEnv, body captureCreateBody, png []byte) map[string]any {
	t.Helper()
	status, out := postCapture(t, e, body, png)
	if status != http.StatusCreated {
		t.Fatalf("the create: %d %v", status, out)
	}
	return out["capture"].(map[string]any)
}

// captureRefused posts body and fails unless it is answered status and code.
func captureRefused(t *testing.T, e *testEnv, body captureCreateBody, png []byte, status int, code string) map[string]any {
	t.Helper()
	got, out := postCapture(t, e, body, png)
	if got != status || codeOf(out) != code {
		t.Fatalf("answered %d %v, want %d %s", got, out, status, code)
	}
	return out
}

func originOf(c map[string]any) map[string]any { return c["origin"].(map[string]any) }

// edited edits the capture id at rev 1 with one note, as the admin, and
// fails unless it is done.
func (p *previewed) edited(t *testing.T, id, note string) {
	t.Helper()
	edit := map[string]any{"opId": fmt.Sprintf("edit-%d", captureOps.Add(1)), "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": note}}}
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+"/edit", edit); status != http.StatusOK {
		t.Fatalf("the edit: %d %s", status, data)
	}
}

// hasSecret reports the files under dir that hold the secret, in any case.
func hasSecret(t *testing.T, dir, secret string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(bytes.ToLower(b), []byte(secret)) {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// keptNowhere fails the test when any file of Dispatch's store holds one
// of the secrets.
func (p *previewed) keptNowhere(t *testing.T, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), s); len(found) > 0 {
			t.Fatalf("a tree's secret is in %v", found)
		}
	}
}

// another launches a second preview beside p's, of its own crew, and
// returns it.
func (p *previewed) another(t *testing.T) *previewed {
	t.Helper()
	c := previewedCrew(p.e.root)
	c.ID, c.Name = "previewed-too", "Previewed too"
	q := launchPreview(t, p.e, c)
	if q.secret == p.secret {
		t.Fatal("the two trees have one secret")
	}
	return q
}

// What the page serves is its word, compared with the tree's at the save.
func TestTheServingRevisionIsThePagesWord(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	commit := l["commit"].(string)
	other := strings.Repeat("a", 40)
	for _, c := range []struct{ revision, state string }{{other, "stale"}, {commit, "declared"}, {"", "unconfirmed"}} {
		b := p.body(t, l, button())
		b.Revision = c.revision
		moment(&b)
		if s := originOf(created(t, p.e, b, nil))["serving"].(map[string]any); s["state"] != c.state || (c.revision != "" && s["page"] != c.revision) {
			t.Errorf("the page declaring %q: %v, want %s", c.revision, s, c.state)
		}
	}
	writeIn(t, p.dir, "README", "dirty\n")
	dirty := p.label(t)
	b := p.body(t, dirty, button())
	b.Revision = commit
	moment(&b)
	if s := originOf(created(t, p.e, b, nil))["serving"].(map[string]any); s["state"] != "unconfirmed" {
		t.Errorf("a commit alone over a tree with changes: %v", s)
	}
}

// A create, its reply lost, is answered by its receipt after the preview
// has closed and its secret is gone; so is an edit; another body under
// the same opId is op_conflict; the store holds the secret nowhere.
func TestALostReplyAfterTheTreeEndedIsReplayed(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	b.URL = p.page + "#token=" + p.secret
	moment(&b)
	b.Annotations[0].Note = "the key " + p.secret
	c := created(t, p.e, b, nil)
	id := c["id"].(string)
	edit := map[string]any{"opId": "edit-1", "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "pasted " + strings.ToUpper(p.secret)}}}
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+"/edit", edit); status != http.StatusOK {
		t.Fatalf("the edit: %d %s", status, data)
	}
	if resp, out := p.e.do("POST", "/api/runs/"+p.run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	if status, _, _ := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil); status != http.StatusNotFound {
		t.Fatalf("the label after the stop: %d", status)
	}
	status, out := postCapture(t, p.e, b, nil)
	if status != http.StatusOK || receiptOf(t, out)["repeat"] != true || receiptOf(t, out)["id"] != id {
		t.Fatalf("the create again: %d %v", status, out)
	}
	if status, data, out := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+"/edit", edit); status != http.StatusOK || receiptOf(t, out)["repeat"] != true {
		t.Fatalf("the edit again: %d %s", status, data)
	}
	b.Title = "another"
	if status, out := postCapture(t, p.e, b, nil); status != http.StatusConflict || codeOf(out) != "op_conflict" {
		t.Fatalf("another body under the opId: %d %v", status, out)
	}
	p.keptNowhere(t, p.secret)
}

// The revision is the server's: a client's commit and content are kept
// as the drift, never as the capture's.
func TestACaptureTakesTheServersRevision(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	b := p.body(t, l, button())
	b.Label.Commit, b.Label.Content = strings.Repeat("b", 40), strings.Repeat("c", 40)
	moment(&b)
	o := originOf(created(t, p.e, b, nil))
	if o["commit"] != l["commit"] || o["content"] != l["content"] || o["drift"].(map[string]any)["commit"] != strings.Repeat("b", 40) {
		t.Fatalf("the origin: %v (the tree's %v)", o, l)
	}
}

// driftOf creates a capture of label (read before change) once change has
// run, and returns its origin.
func (p *previewed) driftOf(t *testing.T, change func()) (map[string]any, map[string]any) {
	t.Helper()
	l := p.label(t)
	change()
	return originOf(created(t, p.e, p.body(t, l, button()), nil)), l
}

// The tree changed between the selection and the save: the drift is what
// the pane read.
func TestTheTreeChangedBetweenSelectionAndSave(t *testing.T) {
	p := previewEnv(t, nil)
	o, l := p.driftOf(t, func() { writeIn(t, p.dir, "README", "moved\n") })
	if d, _ := o["drift"].(map[string]any); d == nil || d["content"] != l["content"] || o["content"] == l["content"] {
		t.Fatalf("the origin: %v", o)
	}
}

// A second edit of a file already changed is drift: the same commit and
// the same count.
func TestASecondEditOfADirtyFileIsDrift(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "README", "one\n")
	o, l := p.driftOf(t, func() { writeIn(t, p.dir, "README", "two\n") })
	if d, _ := o["drift"].(map[string]any); d == nil || o["commit"] != l["commit"] || o["changed"] != l["changed"] {
		t.Fatalf("the origin: %v (the selection's %v)", o, l)
	}
}

// Other paths of the same count are drift.
func TestOtherPathsOfTheSameCountAreDrift(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "a.txt", "a\n")
	o, l := p.driftOf(t, func() {
		os.Remove(filepath.Join(p.dir, "a.txt"))
		writeIn(t, p.dir, "b.txt", "b\n")
	})
	if d, _ := o["drift"].(map[string]any); d == nil || o["changed"] != l["changed"] || fmt.Sprint(o["changedPaths"]) != "[b.txt]" {
		t.Fatalf("the origin: %v (the selection's %v)", o, l)
	}
}

// A page of another origin than the attempt's preview is refused, naming
// both.
func TestACaptureOfAnotherOriginIsRefused(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t))
	b.URL = "http://127.0.0.1:1/"
	out := captureRefused(t, p.e, b, nil, http.StatusBadRequest, "invalid")
	if m := apiMessage(out); !strings.Contains(m, "http://127.0.0.1:1") || !strings.Contains(m, strings.TrimSuffix(p.page, "/")) {
		t.Fatalf("the words: %q", m)
	}
}

// A page whose host is a tree's secret, as written or percent-encoded, or
// is not plain ASCII, is refused in words that quote the preview's origin
// and never the page's host.
func TestAnOriginRefusalQuotesThePreviewAlone(t *testing.T) {
	p := previewEnv(t, nil)
	var encoded strings.Builder
	for _, c := range []byte(p.secret) {
		fmt.Fprintf(&encoded, "%%%02X", c)
	}
	preview := strings.TrimSuffix(p.page, "/")
	for name, page := range map[string]string{
		"the secret as the host":      "http://" + p.secret + ":5173/",
		"the secret percent-encoded":  "http://" + encoded.String() + ":5173/",
		"a host that is not ASCII":    "http://bücher.example:5173/",
		"a host of another script":    "http://xn--bcher-kva.example:5173/",
		"the secret in another's URL": "http://" + p.secret + ".example:5173/",
	} {
		b := p.body(t, p.label(t), button())
		b.URL = page
		moment(&b)
		status, data := postCaptureRaw(t, p.e, b, nil)
		if status != http.StatusBadRequest || strings.Contains(strings.ToLower(string(data)), p.secret) || strings.Contains(string(data), "bücher") {
			t.Errorf("%s: %d %s", name, status, data)
		}
		if name == "a host that is not ASCII" && !strings.Contains(string(data), "the page is of another origin than this attempt's preview "+preview) {
			t.Errorf("%s: %s", name, data)
		}
	}
}

// No capture of an attempt whose preview is not open now: another
// attempt, a candidate's checkout, and the attempt once its run stopped.
func TestACaptureWithoutAnOpenPreviewIsRefused(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	other := b
	other.From.Attempt = 2
	captureRefused(t, p.e, other, nil, http.StatusNotFound, "no_preview")
	other.From.Attempt, other.From.Seq = 1, 1
	captureRefused(t, p.e, other, nil, http.StatusNotFound, "no_preview")
	if resp, out := p.e.do("POST", "/api/runs/"+p.run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	captureRefused(t, p.e, b, nil, http.StatusNotFound, "no_preview")
}

// sourcesOf creates a capture of one element with the hints and returns
// its sources.
func (p *previewed) sourcesOf(t *testing.T, hints ...proto.ElementHint) []any {
	t.Helper()
	r := button()
	r.Hints = hints
	c := created(t, p.e, p.body(t, p.label(t), r), nil)
	return c["annotations"].([]any)[0].(map[string]any)["sources"].([]any)
}

// A hint's file is made relative to the tree and looked for in it; one
// outside it, a .. path among them, is kept as reported.
func TestSourcesAreMadeRelativeAndChecked(t *testing.T) {
	p := previewEnv(t, nil)
	outside := filepath.Join(filepath.Dir(p.dir), "elsewhere.vue")
	dotdot := p.dir + "/../x.vue"
	got := p.sourcesOf(t, proto.ElementHint{How: "vue", File: filepath.Join(p.dir, "README"), Line: 3},
		proto.ElementHint{How: "vue", File: filepath.Join(p.dir, "gone.vue")}, proto.ElementHint{How: "vue", File: outside},
		proto.ElementHint{How: "attribute", File: dotdot})
	want := []string{"README found tracked", "gone.vue missing untracked", outside + " outside untracked", dotdot + " outside untracked"}
	for i, s := range got {
		m := s.(map[string]any)
		where := "missing"
		switch {
		case m["outside"] == true:
			where = "outside"
		case m["found"] == true:
			where = "found"
		}
		if g := fmt.Sprintf("%s %s %s", m["path"], where, m["class"]); i >= len(want) || g != want[i] {
			t.Errorf("source %d: %s, want %v", i, g, want)
		}
	}
}

// The app's tracked file comes first though a library's component under
// node_modules is innermost; the library's is vendor, after the untracked
// and the ignored.
func TestSourcesAreClassedAndTheAppsFileFirst(t *testing.T) {
	p := previewEnv(t, map[string]string{"app/Save.vue": "<template/>", ".gitignore": "dist/\n"})
	for _, f := range []string{"node_modules/lib/Btn.vue", "app/New.vue", "dist/x.vue"} {
		writeIn(t, p.dir, f, "x")
	}
	hint := func(f string) proto.ElementHint { return proto.ElementHint{How: "vue", File: filepath.Join(p.dir, f)} }
	got := p.sourcesOf(t, hint("node_modules/lib/Btn.vue"), hint("app/Save.vue"), hint("app/New.vue"), hint("dist/x.vue"))
	var order []string
	for _, s := range got {
		m := s.(map[string]any)
		order = append(order, fmt.Sprintf("%s:%s", m["path"], m["class"]))
	}
	if want := "[app/Save.vue:tracked app/New.vue:untracked dist/x.vue:ignored node_modules/lib/Btn.vue:vendor]"; fmt.Sprint(order) != want {
		t.Fatalf("the sources: %v, want %s", order, want)
	}
}

// readFixture reads a report fixture the bridge's vitest wrote.
func readFixture(t *testing.T, name string) (int, proto.ElementReport) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Bytes  int                 `json:"bytes"`
		Report proto.ElementReport `json:"report"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Bytes, f.Report
}

// The bridge's two fixtures, its fullest report at every field's bound and
// its largest-tag report (names of control characters, each made a space),
// measure what the bridge measured and are created; so is a report whose
// HTML, neighbourhood and an attribute hold CRLF, and every element the
// bridge answered on its three pages.
func TestTheBridgesFullestReportIsAccepted(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	for _, name := range []string{"fullest-report.json", "largest-tag-report.json"} {
		n, r := readFixture(t, name)
		if got := proto.PreviewMeasure(r); got != n || n > proto.MaxElementReport {
			t.Fatalf("%s measures %d, the bridge %d", name, got, n)
		}
		if status, out := postCapture(t, p.e, p.body(t, l, r), nil); status != http.StatusCreated {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	crlf := button()
	crlf.HTML, crlf.Neighbourhood = "<p>one\r\ntwo</p>", "<div>\r\n<p>one\r\ntwo</p>\r\n</div>"
	crlf.Attributes = append(crlf.Attributes, [2]string{"title", "one\r\ntwo"})
	c := created(t, p.e, p.body(t, l, crlf), nil)
	kept := c["annotations"].([]any)[0].(map[string]any)["element"].(map[string]any)
	if kept["html"] != "<p>one\n\ntwo</p>" || fmt.Sprint(kept["attributes"]) != "[[type button] [title one\n\ntwo]]" {
		t.Errorf("the CRLF report kept: %v", kept)
	}
	for _, page := range []string{"plain-page-messages.json", "large-page-messages.json", "fields-page-messages.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", page))
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Messages []struct {
				Element *proto.ElementReport `json:"element"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		for i, m := range f.Messages {
			if m.Element != nil {
				if status, out := postCapture(t, p.e, p.body(t, l, *m.Element), nil); status != http.StatusCreated {
					t.Errorf("%s, message %d: %d %v", page, i, status, out)
				}
			}
		}
	}
}

// Every element of a capture was selected at its moment: one observed at
// another URL, viewport, scroll, epoch or label is refused.
func TestACaptureIsOneMoment(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	for name, change := range map[string]func(o *dispatch.Observed){
		"url":      func(o *dispatch.Observed) { o.URL = p.page + "other" },
		"viewport": func(o *dispatch.Observed) { o.Viewport.Width = 801 },
		"scroll":   func(o *dispatch.Observed) { o.Viewport.ScrollY = 40 },
		"epoch":    func(o *dispatch.Observed) { o.Epoch = 4 },
		"label":    func(o *dispatch.Observed) { o.Label.Content = strings.Repeat("d", 40) },
	} {
		b := p.body(t, l, button(), button())
		o := *b.Annotations[1].Observed
		change(&o)
		b.Annotations[1].Observed = &o
		out := captureRefused(t, p.e, b, nil, http.StatusBadRequest, "invalid")
		if !strings.Contains(apiMessage(out), "annotation 2 was selected after the page changed") {
			t.Errorf("%s: %v", name, out)
		}
	}
}

// countTreeGit counts the git commands of the trees' reads from now on.
func countTreeGit(t *testing.T) *atomic.Int64 {
	t.Helper()
	n := &atomic.Int64{}
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		n.Add(1)
		return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
	}))
	return n
}

// eachString calls f with a setter of each string v holds, in one order,
// however deep: the walk of a request's members.
func eachString(v reflect.Value, f func(set func(string))) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			f(v.SetString)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			eachString(v.Elem(), f)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			eachString(v.Field(i), f)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			eachString(v.Index(i), f)
		}
	}
}

// A request the store's own check refuses is refused before any tree is
// read, whatever member breaks it, and the refusal is the store's (its
// receipt), answered the same when it is sent again. The walk sets each
// string member of a create (the client's words, the annotations, every
// text of an element's report and of its moment, so a member added later
// is walked too) past every bound the store has; then the rules no text
// carries: a count, a kind, the moment, a region without a screenshot, a
// number, and a class list of 100 KiB of spaces, which the store refuses
// for the report's measure as the bridge cut it; a region past the
// screenshot's size (its header read); and what the store refuses of its
// own rows: a retake of no capture, of a deleted one and of a link's, a
// selection that names no label or a commit alone and no read's time, and
// a capture past the live captures' bound.
func TestARequestTheStoreRefusesReadsNoTree(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	gone := created(t, p.e, p.body(t, l, button()), nil)["id"].(string)
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+gone+"/delete", map[string]any{"opId": "gone-1"}); status != http.StatusOK {
		t.Fatalf("the delete: %d %s", status, data)
	}
	link := p.body(t, l)
	link.From, link.Label = captureFrom{URL: p.page}, dispatch.TreeRead{}
	linked := created(t, p.e, link, nil)["id"].(string)
	full := func() captureCreateBody {
		r := button()
		r.Hints = []proto.ElementHint{{How: "vue", File: "src/Save.vue", Component: "Save"}}
		r.Cut = []string{"neighbourhood"}
		r.State.AriaDisabled, r.Role, r.ID, r.Neighbourhood = "false", "button", "save", "<div/>"
		b := p.body(t, l, r)
		b.Revision, b.RetakeOf = l["commit"].(string), ""
		moment(&b)
		return b
	}
	type refusal struct {
		name  string
		set   func(b *captureCreateBody)
		image []byte
	}
	var cases []refusal
	members := 0
	for k := 0; ; k++ {
		b, i, found := full(), 0, false
		eachString(reflect.ValueOf(&b.NewCapture).Elem(), func(set func(string)) {
			if i == k {
				found = true
			}
			i++
		})
		eachString(reflect.ValueOf(&b.Annotations).Elem(), func(set func(string)) {
			if i == k {
				found = true
			}
			i++
		})
		if !found {
			break
		}
		members++
		cases = append(cases, refusal{fmt.Sprintf("string member %d", k), func(b *captureCreateBody) {
			i := 0
			past := func(set func(string)) {
				if i == k {
					set(strings.Repeat("a", 100<<10))
				}
				i++
			}
			eachString(reflect.ValueOf(&b.NewCapture).Elem(), past)
			eachString(reflect.ValueOf(&b.Annotations).Elem(), past)
		}, nil})
	}
	if members < 40 {
		t.Fatalf("the walk found %d string members", members)
	}
	cases = append(cases,
		refusal{"21 annotations", func(b *captureCreateBody) {
			b.Annotations = slices.Repeat(b.Annotations[:1], dispatch.MaxCaptureAnnotations+1)
		}, nil},
		refusal{"a kind of none", func(b *captureCreateBody) { b.Annotations[0].Kind = "circle" }, nil},
		refusal{"an element of no moment", func(b *captureCreateBody) { b.Annotations[0].Observed = nil }, nil},
		refusal{"an element of another moment", func(b *captureCreateBody) { b.Annotations[0].Observed.Epoch++ }, nil},
		refusal{"a region with no screenshot", func(b *captureCreateBody) {
			b.Annotations = []captureAnnotation{{Kind: "region", Note: "here", Rect: dispatch.Rect{Width: 1, Height: 1}}}
		}, nil},
		refusal{"a negative viewport", func(b *captureCreateBody) { b.Viewport.Width = -1; moment(b) }, nil},
		refusal{"a motion past the bridge's", func(b *captureCreateBody) { b.Annotations[0].Element.State.Motion = dispatch.MaxHintLine + 1 }, nil},
		refusal{"a class list of 100 KiB of spaces", func(b *captureCreateBody) {
			b.Annotations[0].Element.Classes = strings.Repeat(" ", 100<<10)
		}, nil},
		refusal{"a region past the screenshot's size", func(b *captureCreateBody) {
			*b = withImage(*b)
			b.Annotations = append(b.Annotations, captureAnnotation{Kind: "region", Note: "here", Rect: dispatch.Rect{X: 2, Y: 2, Width: 3, Height: 1}})
		}, pngOf(t, 4, 4)},
		refusal{"a retake of no capture", func(b *captureCreateBody) { b.RetakeOf = "0123456789abcdef" }, nil},
		refusal{"a retake of a deleted capture", func(b *captureCreateBody) { b.RetakeOf = gone }, nil},
		refusal{"a retake of a link's capture", func(b *captureCreateBody) { b.RetakeOf = linked }, nil},
		// A selection that names no content is never the tree's: its
		// read's time is required whatever the tree holds.
		refusal{"a selection of no label and no time", func(b *captureCreateBody) { b.Label = dispatch.TreeRead{}; moment(b) }, nil},
		refusal{"a selection of a commit alone and no time", func(b *captureCreateBody) {
			b.Label = dispatch.TreeRead{Commit: b.Label.Commit}
			moment(b)
		}, nil},
		// Last: every create after it is refused by the bound.
		refusal{"a capture past the live captures' bound", func(b *captureCreateBody) { fillCaptures(t, p.e, linked) }, nil},
	)
	reads := countTreeGit(t)
	for _, c := range cases {
		b := full()
		c.set(&b)
		status, out := postCapture(t, p.e, b, c.image)
		again, outAgain := postCapture(t, p.e, b, c.image)
		rc, _ := out["receipt"].(map[string]any)
		rcAgain, _ := outAgain["receipt"].(map[string]any)
		if (status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge) || rc == nil || rc["status"] != "failed" ||
			again != status || codeOf(outAgain) != codeOf(out) || rcAgain == nil || rcAgain["repeat"] != true {
			t.Errorf("%s: %d %v; again %d %v", c.name, status, out, again, outAgain)
		}
		if c.name == "a class list of 100 KiB of spaces" && (codeOf(out) != "bound" || !strings.Contains(apiMessage(out), "as the bridge measures it")) {
			t.Errorf("%s: %v", c.name, out)
		}
		if (c.name == "a region past the screenshot's size" && !strings.Contains(apiMessage(out), "not within the 4 by 4 screenshot")) ||
			(c.name == "a capture past the live captures' bound" && codeOf(out) != "bound") {
			t.Errorf("%s: %v", c.name, out)
		}
		if n := reads.Load(); n != 0 {
			t.Fatalf("%s: %d git commands read a tree", c.name, n)
		}
	}
}

// pngOf is a w by h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withChunk is the PNG b with a chunk of the type and data after its
// header's.
func withChunk(b []byte, typ string, data []byte) []byte {
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	chunk = append(append(chunk, typ...), data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	at := 8 + 25
	return slices.Concat(b[:at], chunk, b[at:])
}

// withImage is b with a screenshot taken from the tab.
func withImage(b captureCreateBody) captureCreateBody {
	b.ImageHow, b.ImageWhy, b.ImageScale = "tab", "", 1
	return b
}

// The tree's secret is in no byte the store keeps, whichever string of a
// capture carried it (the page's URL and an observed one keeping
// #token=, an href and the HTML, a note, a link fallback's frame src),
// nor in the document or a ticket made from it; the URLs keep its name.
func TestATreeSecretIsScrubbedFromEveryCapture(t *testing.T) {
	p := previewEnv(t, nil)
	keyed := p.page + "#token=" + p.secret
	b := p.body(t, p.label(t))
	b.URL = keyed
	r := button()
	r.Attributes = append(r.Attributes, [2]string{"href", keyed})
	r.HTML = `<a href="` + keyed + `">Save</a>`
	b.Annotations = []captureAnnotation{observedAt(b, r)}
	b.Annotations[0].Note = "the key is " + strings.ToUpper(p.secret)
	c := created(t, p.e, b, nil)
	// The fallback without the bridge: the frame's src, a region of the
	// screenshot alone.
	fb := withImage(p.body(t, p.label(t)))
	fb.URL = keyed
	fb.Annotations = []captureAnnotation{{Kind: "region", Note: p.secret, Rect: dispatch.Rect{X: 0, Y: 0, Width: 1, Height: 1}}}
	created(t, p.e, fb, pngOf(t, 2, 2))
	if c["url"] != p.page+"#token="+treeSecretName {
		t.Fatalf("the capture's URL: %v", c["url"])
	}
	ds := p.e.srv.dispatch
	ctx := t.Context()
	doc, err := ds.Document(ctx, c["id"].(string), 1)
	if err != nil || bytes.Contains(bytes.ToLower(doc), []byte(p.secret)) || !bytes.Contains(doc, []byte(treeSecretName)) {
		t.Fatalf("the document: %v %s", err, doc)
	}
	if rc, err := ds.CreateProject(ctx, dispatch.Op{Actor: "admin", ID: "proj-1"}, dispatch.NewProject{Key: "CAP", Name: "Captures"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the project: %v %v", rc, err)
	}
	rc, err := ds.SendCapture(ctx, dispatch.Op{Actor: "admin", ID: "send-1"}, strings.Repeat("e", 64), c["id"].(string), 1,
		dispatch.Destination{Kind: "ticket", Project: "CAP", Title: "From a capture"}, dispatch.SendFacts{})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the send: %v %v", rc, err)
	}
	tk, err := ds.Ticket(ctx, rc.Key)
	if err != nil || strings.Contains(strings.ToLower(tk.Description), p.secret) {
		t.Fatalf("the ticket: %v %q", err, tk.Description)
	}
	p.keptNowhere(t, p.secret)
}

// A link's capture is scrubbed of the secret of every tree held now: its
// URL, the link's, an element's report and a note.
func TestALinksCaptureHoldsNoTreesSecret(t *testing.T) {
	p := previewEnv(t, nil)
	page := "http://example.test/?k=" + p.secret
	b := p.body(t, p.label(t))
	b.From, b.Label, b.URL = captureFrom{URL: page}, dispatch.TreeRead{}, page
	r := button()
	r.HTML = "<a href=\"" + page + "\">Save</a>"
	b.Annotations = []captureAnnotation{observedAt(b, r)}
	b.Annotations[0].Note = "pasted " + strings.ToUpper(p.secret)
	if c := created(t, p.e, b, nil); c["url"] != "http://example.test/?k="+treeSecretName {
		t.Fatalf("the link's capture: %v", c["url"])
	}
	p.keptNowhere(t, p.secret)
}

// An attempt's capture is scrubbed of another tree's secret too: in a
// note, and in a changed path's name the server read.
func TestACaptureHoldsNoOtherTreesSecret(t *testing.T) {
	p := previewEnv(t, nil)
	q := p.another(t)
	writeIn(t, p.dir, "c-"+q.secret+".txt", "x\n")
	b := p.body(t, p.label(t), button())
	b.Annotations[0].Note = "the other key " + q.secret
	c := created(t, p.e, b, nil)
	if fmt.Sprint(originOf(c)["changedPaths"]) != "[c-"+treeSecretName+".txt]" {
		t.Fatalf("the changed paths: %v", originOf(c)["changedPaths"])
	}
	p.keptNowhere(t, p.secret, q.secret)
}

// The display name of a create, an edit and a delete is scrubbed too.
func TestADisplayNameHoldsNoTreesSecret(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	b.ByName = "Nate " + p.secret[:55]
	id := created(t, p.e, b, nil)["id"].(string)
	for _, w := range []struct {
		path string
		body map[string]any
	}{
		{"/edit", map[string]any{"opId": "named-edit", "byName": p.secret[4:], "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "plain"}}}},
		{"/delete", map[string]any{"opId": "named-delete", "byName": strings.ToUpper(p.secret[:60])}},
	} {
		if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+w.path, w.body); status != http.StatusOK {
			t.Fatalf("%s: %d %s", w.path, status, data)
		}
	}
	keptNoPart(t, p.e.srv.store.Dir(), p.secret)
}

// An edit is scrubbed of every tree's secret, its capture not looked up:
// one whose capture names no preview the engine holds.
func TestAnEditOfACaptureOfNoPreviewHoldsNoSecret(t *testing.T) {
	p := previewEnv(t, nil)
	ds := p.e.srv.dispatch
	l := p.label(t)
	n := p.body(t, l).NewCapture
	n.Annotations = []dispatch.NewAnnotation{{Kind: "element", Note: "x", Rect: dispatch.Rect{Width: 1, Height: 1},
		Element: &dispatch.ElementReport{Tag: "button", Selector: "#b"}, Observed: &dispatch.Observed{At: n.SelectedAt, URL: n.URL, Title: n.Title,
			Viewport: n.Viewport, Epoch: n.Epoch, Label: dispatch.RevisionLabel{Commit: n.Label.Commit, Content: n.Label.Content}}}}
	rc, err := ds.CreateCapture(t.Context(), dispatch.Op{Actor: "admin", ID: "elsewhere-1"}, strings.Repeat("f", 64), n,
		dispatch.CaptureFacts{Origin: "attempt", Run: p.run, Task: "deploy", Attempt: 1, Tree: "worktree", Commit: l["commit"].(string),
			Content: l["content"].(string), Mutable: true, Where: "native"}, nil)
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("a capture of no preview held: %v %v", rc, err)
	}
	p.edited(t, rc.ID, "pasted "+p.secret)
	p.keptNowhere(t, p.secret)
}

// An edit made once the preview was withdrawn (its service exited on its
// own) while the attempt still runs is scrubbed of its tree's secret.
func TestAnEditAfterThePreviewWasWithdrawnHoldsNoSecret(t *testing.T) {
	p := previewEnv(t, nil)
	c := created(t, p.e, p.body(t, p.label(t), button()), nil)
	// The service ends on its own: the preview is withdrawn, the attempt
	// runs on, its tree and its secret held.
	resp, err := http.Get(p.page + "exit")
	if err == nil {
		resp.Body.Close()
	}
	if !waitFor(time.Minute, func() bool {
		status, _, _ := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
		return status == http.StatusNotFound
	}) {
		t.Fatal("the preview was not withdrawn")
	}
	if !slices.Contains(p.e.srv.runs.TreeSecrets(), p.secret) {
		t.Fatal("the tree's secret is no longer held: the attempt ended")
	}
	p.edited(t, c["id"].(string), "pasted "+p.secret)
	p.keptNowhere(t, p.secret)
}

// A screenshot's text chunks are not kept: one holding the tree's secret
// as it is, one holding it compressed and one holding it in an
// international text are each taken (201), and no file of the store holds
// 8 characters in a row of the secret.
func TestAScreenshotsTextChunksAreNotKept(t *testing.T) {
	p := previewEnv(t, nil)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte(p.secret))
	zw.Close()
	for typ, data := range map[string][]byte{
		"zTXt": append([]byte("Comment\x00\x00"), z.Bytes()...),
		"tEXt": []byte("Comment\x00" + p.secret),
		"iTXt": []byte("Comment\x00\x00\x00\x00\x00" + strings.ToUpper(p.secret)),
	} {
		created(t, p.e, withImage(p.body(t, p.label(t))), withChunk(pngOf(t, 4, 4), typ, data))
	}
	keptNoPart(t, p.e.srv.store.Dir(), p.secret)
}

// 200 changed paths of 700 bytes: all counted, the list cut within its
// bytes in the capture.
func TestACapturesChangedPathsAreCutByBytes(t *testing.T) {
	p := previewEnv(t, nil)
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	deep := strings.Join([]string{strings.Repeat("d", 200), strings.Repeat("e", 200), strings.Repeat("g", 200)}, "/")
	if err := root.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if err := root.WriteFile(deep+"/"+fmt.Sprintf("%03d", i)+strings.Repeat("f", 700-len(deep)-4), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := originOf(created(t, p.e, p.body(t, p.label(t), button()), nil))
	paths, _ := json.Marshal(o["changedPaths"])
	if o["changed"] != float64(200) || o["changedCut"] != true || len(paths) > dispatch.MaxChangedPathsBytes {
		t.Fatalf("the capture: changed %v, cut %v, %d bytes of paths", o["changed"], o["changedCut"], len(paths))
	}
}

// A retake of the same run and task is related by the run; a deleted
// capture, a link's and another repository's are refused.
func TestARetakeOfTheSameRunIsRelatedByTheRun(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	first := created(t, p.e, p.body(t, l, button()), nil)
	again := p.body(t, l, button())
	again.RetakeOf = first["id"].(string)
	if c := created(t, p.e, again, nil); c["retakeVia"] != "run" || c["retakeOf"] != first["id"] {
		t.Fatalf("the retake: %v", c)
	}
	gone := created(t, p.e, p.body(t, l, button()), nil)
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+gone["id"].(string)+"/delete", map[string]any{"opId": "del-1"}); status != http.StatusOK {
		t.Fatalf("the delete: %d %s", status, data)
	}
	link := p.body(t, l)
	link.From, link.Label = captureFrom{URL: p.page}, dispatch.TreeRead{}
	linked := created(t, p.e, link, nil)
	if originOf(linked)["kind"] != "url" {
		t.Fatalf("the link's capture: %v", linked)
	}
	elsewhere := p.body(t, l)
	ds := p.e.srv.dispatch
	rc, err := ds.CreateCapture(t.Context(), dispatch.Op{Actor: "admin", ID: "else-1"}, strings.Repeat("f", 64), elsewhere.NewCapture,
		dispatch.CaptureFacts{Origin: "attempt", Run: p.run, Task: "build", Attempt: 1, Tree: "worktree", Commit: l["commit"].(string), Content: l["content"].(string),
			Mutable: true, Where: "native", Scope: dispatch.CaptureScope{Repo: "/another/repository"}}, nil)
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("another repository's capture: %v %v", rc, err)
	}
	for name, id := range map[string]string{"a deleted capture": gone["id"].(string), "a link's": linked["id"].(string), "another repository's": rc.ID} {
		b := p.body(t, l, button())
		b.RetakeOf = id
		if status, out := postCapture(t, p.e, b, nil); status != http.StatusBadRequest || codeOf(out) != "invalid" {
			t.Errorf("a retake of %s: %d %v", name, status, out)
		}
	}
}

// A screenshot that is not a whole PNG is refused, whatever its header.
func TestAnImageThatIsNotAPNGIsRefused(t *testing.T) {
	p := previewEnv(t, nil)
	good := pngOf(t, 64, 64)
	idat := bytes.Index(good, []byte("IDAT"))
	cut := slices.Clone(good[:idat+12])
	corrupt := slices.Clone(good)
	corrupt[idat+6] ^= 0xff
	for name, img := range map[string][]byte{"not a PNG": []byte("GIF89a not a png"), "cut short": cut, "a corrupt IDAT": corrupt} {
		out := captureRefused(t, p.e, withImage(p.body(t, p.label(t))), img, http.StatusBadRequest, "invalid")
		if !strings.Contains(apiMessage(out), "PNG") {
			t.Errorf("%s: %v", name, out)
		}
	}
	created(t, p.e, withImage(p.body(t, p.label(t))), good)
}

// A screenshot past 4,096 pixels a side is refused from its header.
func TestAnImagePastItsSideIsRefused(t *testing.T) {
	p := previewEnv(t, nil)
	captureRefused(t, p.e, withImage(p.body(t, p.label(t))), pngOf(t, dispatch.MaxCaptureImageSide+1, 1), http.StatusBadRequest, "invalid")
	created(t, p.e, withImage(p.body(t, p.label(t))), pngOf(t, dispatch.MaxCaptureImageSide, 1))
}

// The screenshot is served as an image and nothing else, whole: a request
// that names one range or two is answered 200 with the whole file and the
// same headers. (The kept file of a PNG Go's encoder made at its default
// level is that PNG byte for byte.)
func TestTheImageIsServedSandboxed(t *testing.T) {
	p := previewEnv(t, nil)
	shot := pngOf(t, 3, 2)
	c := created(t, p.e, withImage(p.body(t, p.label(t))), shot)
	for _, ranges := range []string{"", "bytes=0-3", "bytes=0-1,4-5"} {
		header := map[string]string{}
		if ranges != "" {
			header["Range"] = ranges
		}
		resp, body := p.e.raw("/api/captures/"+c["id"].(string)+"/image", adminToken, header)
		h := resp.Header
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, shot) || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Content-Security-Policy") != "sandbox" || h.Get("Content-Disposition") != "inline" || h.Get("Content-Range") != "" ||
			h.Get("Content-Length") != fmt.Sprint(len(shot)) || h.Get("X-Conductor-SHA256") != c["image"].(map[string]any)["sha256"] {
			t.Fatalf("the image, range %q: %d %v, %d bytes of %d", ranges, resp.StatusCode, h, len(body), len(shot))
		}
	}
	none := created(t, p.e, p.body(t, p.label(t)), nil)
	if resp, _ := p.e.raw("/api/captures/"+none["id"].(string)+"/image", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the image of a capture with none: %d", resp.StatusCode)
	}
}

// Every capture route is the admin's: a share link's viewer and
// controller, an agent's session token, a host token, a Brakeman turn and
// an identity provider's principal with the decide grant alone or the host
// grant alone are refused 401, and a switchyard answers 403.
func TestCaptureRoutesAreAdminOnly(t *testing.T) {
	fake := exttest.NewFakeIdentity("cloud")
	fake.Add(passAna, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantDecide}})
	fake.Add(passHostOnly, ext.Principal{Subject: "ana", Name: "Ana", Grants: []ext.Grant{ext.GrantHost}})
	provided := newTestEnvAgents(t, nil, slog.New(slog.DiscardHandler), nil, withIdentity(fake))
	b := brakemanEnv(t, nil)
	sid := b.createSession("cat")
	tokens := map[string]string{"none": "", "session": b.agentToken(sid), "host": "test-host-token"}
	for _, role := range []string{"view", "control"} {
		status, data, out := b.callAs(t, adminToken, "POST", "/api/sessions/"+sid+"/links", map[string]any{"role": role})
		if status != http.StatusCreated {
			t.Fatalf("a %s link: %d %s", role, status, data)
		}
		tokens["link "+role] = out["token"].(string)
	}
	b.enable(t, "bk", nil)
	bsid, btok, _ := b.turnSession(t, b.briefing(t))
	id := strings.Repeat("a", 16)
	routes := [][2]string{{"POST", "/api/captures"}, {"GET", "/api/captures"}, {"GET", "/api/captures/" + id}, {"GET", "/api/captures/" + id + "/image"},
		{"POST", "/api/captures/" + id + "/edit"}, {"POST", "/api/captures/" + id + "/delete"}}
	for _, r := range routes {
		var body any
		if r[0] == "POST" {
			body = map[string]any{"opId": "x"}
		}
		for who, tok := range tokens {
			if status, data, _ := b.callAs(t, tok, r[0], r[1], body); status != http.StatusUnauthorized {
				t.Errorf("%s %s with %s: %d %s", r[0], r[1], who, status, data)
			}
		}
		raw, _ := json.Marshal(body)
		if status, data, _ := b.callAsTurn(t, btok, bsid, r[0], r[1], raw); status != http.StatusUnauthorized {
			t.Errorf("%s %s as a Brakeman turn: %d %s", r[0], r[1], status, data)
		}
		for who, pass := range map[string]string{"the decide grant alone": passAna, "the host grant alone": passHostOnly} {
			if status, data, _ := provided.callAs(t, pass, r[0], r[1], body); status != http.StatusUnauthorized {
				t.Errorf("%s %s as a provider's principal with %s: %d %s", r[0], r[1], who, status, data)
			}
		}
	}
	yard := newTestEnv(t, func(c *config.Config) { c.Switchyard.Enabled = true })
	for _, r := range routes {
		if status, data, out := yard.callAs(t, adminToken, r[0], r[1], nil); status != http.StatusForbidden || codeOf(out) != "switchyard" {
			t.Errorf("%s %s on a switchyard: %d %s", r[0], r[1], status, data)
		}
	}
}

// The gate answers in its own words, never a parser's: a member the
// capture does not have, a part past its bound, a part of another name.
func TestTheCaptureGateAnswersInFixedWords(t *testing.T) {
	e := newTestEnv(t, nil)
	post := func(ct string, body []byte) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.http.URL+"/api/captures", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", ct)
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		data, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out
	}
	part := func(name string, body []byte) (string, []byte) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		w, _ := mw.CreateFormField(name)
		w.Write(body)
		mw.Close()
		return mw.FormDataContentType(), buf.Bytes()
	}
	planted := "PLANTED<img src=x>"
	for name, c := range map[string]struct {
		status int
		ct     string
		body   []byte
	}{
		"an unknown member": {http.StatusBadRequest, "", []byte(`{"opId":"a","` + planted + `":1}`)},
		"not JSON":          {http.StatusBadRequest, "", []byte(planted)},
		"another part":      {http.StatusBadRequest, "other", []byte(`{}`)},
		"past its bound":    {http.StatusRequestEntityTooLarge, "", bytes.Repeat([]byte(" "), maxCaptureJSON+1)},
		"not multipart":     {http.StatusBadRequest, "text/plain", []byte(planted)},
	} {
		ct, body := part("capture", c.body)
		switch c.ct {
		case "other":
			ct, body = part(planted, c.body)
		case "text/plain":
			ct, body = c.ct, c.body
		}
		status, out := post(ct, body)
		if status != c.status || strings.Contains(fmt.Sprint(out), "PLANTED") {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
}

// Brakeman's reads of Dispatch's changes pass over a capture's rows (a
// turn has no read of captures yet): a turn of an unscoped grant and one
// scoped by a repository each read the changes after a capture was made,
// edited and deleted, and no row names it.
func TestBrakemansChangesNameNoCapture(t *testing.T) {
	f := newObsFixture(t)
	since := time.Now().UTC()
	ds := f.srv.dispatch
	ctx := t.Context()
	n := dispatch.NewCapture{Via: "web", URL: "http://127.0.0.1:5173/", Title: "Page", SelectedAt: since, ImageHow: "none", ImageWhy: "none taken",
		Viewport: dispatch.Viewport{Mode: "fit", Width: 800, Height: 600, Scale: 1, DPR: 1, ColorScheme: "light", UserAgent: "test"}}
	ops := []string{"cap-make", "cap-edit", "cap-delete"}
	made := doneOf(t)(ds.CreateCapture(ctx, dispatch.Op{Actor: adminActor, ID: ops[0]}, strings.Repeat("a", 64), n, dispatch.CaptureFacts{Origin: dispatch.OriginURL}, nil))
	doneOf(t)(ds.EditCapture(ctx, dispatch.Op{Actor: adminActor, ID: ops[1]}, strings.Repeat("b", 64), made.ID, 1, nil))
	doneOf(t)(ds.DeleteCapture(ctx, dispatch.Op{Actor: adminActor, ID: ops[2]}, made.ID))
	project := doneOf(t)(ds.CreateProject(ctx, adminOp("Ann"), dispatch.NewProject{Key: "AFT", Name: "After"}))
	look := func(what string) {
		t.Helper()
		out := f.expectRead(t, "/api/brakeman/changes?since="+since.Format(time.RFC3339Nano), http.StatusOK, "")
		raw, _ := json.Marshal(out["changes"])
		for _, c := range out["changes"].([]any) {
			m := c.(map[string]any)
			if kind, _ := m["kind"].(string); strings.HasPrefix(kind, "capture") {
				t.Errorf("%s: a change names a capture: %v", what, m)
			}
		}
		for _, id := range append(ops, made.ID) {
			if strings.Contains(string(raw), id) {
				t.Errorf("%s: the changes name %s: %s", what, id, raw)
			}
		}
		if what == "an unscoped grant" && !strings.Contains(string(raw), project.OpID) {
			t.Errorf("%s: the project made after the capture is not among the changes: %s", what, raw)
		}
	}
	f.turn(t, nil)
	look("an unscoped grant")
	f.again(t, func(c map[string]any) { c["scope"] = map[string]any{"repos": []string{f.root}} })
	look("a grant scoped by a repository")
}
