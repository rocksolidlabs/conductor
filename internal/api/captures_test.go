package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// previewedCrew is servedCrew whose preview names the tree's secret in its
// fragment, as a preview that hands its page a credential does.
func previewedCrew(root string) crew.Crew {
	c := servedCrew(root)
	c.ID, c.Name = "previewed", "Previewed"
	c.Manifest.Preview = "http://127.0.0.1:${CONDUCTOR_PORT_WEB}/#token=${CONDUCTOR_TREE_SECRET}"
	return c
}

// previewed is a run whose attempt's worktree preview is open: the
// worktree, the page (the preview without its fragment) and the tree's
// secret, read from the admin's label.
type previewed struct {
	e      *testEnv
	run    string
	dir    string
	page   string
	secret string
}

// previewEnv launches previewedCrew over a repository that also holds
// files (committed), and waits for its attempt's preview.
func previewEnv(t *testing.T, files map[string]string) *previewed {
	t.Helper()
	e := readyEnv(t, nil)
	e.gitRepo(t)
	for name, body := range files {
		writeIn(t, e.root, name, body)
		gitIn(t, e.root, "add", name)
	}
	if len(files) > 0 {
		gitIn(t, e.root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "files")
	}
	run, release, err := e.srv.runs.LaunchNamed(t.Context(), previewedCrew(e.root), "")
	release()
	if err != nil {
		t.Fatal(err)
	}
	a := runningAttempt(t, e, run.ID, "build", 1)
	p := &previewed{e: e, run: run.ID, dir: a["worktree"].(string)}
	u, secret, _ := strings.Cut(p.label(t)["url"].(string), "#token=")
	if len(secret) != 64 || strings.Contains(u, "#") {
		t.Fatalf("the admin's label URL: %q", u+"#token="+secret)
	}
	p.page, p.secret = u, secret
	return p
}

// writeIn writes the file name (slash-separated) under dir.
func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// label reads the attempt's label as the admin.
func (p *previewed) label(t *testing.T) map[string]any {
	t.Helper()
	status, data, out := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run+"/tasks/build/attempts/1/preview", nil)
	if status != http.StatusOK {
		t.Fatalf("the label: %d %s", status, data)
	}
	return out
}

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
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
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

// A label is the tree now: a file changed between two reads changes the
// content and the changed paths, at the same commit.
func TestThePreviewLabelReadsTheTreeNow(t *testing.T) {
	p := previewEnv(t, nil)
	first := p.label(t)
	if first["tree"] != "worktree" || first["mutable"] != true || first["changed"] != float64(0) || first["content"] == "" || first["unread"] != nil {
		t.Fatalf("the first read: %v", first)
	}
	writeIn(t, p.dir, "README", "changed\n")
	second := p.label(t)
	if second["commit"] != first["commit"] || second["content"] == first["content"] || second["changed"] != float64(1) ||
		fmt.Sprint(second["changedPaths"]) != "[README]" {
		t.Fatalf("the second read: %v (the first %v)", second, first)
	}
}

// A file a first read counted leaves a later read once an ignore rule
// covers it, HEAD unmoved: nothing an earlier read staged survives.
func TestANewIgnoreRuleLeavesTheLabel(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "junk.log", "junk\n")
	if l := p.label(t); fmt.Sprint(l["changedPaths"]) != "[junk.log]" {
		t.Fatalf("the first read: %v", l)
	}
	writeIn(t, p.dir, ".gitignore", "junk.log\n")
	ignored := p.label(t)
	if fmt.Sprint(ignored["changedPaths"]) != "[.gitignore]" || ignored["changed"] != float64(1) {
		t.Fatalf("the read after the rule: %v", ignored)
	}
	if err := os.Remove(filepath.Join(p.dir, "junk.log")); err != nil {
		t.Fatal(err)
	}
	if without := p.label(t); without["content"] != ignored["content"] {
		t.Fatalf("the content with the file ignored %v, without it %v", ignored["content"], without["content"])
	}
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
		b.Annotations = []captureAnnotation{observedAt(b, button())}
		if s := originOf(created(t, p.e, b, nil))["serving"].(map[string]any); s["state"] != c.state || (c.revision != "" && s["page"] != c.revision) {
			t.Errorf("the page declaring %q: %v, want %s", c.revision, s, c.state)
		}
	}
	writeIn(t, p.dir, "README", "dirty\n")
	dirty := p.label(t)
	b := p.body(t, dirty, button())
	b.Revision = commit
	b.Annotations = []captureAnnotation{observedAt(b, button())}
	if s := originOf(created(t, p.e, b, nil))["serving"].(map[string]any); s["state"] != "unconfirmed" {
		t.Errorf("a commit alone over a tree with changes: %v", s)
	}
}

// hasSecret reports whether any file under dir holds the secret, in any
// case.
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

// A create, its reply lost, is answered by its receipt after the preview
// has closed and its secret is gone; so is an edit; another body under
// the same opId is op_conflict; the store holds the secret nowhere.
func TestALostReplyAfterTheTreeEndedIsReplayed(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	b.URL = p.page + "#token=" + p.secret
	b.Annotations = []captureAnnotation{observedAt(b, button())}
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
	if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), p.secret); len(found) > 0 {
		t.Fatalf("the secret is in %v", found)
	}
}

