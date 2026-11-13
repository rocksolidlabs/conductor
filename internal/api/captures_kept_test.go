package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/internal/catalog"
	"github.com/rocksolidlabs/conductor/internal/crew"
	"github.com/rocksolidlabs/conductor/internal/dispatch"
	"github.com/rocksolidlabs/conductor/internal/gitcli"
	"github.com/rocksolidlabs/conductor/internal/proto"
)

// What a capture keeps of its tree's read and of its request, its bodies'
// bounds and its reads as the admin. Each preview is a run of the test's
// own over a repository of its own, ended before its folders go; a step
// that must happen at a moment is held at a seam (the tree reads' runner,
// the stop's wait, afterCaptureRead), never on the clock.

// captureMultipart is a create's body: the capture part's bytes, the
// screenshot's (nil for none), and pad bytes after the closing boundary.
func captureMultipart(capture, png []byte, pad int) (string, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormField("capture")
	part.Write(capture)
	if png != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="image"; filename="shot.png"`)
		h.Set("Content-Type", "image/png")
		w, _ := mw.CreatePart(h)
		w.Write(png)
	}
	mw.Close()
	buf.Write(bytes.Repeat([]byte(" "), pad))
	return mw.FormDataContentType(), buf.Bytes()
}

// sendAs sends body as the admin with the content type, and answers the
// status and the reply's bytes.
func sendAs(e *testEnv, method, path, ct string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", ct)
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

// bodyAnswer is sendAs's answer decoded, failing t when it could not be
// sent.
func bodyAnswer(t *testing.T, e *testEnv, method, path, ct string, body []byte) (int, map[string]any) {
	t.Helper()
	status, data, err := sendAs(e, method, path, ct, body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return status, out
}

// postAsync creates a capture of b as the admin on a goroutine of its own
// and gives its answer on the returned channel.
func (p *previewed) postAsync(t *testing.T, b captureCreateBody) <-chan labelAnswer {
	t.Helper()
	ct, body := captureMultipart(encodeNoHTML(t, b), nil, 0)
	done := make(chan labelAnswer, 1)
	go func() {
		status, data, err := sendAs(p.e, "POST", "/api/captures", ct, body)
		done <- labelAnswer{status, data, err}
	}()
	return done
}

// withHints is a button whose report has a hint of each file.
func withHints(files ...string) proto.ElementReport {
	r := button()
	for _, f := range files {
		r.Hints = append(r.Hints, proto.ElementHint{How: "vue", File: f})
	}
	return r
}

// classesOf is the first annotation's sources of the capture c, each
// "path:class".
func classesOf(c map[string]any) []string {
	var out []string
	sources, _ := c["annotations"].([]any)[0].(map[string]any)["sources"].([]any)
	for _, s := range sources {
		m := s.(map[string]any)
		out = append(out, fmt.Sprintf("%s:%s", m["path"], m["class"]))
	}
	return out
}

// sharedRun is the first run of 8 characters of secret that s holds,
// in any case, or "".
func sharedRun(s []byte, secret string) string {
	low := bytes.ToLower(s)
	for i := 0; i+8 <= len(secret); i++ {
		if bytes.Contains(low, []byte(secret[i:i+8])) {
			return secret[i : i+8]
		}
	}
	return ""
}

// keptNoPart fails t when a file of Dispatch's store under the data
// directory dir, the write-ahead file among them, holds 8 characters in a
// row of the secret.
func keptNoPart(t *testing.T, dir, secret string) {
	t.Helper()
	filepath.WalkDir(filepath.Join(dir, "dispatch"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil {
			if run := sharedRun(b, secret); run != "" {
				t.Errorf("%s holds %q of a tree's secret", path, run)
			}
		}
		return nil
	})
}

// fillCaptures copies the capture id's row into Dispatch's store until it
// holds dispatch.MaxCaptures live captures, as a replica's rows would come.
func fillCaptures(t *testing.T, e *testEnv, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(e.srv.store.Dir(), "dispatch", dispatch.File)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM pragma_table_info('captures')")
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != "id" {
			cols = append(cols, c)
		}
	}
	rows.Close()
	var live int
	if err := db.QueryRow("SELECT COUNT(*) FROM captures WHERE deleted_at = ''").Scan(&live); err != nil {
		t.Fatal(err)
	}
	rest := strings.Join(cols, ", ")
	if _, err := db.Exec(`WITH RECURSIVE k(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM k WHERE x < ?)
		INSERT INTO captures (id, `+rest+`) SELECT printf('f%015x', x), `+rest+` FROM captures, k WHERE captures.id = ?`, dispatch.MaxCaptures-live, id); err != nil {
		t.Fatal(err)
	}
}

// The server's stop while a capture's tree is read, the read held at the
// runner's seam inside the capture's own step (the classes' ls-tree), its
// time limit the test's (treeReads.limit: none of its own): the
// stop cancels the read and waits for it (the bound's timer never fires),
// the folder of indexes is removed only once the read has returned (at the
// removal's seam no read is registered and the held git has returned), the
// capture is answered 503 tree_unread and nothing of it is kept.
func TestTheStopEndsACapturesReadBeforeItRemovesTheIndexes(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), withHints(filepath.Join(p.dir, "README")))
	b.Annotations[0].Note = "held-at-the-stop"
	held, ended, goOn := make(chan struct{}), make(chan error, 1), make(chan struct{})
	var returned atomic.Bool
	holding := false
	// The read's time limit is the test's (none): only the stop ends it.
	p.e.srv.trees.limit = context.WithCancel
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		// A read's commands run one after another: holding needs no lock.
		if args[0] != "ls-tree" || holding {
			return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
		}
		holding = true
		close(held)
		<-ctx.Done()
		ended <- ctx.Err()
		<-goOn
		returned.Store(true)
		return ctx.Err()
	}))
	done := p.postAsync(t, b)
	select {
	case <-held:
	case a := <-done:
		t.Fatalf("answered before its read was held: %d %s %v", a.status, a.body, a.err)
	}
	tr := &p.e.srv.trees
	waits := make(chan struct{})
	var waiting sync.Once
	tr.after = func(time.Duration) <-chan time.Time {
		waiting.Do(func() { close(waits) })
		return nil
	}
	var atRemoval []string
	tr.seam = func(at, _ string) {
		if at != "removing" {
			return
		}
		// Under the reads' lock: what is registered is read as it is.
		if len(tr.reads) != 0 {
			atRemoval = append(atRemoval, fmt.Sprintf("%d reads registered", len(tr.reads)))
		}
		if !returned.Load() {
			atRemoval = append(atRemoval, "the held git had not returned")
		}
	}
	tr.mu.Lock()
	folder := tr.dir
	tr.mu.Unlock()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// The budget only bounds a hang: the held read holds the stop.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		p.e.srv.Shutdown(ctx)
	}()
	<-waits
	close(goOn)
	<-stopped
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Errorf("the held git's context ended with %v, want a cancellation", err)
	}
	if atRemoval != nil {
		t.Errorf("at the removal: %v", atRemoval)
	}
	if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the folder of indexes after the stop: %v", err)
	}
	a := <-done
	var out map[string]any
	_ = json.Unmarshal(a.body, &out)
	if a.err != nil || a.status != http.StatusServiceUnavailable || codeOf(out) != "tree_unread" {
		t.Fatalf("the capture: %d %s %v", a.status, a.body, a.err)
	}
	if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), "held-at-the-stop"); len(found) > 0 {
		t.Fatalf("the capture is kept in %v", found)
	}
}

// A hint through a link to a folder of the tree is not handed to git: the
// capture keeps the content the label read just before, the tracked file
// classed, the hint through the link of a class unknown.
func TestAHintThroughALinkKeepsItsClassUnknown(t *testing.T) {
	p := previewEnv(t, map[string]string{"app/Save.vue": "<template/>"})
	if err := os.Symlink("app", filepath.Join(p.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	l := p.label(t)
	c := created(t, p.e, p.body(t, l, withHints(filepath.Join(p.dir, "app", "Save.vue"), filepath.Join(p.dir, "linked", "Save.vue"))), nil)
	if o := originOf(c); o["content"] != l["content"] || o["content"] == "" {
		t.Fatalf("the capture's content %v, the label's %v", o["content"], l["content"])
	}
	if got := fmt.Sprint(classesOf(c)); got != "[app/Save.vue:tracked linked/Save.vue:unknown]" {
		t.Fatalf("the sources: %s", got)
	}
}

// A hint into a submodule, which git will not class: the capture keeps
// the content and the changed paths it read, every hint's class unknown,
// and the log says so by its kind.
func TestAHintIntoASubmoduleKeepsTheContent(t *testing.T) {
	logs := &logBuffer{}
	e := newTestEnvAgents(t, nil, slog.New(slog.NewTextHandler(logs, nil)), []catalog.Agent{readyCat})
	e.gitRepo(t)
	sub := filepath.Join(e.root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, sub, "init", "-q")
	writeIn(t, sub, "x.vue", "<template/>")
	gitIn(t, sub, "add", "x.vue")
	gitIn(t, sub, "-c", "commit.gpgsign=false", "-c", "maintenance.auto=false", "commit", "-q", "-m", "x")
	gitIn(t, e.root, "add", "sub")
	gitIn(t, e.root, "-c", "commit.gpgsign=false", "-c", "maintenance.auto=false", "commit", "-q", "-m", "a submodule")
	p := launchPreview(t, e, previewedCrew(e.root))
	writeIn(t, p.dir, "new.txt", "x\n")
	l := p.label(t)
	c := created(t, p.e, p.body(t, l, withHints(filepath.Join(p.dir, "README"), filepath.Join(p.dir, "sub", "x.vue"))), nil)
	o := originOf(c)
	if o["content"] != l["content"] || o["content"] == "" || fmt.Sprint(o["changedPaths"]) != "[new.txt]" {
		t.Fatalf("the origin: %v (the label's %v)", o, l)
	}
	if got := fmt.Sprint(classesOf(c)); got != "[README:unknown sub/x.vue:unknown]" {
		t.Fatalf("the sources: %s", got)
	}
	if got := logs.String(); !strings.Contains(got, "a capture's sources are kept with their class unknown") || !strings.Contains(got, `why="git failed"`) {
		t.Fatalf("the log: %s", got)
	}
}

// No part of a tree's secret is kept: an attribute, the HTML, a note and
// a changed file's name each holding the first 57, the first 60 and the
// last 60 characters of the secret; no file of the store, the write-ahead
// file among them, holds 8 of its characters in a row; and a hexadecimal
// run of 40 characters that is no part of a secret is kept as it is.
func TestNoPartOfATreesSecretIsKept(t *testing.T) {
	p := previewEnv(t, nil)
	s := p.secret
	parts := []string{s[:57], s[:60], s[4:]}
	for i, part := range parts {
		writeIn(t, p.dir, fmt.Sprintf("f%d-%s.txt", i, part), "x\n")
	}
	sum := sha256.Sum256([]byte("no part of a secret"))
	plain := hex.EncodeToString(sum[:])[:40]
	for _, secret := range p.e.srv.runs.TreeSecrets() {
		if sharedRun([]byte(plain), secret) != "" {
			t.Fatalf("the plain run shares 8 characters with a secret")
		}
	}
	l := p.label(t)
	var reports []proto.ElementReport
	for _, part := range parts {
		r := button()
		r.Attributes = append(r.Attributes, [2]string{"data-key", part})
		r.HTML = `<button data-key="` + strings.ToUpper(part) + `">Save</button>`
		reports = append(reports, r)
	}
	b := p.body(t, l, reports...)
	for i, part := range parts {
		b.Annotations[i].Note = "pasted " + part
	}
	c := created(t, p.e, b, nil)
	if n := originOf(c)["changed"]; n != float64(3) {
		t.Fatalf("the changed paths: %v", originOf(c))
	}
	keep := p.body(t, l, button())
	keep.Annotations[0].Note = "commit " + plain
	if got := created(t, p.e, keep, nil)["annotations"].([]any)[0].(map[string]any)["note"]; got != "commit "+plain {
		t.Fatalf("the note: %v", got)
	}
	keptNoPart(t, p.e.srv.store.Dir(), s)
}

// A tree whose run ends and leaves the engine between the capture's read
// of it and the scrub of what was read (afterCaptureRead): its secret is
// no longer held, and the changed path that holds it is kept with the
// secret's name; no file of the store holds a part of it.
func TestATreeEndedAfterItsReadLeavesNoSecretInAPath(t *testing.T) {
	p := previewEnv(t, nil)
	writeIn(t, p.dir, "c-"+p.secret+".txt", "x\n")
	b := p.body(t, p.label(t), button())
	var still atomic.Bool
	p.e.srv.afterCaptureRead = func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = p.e.srv.runs.Stop(ctx, p.run)
		p.e.srv.runs.Drain(ctx)
		p.e.srv.runs.ForgetForTest(p.run)
		still.Store(slices.Contains(p.e.srv.runs.TreeSecrets(), p.secret))
	}
	c := created(t, p.e, b, nil)
	if still.Load() {
		t.Fatal("the tree's secret was still held after its run ended")
	}
	if got := fmt.Sprint(originOf(c)["changedPaths"]); got != "[c-"+treeSecretName+".txt]" {
		t.Fatalf("the changed paths: %s", got)
	}
	keptNoPart(t, p.e.srv.store.Dir(), p.secret)
}

// A refusal of a capture or of an edit names the member and what it may
// be, never what the request carried: the way it was made, the viewport's
// mode, the selection's commit and content, how the screenshot was taken,
// an annotation's kind, an edit's kind; the marker is in no reply, no
// receipt and no file of the store.
func TestACapturesRefusalQuotesNothingItCarried(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	const marker = "Marker-q7z<b>"
	for name, set := range map[string]func(b *captureCreateBody){
		"via":                          func(b *captureCreateBody) { b.Via = marker },
		"the viewport's mode":          func(b *captureCreateBody) { b.Viewport.Mode = marker; moment(b) },
		"the selection's commit":       func(b *captureCreateBody) { b.Label.Commit = marker; moment(b) },
		"the selection's content":      func(b *captureCreateBody) { b.Label.Content = marker; moment(b) },
		"how the screenshot was taken": func(b *captureCreateBody) { b.ImageHow = marker },
		"an annotation's kind":         func(b *captureCreateBody) { b.Annotations[0].Kind = marker },
	} {
		b := p.body(t, l, button())
		set(&b)
		status, data := postCaptureRaw(t, p.e, b, nil)
		if status != http.StatusBadRequest || bytes.Contains(data, []byte("Marker")) || !bytes.Contains(data, []byte(`"receipt"`)) {
			t.Errorf("%s: %d %s", name, status, data)
		}
	}
	id := created(t, p.e, p.body(t, l, button()), nil)["id"].(string)
	edit := map[string]any{"opId": "marked-edit", "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": marker, "note": "x"}}}
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+"/edit", edit); status != http.StatusBadRequest || bytes.Contains(data, []byte("Marker")) {
		t.Errorf("an edit's kind: %d %s", status, data)
	}
	if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), strings.ToLower(marker)); len(found) > 0 {
		t.Fatalf("the marker is in %v", found)
	}
}

// chunkOf is a PNG chunk of the type and data.
func chunkOf(typ string, data []byte) []byte {
	chunk := binary32(uint32(len(data)))
	chunk = append(append(chunk, typ...), data...)
	return append(chunk, binary32(crc32.ChecksumIEEE(append([]byte(typ), data...)))...)
}

func binary32(n uint32) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }

// palettedPNG is a 4 by 4 PNG with a palette and its transparency (PLTE
// and tRNS).
func palettedPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.NRGBA{0, 0, 0, 0}, color.NRGBA{200, 10, 10, 255}})
	img.SetColorIndex(1, 1, 1)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.Bytes(), []byte("PLTE")) || !bytes.Contains(b.Bytes(), []byte("tRNS")) {
		t.Fatal("the paletted PNG has no PLTE or no tRNS")
	}
	return b.Bytes()
}

// A screenshot is a plain image, whole: bytes after its end (a text
// chunk, or anything), a private chunk before its end and an animated
// image's chunks are refused in fixed words; each chunk a screenshot
// carries is taken and the image served byte for byte.
func TestAScreenshotIsAPlainImageWhole(t *testing.T) {
	p := previewEnv(t, nil)
	good := pngOf(t, 4, 4)
	for name, img := range map[string][]byte{
		"a text chunk after its end": slices.Concat(good, chunkOf("tEXt", []byte("Comment\x00a word"))),
		"anything after its end":     slices.Concat(good, []byte("anything")),
		"a private chunk":            withChunk(good, "prVt", []byte("x")),
		"an animation's control":     withChunk(good, "acTL", slices.Concat(binary32(1), binary32(0))),
		"an animation's frame":       withChunk(good, "fcTL", make([]byte, 26)),
	} {
		out := captureRefused(t, p.e, withImage(p.body(t, p.label(t))), img, http.StatusBadRequest, "invalid")
		if !strings.Contains(apiMessage(out), "not a plain screenshot") {
			t.Errorf("%s: %v", name, out)
		}
	}
	pal := palettedPNG(t)
	for typ, data := range map[string][]byte{
		"PLTE and tRNS": nil,
		"gAMA":          binary32(45455),
		"cHRM":          make([]byte, 32),
		"sRGB":          {0},
		"iCCP":          append([]byte("profile\x00\x00"), 0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01),
		"pHYs":          append(append(binary32(2835), binary32(2835)...), 1),
		"sBIT":          {8},
		"bKGD":          {0},
	} {
		img := pal
		if data != nil {
			img = withChunk(pal, typ, data)
		}
		c := created(t, p.e, withImage(p.body(t, p.label(t))), img)
		if resp, body := p.e.raw("/api/captures/"+c["id"].(string)+"/image", adminToken, nil); resp.StatusCode != http.StatusOK || !bytes.Equal(body, img) {
			t.Errorf("%s: served %d, %d bytes of %d", typ, resp.StatusCode, len(body), len(img))
		}
	}
}

// A hint's base is the opened tree: once the tree has been opened, its
// path renamed and a link to another folder put in its place, an absolute
// hint under that other folder is not recorded as inside the tree. Where
// git runs in the tree's path rather than in its opened folder (a system
// with no /proc), the read itself is refused, 503 tree_unread, and nothing
// is kept.
func TestAHintUnderAFolderPutInTheTreesPlaceIsOutside(t *testing.T) {
	p := previewEnv(t, nil)
	other := t.TempDir()
	writeIn(t, other, "README", "another\n")
	b := p.body(t, p.label(t), withHints(filepath.Join(other, "README")))
	aside, placed := p.dir+"-aside", false
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		if !placed {
			placed = true
			if err := os.Rename(p.dir, aside); err != nil {
				return err
			}
			if err := os.Symlink(other, p.dir); err != nil {
				return err
			}
		}
		return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
	}))
	t.Cleanup(func() {
		if placed {
			_ = os.Remove(p.dir)
			_ = os.Rename(aside, p.dir)
		}
	})
	status, out := postCapture(t, p.e, b, nil)
	switch {
	case status == http.StatusServiceUnavailable && codeOf(out) == "tree_unread":
	case status != http.StatusCreated:
		t.Fatalf("the capture: %d %v", status, out)
	default:
		src := out["capture"].(map[string]any)["annotations"].([]any)[0].(map[string]any)["sources"].([]any)[0].(map[string]any)
		if src["outside"] != true || src["found"] == true || src["path"] != filepath.Join(other, "README") {
			t.Fatalf("the source: %v", src)
		}
	}
	if !placed {
		t.Fatal("the tree's read never ran a git command")
	}
}

// The display name is judged as the request carried it: one within its
// bound that holds a part of a tree's secret is kept scrubbed, and the
// same request once the tree has ended and the server has restarted is
// answered by the first's receipt; one past its bound as carried is
// refused the same both times.
func TestADisplayNameIsJudgedAsTheRequestCarriedIt(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	b.ByName = "Nate " + p.secret[:55]
	long := p.body(t, p.label(t), button())
	long.ByName = strings.Repeat("n", dispatch.MaxByName+1)
	c := created(t, p.e, b, nil)
	if by, _ := c["createdBy"].(string); !strings.Contains(by, treeSecretName) || sharedRun([]byte(by), p.secret) != "" {
		t.Fatalf("the display name kept: %q", by)
	}
	refusedLong := func(e *testEnv, when string) {
		t.Helper()
		if status, out := postCapture(t, e, long, nil); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Errorf("a display name past its bound %s: %d %v", when, status, out)
		}
	}
	refusedLong(p.e, "first")
	if resp, out := p.e.do("POST", "/api/runs/"+p.run+"/stop", adminToken, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d %v", resp.StatusCode, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p.e.srv.Shutdown(ctx)
	again := p.e.restart()
	t.Cleanup(func() { stopEveryRun(again.srv) })
	if slices.Contains(again.srv.runs.TreeSecrets(), p.secret) {
		t.Fatal("the tree's secret is held after the restart")
	}
	status, out := postCapture(t, again, b, nil)
	if status != http.StatusOK || receiptOf(t, out)["repeat"] != true || receiptOf(t, out)["id"] != c["id"] {
		t.Fatalf("the same request after the restart: %d %v", status, out)
	}
	refusedLong(again, "after the restart")
	keptNoPart(t, again.srv.store.Dir(), p.secret)
}

// The list and the single read as the admin: captures on two tasks and
// two attempts, one sent to a ticket and one deleted; the list by each
// filter, paged by after with limit=1; limit=0, limit=101, attempt=0 and a
// task without its run refused; the deleted one's tombstone, and its
// screenshot not found.
func TestTheCaptureListAndReadAsTheAdmin(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	ds := p.e.srv.dispatch
	ctx := t.Context()
	made := func(op, task string, attempt int, shot bool) string {
		t.Helper()
		n := p.body(t, l).NewCapture
		var img io.Reader
		if shot {
			n.ImageHow, n.ImageWhy, n.ImageScale = "tab", "", 1
			img = bytes.NewReader(pngOf(t, 2, 2))
		}
		rc, err := ds.CreateCapture(ctx, dispatch.Op{Actor: adminActor, ID: op}, strings.Repeat("f", 64), n,
			dispatch.CaptureFacts{Origin: dispatch.OriginAttempt, Run: p.run, Task: task, Attempt: attempt, Tree: dispatch.TreeWorktree,
				Commit: l["commit"].(string), Content: l["content"].(string), Mutable: true, Where: "native"}, img)
		if err != nil || rc.Status != dispatch.StatusDone {
			t.Fatalf("%s: %+v %v", op, rc, err)
		}
		return rc.ID
	}
	a, b, c, d := made("list-a", "build", 1, false), made("list-b", "build", 2, false), made("list-c", "deploy", 1, false), made("list-d", "deploy", 1, true)
	if status, data, _ := p.e.callAs(t, adminToken, "POST", "/api/captures/"+d+"/delete", map[string]any{"opId": "list-del"}); status != http.StatusOK {
		t.Fatalf("the delete: %d %s", status, data)
	}
	if rc, err := ds.CreateProject(ctx, dispatch.Op{Actor: adminActor, ID: "list-proj"}, dispatch.NewProject{Key: "LST", Name: "List"}); err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the project: %+v %v", rc, err)
	}
	sentTo, err := ds.SendCapture(ctx, dispatch.Op{Actor: adminActor, ID: "list-send"}, strings.Repeat("e", 64), a, 1,
		dispatch.Destination{Kind: "ticket", Project: "LST", Title: "From a capture"}, dispatch.SendFacts{})
	if err != nil || sentTo.Status != dispatch.StatusDone {
		t.Fatalf("the send: %+v %v", sentTo, err)
	}
	page := func(query string) ([]string, string) {
		t.Helper()
		status, data, out := p.e.callAs(t, adminToken, "GET", "/api/captures"+query, nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", query, status, data)
		}
		var ids []string
		for _, c := range out["captures"].([]any) {
			ids = append(ids, c.(map[string]any)["id"].(string))
		}
		next, _ := out["next"].(string)
		return ids, next
	}
	set := func(ids ...string) string {
		sort.Strings(ids)
		return fmt.Sprint(ids)
	}
	for query, want := range map[string]string{
		"?run=" + p.run:                           set(a, b, c),
		"?run=" + p.run + "&task=build":           set(a, b),
		"?run=" + p.run + "&task=build&attempt=2": set(b),
		"?run=" + p.run + "&task=deploy":          set(c),
		"?ticket=" + sentTo.Key:                   set(a),
	} {
		if ids, next := page(query); set(ids...) != want || next != "" {
			t.Errorf("%s: %v next %q, want %s", query, ids, next, want)
		}
	}
	var all []string
	query := "?run=" + p.run + "&limit=1"
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatalf("more than 3 pages: %v", all)
		}
		ids, next := page(query)
		if len(ids) > 1 {
			t.Fatalf("a page of limit 1: %v", ids)
		}
		all = append(all, ids...)
		if next == "" {
			break
		}
		query = "?run=" + p.run + "&limit=1&after=" + next
	}
	if set(all...) != set(a, b, c) || len(all) != 3 {
		t.Fatalf("the pages: %v", all)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?run=" + p.run + "&task=build&attempt=0", "?task=build"} {
		if status, data, out := p.e.callAs(t, adminToken, "GET", "/api/captures"+query, nil); status != http.StatusBadRequest || codeOf(out) != "invalid_request" {
			t.Errorf("%s: %d %s", query, status, data)
		}
	}
	status, data, out := p.e.callAs(t, adminToken, "GET", "/api/captures/"+d, nil)
	if keys := slices.Sorted(maps.Keys(out)); status != http.StatusOK || fmt.Sprint(keys) != "[createdAt deletedAt id rev]" || out["id"] != d {
		t.Fatalf("the deleted capture: %d %s", status, data)
	}
	if resp, _ := p.e.raw("/api/captures/"+d+"/image", adminToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the deleted capture's screenshot: %d", resp.StatusCode)
	}
}

// Each body at its bound and one byte past it: at it the body is taken,
// or refused for its content (the screenshot padded past its end), never
// 413; one past it is 413 bound in the gate's words. The capture part
// (2 MiB), the screenshot part (4 MiB), the whole body (the capture part
// at its bound, a screenshot and bytes after the closing boundary), an
// edit's body (2 MiB) and a delete's (64 KiB).
func TestEachCaptureBodyAtItsBoundAndOnePast(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	id := created(t, p.e, p.body(t, l, button()), nil)["id"].(string)
	padded := func(b []byte, n int) []byte { return append(b, bytes.Repeat([]byte(" "), n-len(b))...) }
	shot := pngOf(t, 4, 4)
	rows := []struct {
		name     string
		at, past int
		send     func(extra int) (int, map[string]any)
	}{
		{"the capture part", http.StatusCreated, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
			ct, body := captureMultipart(padded(encodeNoHTML(t, p.body(t, l, button())), maxCaptureJSON+extra), nil, 0)
			return bodyAnswer(t, p.e, "POST", "/api/captures", ct, body)
		}},
		{"the screenshot part", http.StatusBadRequest, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
			img := append(slices.Clone(shot), make([]byte, dispatch.MaxCaptureImage+extra-len(shot))...)
			ct, body := captureMultipart(encodeNoHTML(t, withImage(p.body(t, l))), img, 0)
			return bodyAnswer(t, p.e, "POST", "/api/captures", ct, body)
		}},
		{"the whole body", http.StatusCreated, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
			capture := padded(encodeNoHTML(t, withImage(p.body(t, l))), maxCaptureJSON)
			_, body := captureMultipart(capture, shot, 0)
			ct, body := captureMultipart(capture, shot, maxCaptureBody+extra-len(body))
			return bodyAnswer(t, p.e, "POST", "/api/captures", ct, body)
		}},
		{"an edit's body", http.StatusOK, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
			edit := encodeNoHTML(t, map[string]any{"opId": opID(), "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "x"}}})
			return bodyAnswer(t, p.e, "POST", "/api/captures/"+id+"/edit", "application/json", padded(edit, maxCaptureJSON+extra))
		}},
		{"a delete's body", http.StatusOK, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
			return bodyAnswer(t, p.e, "POST", "/api/captures/"+id+"/delete", "application/json", padded(encodeNoHTML(t, map[string]any{"opId": opID()}), maxBody+extra))
		}},
	}
	for _, r := range rows {
		if status, out := r.send(1); status != r.past || codeOf(out) != "bound" {
			t.Errorf("%s one byte past its bound: %d %v", r.name, status, out)
		}
		if status, out := r.send(0); status != r.at {
			t.Errorf("%s at its bound: %d %v", r.name, status, out)
		}
	}
}

// A capture's scope is what the server read of its run: a run launched
// from a ticket names the crew, the top of its repository and the ticket's
// project, and a run launched from none names no project. The first
// capture, sent to the ticket, is retaken through the route in a second
// managed run launched from that ticket, the repair's: the retake is
// related by the ticket.
func TestARetakeAcrossTheRepairLaunch(t *testing.T) {
	l := newLaunchTest(t, nil, func(c *crew.Crew) { c.Manifest = previewedCrew("").Manifest })
	launched := func() string {
		t.Helper()
		body := map[string]any{"opId": opID(), "plan": l.plan}
		status, data, out := l.call(t, "POST", "/api/dispatch/tickets/CND-1/launch", body)
		if status == http.StatusConflict && codeOf(out) == dispatch.RefusalStalePlan {
			l.expect(t, "POST", "/api/dispatch/plans/"+l.plan+"/approve", map[string]any{"opId": opID(), "note": "Again."}, http.StatusOK, "")
			body["opId"] = opID()
			status, data, out = l.call(t, "POST", "/api/dispatch/tickets/CND-1/launch", body)
		}
		if status != http.StatusCreated {
			t.Fatalf("the launch: %d %s", status, data)
		}
		return entityOf(t, out, "launch")["runId"].(string)
	}
	top := gitIn(t, l.repo, "rev-parse", "--show-toplevel")
	p1 := previewedRun(t, l.testEnv, launched())
	first := created(t, l.testEnv, p1.body(t, p1.label(t), button()), nil)
	if sc := first["scope"].(map[string]any); sc["crew"] != l.crew.ID || sc["repo"] != top || sc["project"] != "CND" {
		t.Fatalf("the scope of a run launched from a ticket: %v (crew %s, repository %s)", sc, l.crew.ID, top)
	}
	rc, err := l.srv.dispatch.SendCapture(t.Context(), dispatch.Op{Actor: adminActor, ID: "to-the-ticket"}, strings.Repeat("e", 64), first["id"].(string), 1,
		dispatch.Destination{Kind: "ticket", Key: "CND-1"}, dispatch.SendFacts{})
	if err != nil || rc.Status != dispatch.StatusDone {
		t.Fatalf("the send to CND-1: %+v %v", rc, err)
	}
	p2 := previewedRun(t, l.testEnv, launched())
	if p2.run == p1.run {
		t.Fatal("the second launch took the first run")
	}
	retake := p2.body(t, p2.label(t), button())
	retake.RetakeOf = first["id"].(string)
	if c := created(t, l.testEnv, retake, nil); c["retakeVia"] != "ticket" || c["retakeOf"] != first["id"] {
		t.Fatalf("the retake across the launch: %v", c)
	}
	q := launchPreview(t, l.testEnv, previewedCrew(l.repo))
	if sc := created(t, l.testEnv, q.body(t, q.label(t), button()), nil)["scope"].(map[string]any); sc["project"] != nil || sc["repo"] != top {
		t.Fatalf("the scope of a run launched from no ticket: %v", sc)
	}
}