// The label's URL has the tree's secret for the admin alone; nothing but
// the label gives it, and the run's read keeps its name.
func TestTheLabelGivesTheSecretToTheAdminAlone(t *testing.T) {
	p := previewEnv(t, nil)
	path := "/api/runs/" + p.run + "/tasks/build/attempts/1/preview"
	for who, tok := range map[string]string{"none": "", "host": "test-host-token", "a stranger": "not-a-token"} {
		if status, data, _ := p.e.callAs(t, tok, "GET", path, nil); status != http.StatusUnauthorized || bytes.Contains(data, []byte(p.secret)) {
			t.Errorf("the label for %s: %d %s", who, status, data)
		}
	}
	status, data, _ := p.e.callAs(t, adminToken, "GET", "/api/runs/"+p.run, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte(p.secret)) || !bytes.Contains(data, []byte("#token=${CONDUCTOR_TREE_SECRET}")) {
		t.Fatalf("the run's read: %d %s", status, data)
	}
	if u := labelURL("http://h:1/#token="+treeSecretName, p.secret, false); u != "http://h:1/" {
		t.Fatalf("the label's URL for another than the admin: %q", u)
	}
}

// The revision is the server's: a client's commit and content are kept
// as the drift, never as the capture's.
func TestACaptureTakesTheServersRevision(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	b := p.body(t, l)
	b.Label.Commit, b.Label.Content = strings.Repeat("b", 40), strings.Repeat("c", 40)
	b.Annotations = []captureAnnotation{observedAt(b, button())}
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

// The bridge's fullest report, at every field's bound, is taken at the
// bridge's measure, and so is every element the bridge answered on its
// three pages; the largest-tag report measures what the bridge measured,
// and the store's field rules refuse its control characters (U+0001,
// which no name, text or selector the store keeps may hold).
func TestTheBridgesFullestReportIsAccepted(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	n, fullest := readFixture(t, "fullest-report.json")
	if got := proto.PreviewMeasure(fullest); got != n || n > proto.MaxElementReport {
		t.Fatalf("the fullest report measures %d, the bridge %d", got, n)
	}
	created(t, p.e, p.body(t, l, fullest), nil)
	n, largest := readFixture(t, "largest-tag-report.json")
	if got := proto.PreviewMeasure(largest); got != n {
		t.Fatalf("the largest-tag report measures %d, the bridge %d", got, n)
	}
	captureRefused(t, p.e, p.body(t, l, largest), nil, http.StatusBadRequest, "invalid")
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

// pngOf is a w by h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
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
	if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), p.secret); len(found) > 0 {
		t.Fatalf("the secret is in %v", found)
	}
}

// 200 changed paths of 4,000 bytes: all counted, the list cut within its
// bytes, in the label and in the capture.
func TestLongChangedPathsAreCutByBytes(t *testing.T) {
	p := previewEnv(t, nil)
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var dir []string
	for len(strings.Join(dir, "/")) < 3800 {
		dir = append(dir, strings.Repeat("d", 200))
	}
	deep := strings.Join(dir, "/")
	if err := root.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		name := deep + "/" + fmt.Sprintf("%03d", i) + strings.Repeat("f", 4000-len(deep)-4)
		if err := root.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := p.label(t)
	paths, _ := json.Marshal(l["changedPaths"])
	if l["changed"] != float64(200) || l["changedCut"] != true || len(paths) > dispatch.MaxChangedPathsBytes || len(l["changedPaths"].([]any)) == 0 {
		t.Fatalf("the label: changed %v, cut %v, %d bytes of paths", l["changed"], l["changedCut"], len(paths))
	}
	o := originOf(created(t, p.e, p.body(t, l, button()), nil))
	if o["changed"] != float64(200) || o["changedCut"] != true {
		t.Fatalf("the capture: changed %v, cut %v", o["changed"], o["changedCut"])
	}
}

// A retake of the same run and task is related by the run; a deleted
// capture, a link's and another repository's are refused.
func TestARetakeAcrossTheRepairLaunch(t *testing.T) {
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

// The screenshot is served as an image and nothing else.
func TestTheImageIsServedSandboxed(t *testing.T) {
	p := previewEnv(t, nil)
	shot := pngOf(t, 3, 2)
	c := created(t, p.e, withImage(p.body(t, p.label(t))), shot)
	resp, body := p.e.raw("/api/captures/"+c["id"].(string)+"/image", adminToken, nil)
	h := resp.Header
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, shot) || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Security-Policy") != "sandbox" || h.Get("X-Conductor-SHA256") != c["image"].(map[string]any)["sha256"] {
		t.Fatalf("the image: %d %v", resp.StatusCode, h)
	}
	none := created(t, p.e, p.body(t, p.label(t)), nil)
	if resp, _ := p.e.raw("/api/captures/"+none["id"].(string)+"/image", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the image of a capture with none: %d", resp.StatusCode)
	}
}

// Every capture route and the label are the admin's: a share link's
// viewer and controller, an agent's session token, a host token and a
// Brakeman turn are refused 401, and a switchyard answers 403.
func TestCaptureRoutesAreAdminOnly(t *testing.T) {
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
		{"POST", "/api/captures/" + id + "/edit"}, {"POST", "/api/captures/" + id + "/delete"}, {"GET", "/api/runs/r-1/tasks/build/attempts/1/preview"}}
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
