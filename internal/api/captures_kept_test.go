package api

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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
	"runtime"
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

// beforeEnd is the PNG b with the chunks put just before its end (IEND).
func beforeEnd(b []byte, chunks ...[]byte) []byte {
	return slices.Concat(b[:len(b)-12], slices.Concat(chunks...), b[len(b)-12:])
}

// zlibOf is data compressed as a PNG compresses its chunks.
func zlibOf(data []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(data)
	zw.Close()
	return z.Bytes()
}

// encodedPNG is img encoded as Go's encoder makes it.
func encodedPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// screenshotKinds are a 5 by 3 screenshot of each kind of pixel: 8 bits
// and 16 bits a channel, a palette and grey, each with and without
// transparency (grey's tRNS chunk put in by hand).
func screenshotKinds(t *testing.T) map[string][]byte {
	t.Helper()
	r := image.Rect(0, 0, 5, 3)
	rgba, nrgba, rgba64, nrgba64 := image.NewRGBA(r), image.NewNRGBA(r), image.NewRGBA64(r), image.NewNRGBA64(r)
	gray := image.NewGray(r)
	pal := color.Palette{color.NRGBA{10, 20, 30, 255}, color.NRGBA{200, 10, 10, 255}, color.NRGBA{0, 90, 0, 255}}
	paletted, palettedAlpha := image.NewPaletted(r, pal), image.NewPaletted(r, append(slices.Clone(pal), color.NRGBA{0, 0, 0, 0}))
	for x := range 5 {
		for y := range 3 {
			v := uint8(40*x + 60*y + 1)
			rgba.Set(x, y, color.RGBA{v, 255 - v, v / 2, 255})
			nrgba.Set(x, y, color.NRGBA{v, 255 - v, v / 2, uint8(50 * x)})
			rgba64.Set(x, y, color.RGBA64{uint16(v) * 257, 1000 * uint16(x), 3000 * uint16(y), 0xffff})
			nrgba64.Set(x, y, color.NRGBA64{uint16(v)*257 + 1, 1000 * uint16(x), 3000 * uint16(y), 12000 * uint16(x+y)})
			gray.SetGray(x, y, color.Gray{v})
			paletted.SetColorIndex(x, y, uint8((x+y)%3))
			palettedAlpha.SetColorIndex(x, y, uint8((x+y)%4))
		}
	}
	grayAlpha := withChunk(encodedPNG(t, gray), "tRNS", []byte{0, 41})
	return map[string][]byte{"8-bit": encodedPNG(t, rgba), "8-bit, transparent": encodedPNG(t, nrgba),
		"16-bit": encodedPNG(t, rgba64), "16-bit, transparent": encodedPNG(t, nrgba64),
		"paletted": encodedPNG(t, paletted), "paletted, transparent": encodedPNG(t, palettedAlpha),
		"grey": encodedPNG(t, gray), "grey, transparent": grayAlpha}
}

// samePixels fails t unless the PNGs a and b decode to the same size and,
// pixel by pixel, the same colours.
func samePixels(t *testing.T, name string, a, b []byte) {
	t.Helper()
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("%s: the upload: %v", name, err)
	}
	ib, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: the kept file: %v", name, err)
	}
	if ia.Bounds() != ib.Bounds() {
		t.Fatalf("%s: the kept file is %v, the upload %v", name, ib.Bounds(), ia.Bounds())
	}
	for x := ia.Bounds().Min.X; x < ia.Bounds().Max.X; x++ {
		for y := ia.Bounds().Min.Y; y < ia.Bounds().Max.Y; y++ {
			if ca, cb := color.NRGBA64Model.Convert(ia.At(x, y)), color.NRGBA64Model.Convert(ib.At(x, y)); ca != cb {
				t.Fatalf("%s: pixel %d,%d is %v, the upload's %v", name, x, y, cb, ca)
			}
		}
	}
}

// chunkTypes is the types of the PNG b's chunks, in order.
func chunkTypes(b []byte) []string {
	var types []string
	for i := 8; i+8 <= len(b); i += 12 + int(binary.BigEndian.Uint32(b[i:])) {
		types = append(types, string(b[i+4:i+8]))
	}
	return types
}

// What is kept of a screenshot is the server's own encoding of its pixels
// (pngKept): an upload with image data after its pixels' end, a text
// chunk, a colour profile, each small colour chunk holding a marker, bytes
// after its end, and an animated image's chunks are each kept (201) as
// that encoding, holding none of the marker's bytes and no chunk but the
// pixels' own, decoding to the upload's pixels at its size; the same upload
// sent again is answered by the first's receipt. Every kind of pixel (8
// and 16 bits, a palette, grey, each with and without transparency) is
// kept pixel for pixel, and the image's route and its hash are the kept
// file's.
func TestWhatIsKeptOfAScreenshotIsTheServersEncoding(t *testing.T) {
	p := previewEnv(t, nil)
	l := p.label(t)
	marker := []byte("Mk7qZw9xR2")
	kept := func(name string, upload []byte) (captureCreateBody, map[string]any, []byte) {
		t.Helper()
		b := withImage(p.body(t, l))
		c := created(t, p.e, b, upload)
		resp, served := p.e.raw("/api/captures/"+c["id"].(string)+"/image", adminToken, nil)
		sum := sha256.Sum256(served)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Conductor-SHA256") != hex.EncodeToString(sum[:]) ||
			c["image"].(map[string]any)["sha256"] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: served %d, its hash %s, the capture's %v", name, resp.StatusCode, resp.Header.Get("X-Conductor-SHA256"), c["image"])
		}
		img, err := png.Decode(bytes.NewReader(upload))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var own bytes.Buffer
		if err := pngKept.Encode(&own, img); err != nil || !bytes.Equal(served, own.Bytes()) {
			t.Fatalf("%s: the kept file is not the server's encoding of the upload's pixels (%v)", name, err)
		}
		samePixels(t, name, upload, served)
		return b, c, served
	}
	for name, upload := range screenshotKinds(t) {
		kept(name, upload)
	}
	base := pngOf(t, 6, 4)
	// An animation's frame: its number, its size, its place, its delay
	// (1/1 s) and how it is disposed of and blended.
	frame := func(seq uint32) []byte {
		return slices.Concat(binary32(seq), binary32(6), binary32(4), make([]byte, 8), []byte{0, 1, 0, 1, 0, 0})
	}
	// padded is n bytes, b's first.
	padded := func(n int, b []byte) []byte {
		out := make([]byte, n)
		copy(out, b)
		return out
	}
	for name, upload := range map[string][]byte{
		"image data after its pixels' end": beforeEnd(base, chunkOf("IDAT", zlibOf(marker))),
		"a text chunk":                     withChunk(base, "tEXt", append([]byte("Comment\x00"), marker...)),
		"a colour profile":                 withChunk(base, "iCCP", append([]byte("profile\x00\x00"), zlibOf(marker)...)),
		"a gamma":                          withChunk(base, "gAMA", marker[:4]),
		"chromaticities":                   withChunk(base, "cHRM", padded(32, marker)),
		"a colour space":                   withChunk(base, "sRGB", marker[:1]),
		"a physical size":                  withChunk(base, "pHYs", padded(9, marker)),
		"significant bits":                 withChunk(base, "sBIT", marker[:4]),
		"a background":                     withChunk(base, "bKGD", padded(6, marker)),
		"bytes after its end":              slices.Concat(base, marker),
		"an animated image": beforeEnd(withChunk(withChunk(base, "fcTL", frame(0)), "acTL", slices.Concat(binary32(2), binary32(0))),
			chunkOf("fcTL", frame(1)), chunkOf("fdAT", slices.Concat(binary32(2), zlibOf(marker)))),
	} {
		b, c, served := kept(name, upload)
		if bytes.Contains(served, marker[:4]) || fmt.Sprint(chunkTypes(served)) != "[IHDR IDAT IEND]" {
			t.Errorf("%s: the kept file's chunks %v, the marker kept %v", name, chunkTypes(served), bytes.Contains(served, marker[:4]))
		}
		again, out := postCapture(t, p.e, b, upload)
		if again != http.StatusOK || receiptOf(t, out)["repeat"] != true || receiptOf(t, out)["id"] != c["id"] {
			t.Errorf("%s sent again: %d %v", name, again, out)
		}
	}
	p.keptNowhere(t, strings.ToLower(string(marker)))
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

// The display name is judged as the request carried it: one at its bound
// made of parts of a tree's secret is kept scrubbed, no longer than it
// came, and the same request once the tree has ended and the server has
// restarted is answered by the first's receipt; one past its bound as
// carried is refused the same both times.
func TestADisplayNameIsJudgedAsTheRequestCarriedIt(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	b.ByName = "Nate " + p.secret[:8] + " " + p.secret[10:18] + " " + p.secret[20:30] + " " + p.secret[30:56]
	if n := len([]rune(b.ByName)); n != dispatch.MaxByName {
		t.Fatalf("the display name is %d characters", n)
	}
	long := p.body(t, p.label(t), button())
	long.ByName = strings.Repeat("n", dispatch.MaxByName+1)
	c := created(t, p.e, b, nil)
	want := "Nate " + strings.Repeat(treeSecretPart+" ", 3) + treeSecretPart
	if by, _ := c["createdBy"].(string); by != want || sharedRun([]byte(by), p.secret) != "" {
		t.Fatalf("the display name kept: %q, want %q", by, want)
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
// filter, paged by after with limit=1; limit=0, limit=101, attempt=0, a
// task without its run, and an attempt or a limit given twice, empty or
// in anything but digits refused; the deleted one's tombstone, and its
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
	attempt := "?run=" + p.run + "&task=build&attempt="
	for _, query := range []string{"?limit=0", "?limit=101", attempt + "0", "?task=build",
		// The numbers are read once and whole: given twice, given empty,
		// or with a sign, a space or anything but digits.
		attempt + "1&attempt=2", attempt, attempt + "%2B1", attempt + "-1", attempt + "+1", attempt + "1x", attempt + "99999999999999999999",
		"?limit=1&limit=101", "?limit=", "?limit=%2B5", "?limit=-5", "?limit=1e2", "?limit=0x10"} {
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

// Each body at its bound and one byte past it: at it the body is taken
// (the screenshot padded past its end is kept as the server's encoding of
// its pixels), never 413; one past it is 413 bound in the gate's words.
// The capture part (2 MiB), the screenshot part (4 MiB), the whole body (the capture part
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
		{"the screenshot part", http.StatusCreated, http.StatusRequestEntityTooLarge, func(extra int) (int, map[string]any) {
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

// atBound is n characters of fill ending in tail.
func atBound(n int, tail string) string { return strings.Repeat("v", n-len(tail)) + tail }

// The scrub makes no text longer: an attribute's value, an element's text
// and a note each at its bound and ending in the first 10 characters of
// the tree's secret are taken (201), kept scrubbed, each no longer than it
// was sent; an edit's note at its bound likewise (200).
func TestNoScrubbedTextIsLongerThanItCame(t *testing.T) {
	p := previewEnv(t, nil)
	part := p.secret[:10]
	value, text, note := atBound(dispatch.MaxAttributeValue, part), atBound(dispatch.MaxElementText, part), atBound(dispatch.MaxAnnotationNote, part)
	r := button()
	r.Attributes = append(r.Attributes, [2]string{"data-key", value})
	r.Text = text
	b := p.body(t, p.label(t), r)
	b.Annotations[0].Note = note
	c := created(t, p.e, b, nil)
	a := c["annotations"].([]any)[0].(map[string]any)
	el := a["element"].(map[string]any)
	attrs := el["attributes"].([]any)
	got := map[string]string{"the attribute's value": attrs[len(attrs)-1].([]any)[1].(string), "the element's text": el["text"].(string), "the note": a["note"].(string)}
	sent := map[string]string{"the attribute's value": value, "the element's text": text, "the note": note}
	for name, kept := range got {
		if want := strings.TrimSuffix(sent[name], part) + treeSecretPart; kept != want || len(kept) > len(sent[name]) {
			t.Errorf("%s: kept %d bytes ending %q, sent %d", name, len(kept), kept[max(0, len(kept)-12):], len(sent[name]))
		}
	}
	edit := map[string]any{"opId": opID(), "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": note}}}
	status, data, out := p.e.callAs(t, adminToken, "POST", "/api/captures/"+c["id"].(string)+"/edit", edit)
	if status != http.StatusOK {
		t.Fatalf("the edit: %d %s", status, data)
	}
	if kept := out["capture"].(map[string]any)["annotations"].([]any)[0].(map[string]any)["note"].(string); kept != strings.TrimSuffix(note, part)+treeSecretPart {
		t.Errorf("the edit's note: %d bytes ending %q", len(kept), kept[max(0, len(kept)-12):])
	}
	keptNoPart(t, p.e.srv.store.Dir(), p.secret)
}

// A hundred changed paths made of parts of the tree's secret, the list
// within its byte limit as the tree names them: the label and the capture
// each keep all of them, scrubbed, within the limit and not cut.
func TestChangedPathsOfPartsOfASecretStayWithinTheirLimit(t *testing.T) {
	p := previewEnv(t, nil)
	s := p.secret
	size := len("[]") + 99
	for i := range 100 {
		name := fmt.Sprintf("%03d", i)
		for j := 0; len(name) < 150; j += 3 {
			name += "-" + s[j%56:j%56+8]
		}
		writeIn(t, p.dir, name, "x\n")
		size += len(name) + 2
	}
	if size > dispatch.MaxChangedPathsBytes || size < dispatch.MaxChangedPathsBytes-1024 {
		t.Fatalf("the paths are %d bytes as encoded", size)
	}
	l := p.label(t)
	o := originOf(created(t, p.e, p.body(t, l, button()), nil))
	for name, read := range map[string]map[string]any{"the label": l, "the capture": o} {
		paths, _ := json.Marshal(read["changedPaths"])
		if read["changed"] != float64(100) || len(read["changedPaths"].([]any)) != 100 || read["changedCut"] != false ||
			len(paths) > dispatch.MaxChangedPathsBytes || sharedRun(paths, s) != "" {
			t.Errorf("%s: changed %v, %d paths kept, cut %v, %d bytes", name, read["changed"], len(read["changedPaths"].([]any)), read["changedCut"], len(paths))
		}
	}
}

// A text that is hexadecimal throughout, 2 MiB of short parts (8 to 12
// characters) and wholes of a secret between other runs, is no longer once
// scrubbed; a whole secret is its name, a part the part's word.
func TestAScrubbedTextIsNoLonger(t *testing.T) {
	sum := sha256.Sum256([]byte("a secret"))
	secret := hex.EncodeToString(sum[:])
	var b strings.Builder
	for i := 0; b.Len() < 2<<20; i++ {
		other := sha256.Sum256(binary32(uint32(i)))
		start := i % 53
		b.WriteString(secret[start : start+8+i%5])
		b.WriteString(hex.EncodeToString(other[:3]))
		if i%7 == 0 {
			b.WriteString(strings.ToUpper(secret))
		}
	}
	in := b.String()
	if strings.Trim(in, "0123456789abcdefABCDEF") != "" {
		t.Fatal("the text is not hexadecimal throughout")
	}
	out := in
	scrubSecrets(&out, []string{secret})
	if len(out) > len(in) || out == in || sharedRun([]byte(out), secret) != "" {
		t.Fatalf("scrubbed: %d bytes of %d", len(out), len(in))
	}
	for text, want := range map[string]string{"k=" + secret + ".": "k=" + treeSecretName + ".", "k=" + secret[3:40] + ".": "k=" + treeSecretPart + ".",
		secret[:7]: secret[:7]} {
		got := text
		scrubSecrets(&got, []string{secret})
		if got != want {
			t.Errorf("%q scrubbed is %q, want %q", text, got, want)
		}
	}
}

// An operation's id that holds a part of a tree's secret is refused 400
// on a create, an edit and a delete, before any call to the store: no
// file of the store, the write-ahead file among them, holds 8 characters
// in a row of the secret.
func TestAnOperationsIDHoldingASecretIsRefused(t *testing.T) {
	p := previewEnv(t, nil)
	id := created(t, p.e, p.body(t, p.label(t), button()), nil)["id"].(string)
	marked := "op-" + strings.ToUpper(p.secret[20:32])
	b := p.body(t, p.label(t), button())
	b.OpID = marked
	status, out := postCapture(t, p.e, b, nil)
	answers := map[string]string{"the create": fmt.Sprintf("%d %s %s", status, codeOf(out), apiMessage(out))}
	for name, w := range map[string]struct {
		path string
		body map[string]any
	}{
		"the edit":   {"/edit", map[string]any{"opId": marked, "expectedRev": 1, "annotations": []map[string]any{{"k": 1, "kind": "element", "note": "x"}}}},
		"the delete": {"/delete", map[string]any{"opId": marked}},
	} {
		status, _, out := p.e.callAs(t, adminToken, "POST", "/api/captures/"+id+w.path, w.body)
		answers[name] = fmt.Sprintf("%d %s %s", status, codeOf(out), apiMessage(out))
	}
	for name, got := range answers {
		if got != "400 invalid_request opId holds a tree's secret" {
			t.Errorf("%s: %s", name, got)
		}
	}
	keptNoPart(t, p.e.srv.store.Dir(), p.secret)
}

// The look for the run's repository is inside the capture's read: the
// server stopped while it is held (the read's time limit the test's, so
// only the stop ends it) cancels it, waits for it and keeps nothing; and a
// capture starts no git once its read has returned, by the count of the
// trees' runner and of the look.
func TestACapturesLookForItsRepositoryIsInItsRead(t *testing.T) {
	p := previewEnv(t, nil)
	var after atomic.Bool
	var late atomic.Int64
	count := func() {
		if after.Load() {
			late.Add(1)
		}
	}
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		count()
		return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
	}))
	looks := atomic.Int64{}
	p.e.srv.repoTop = func(ctx context.Context, dir string) (string, error) {
		count()
		looks.Add(1)
		return gitcli.TopOf(ctx, dir)
	}
	p.e.srv.afterCaptureRead = func() { after.Store(true) }
	c := created(t, p.e, p.body(t, p.label(t), withHints(filepath.Join(p.dir, "README"))), nil)
	if top := gitIn(t, p.e.root, "rev-parse", "--show-toplevel"); c["scope"].(map[string]any)["repo"] != top || looks.Load() != 1 || late.Load() != 0 {
		t.Fatalf("the scope %v (the top %s), %d looks, %d git commands after the read", c["scope"], top, looks.Load(), late.Load())
	}
	p.e.srv.afterCaptureRead = nil
	held, ended, goOn := make(chan struct{}), make(chan error, 1), make(chan struct{})
	var returned atomic.Bool
	p.e.srv.trees.limit = context.WithCancel
	p.e.srv.repoTop = func(ctx context.Context, dir string) (string, error) {
		close(held)
		<-ctx.Done()
		ended <- ctx.Err()
		<-goOn
		returned.Store(true)
		return "", ctx.Err()
	}
	b := p.body(t, p.label(t), button())
	b.Annotations[0].Note = "held-in-the-look"
	done := p.postAsync(t, b)
	select {
	case <-held:
	case a := <-done:
		t.Fatalf("answered before its look was held: %d %s %v", a.status, a.body, a.err)
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
		if len(tr.reads) != 0 {
			atRemoval = append(atRemoval, fmt.Sprintf("%d reads registered", len(tr.reads)))
		}
		if !returned.Load() {
			atRemoval = append(atRemoval, "the held look had not returned")
		}
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// The budget only bounds a hang: the held look holds the stop.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		p.e.srv.Shutdown(ctx)
	}()
	<-waits
	close(goOn)
	<-stopped
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Errorf("the held look's context ended with %v, want a cancellation", err)
	}
	if atRemoval != nil {
		t.Errorf("at the removal: %v", atRemoval)
	}
	a := <-done
	var out map[string]any
	_ = json.Unmarshal(a.body, &out)
	if a.err != nil || a.status != http.StatusServiceUnavailable || codeOf(out) != "tree_unread" {
		t.Fatalf("the capture: %d %s %v", a.status, a.body, a.err)
	}
	if found := hasSecret(t, filepath.Join(p.e.srv.store.Dir(), "dispatch"), "held-in-the-look"); len(found) > 0 {
		t.Fatalf("the capture is kept in %v", found)
	}
}

// A hint whose path begins with a colon is not handed to git: its class is
// unknown, the tracked file beside it classed, and no git command of the
// read names it.
func TestAHintFromAColonIsNotHandedToGit(t *testing.T) {
	p := previewEnv(t, nil)
	var named atomic.Bool
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, ":") }) {
			named.Store(true)
		}
		return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
	}))
	c := created(t, p.e, p.body(t, p.label(t), withHints(filepath.Join(p.dir, "README"), ":README")), nil)
	if got := fmt.Sprint(classesOf(c)); got != "[README:tracked :README:unknown]" || named.Load() {
		t.Fatalf("the sources: %s; a git command named the colon's path: %v", got, named.Load())
	}
}

// goroutineIn reports whether a goroutine is blocked in a select inside
// the function named fn, as the runtime's stacks show it.
func goroutineIn(fn string) bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for _, g := range strings.Split(string(buf), "\n\n") {
		if head, _, _ := strings.Cut(g, "\n"); strings.Contains(head, "[select") && strings.Contains(g, fn) {
			return true
		}
	}
	return false
}

// A capture and a label of one tree at once: the tree's entry counts both
// (reserved), and while the capture's git is held at the runner's seam the
// label waits at the entry's gate, its own git not started, and is
// answered once the capture's read is over.
func TestACaptureAndALabelOfOneTreeShareItsGate(t *testing.T) {
	p := previewEnv(t, nil)
	b := p.body(t, p.label(t), button())
	held, goOn := make(chan struct{}), make(chan struct{})
	var holding, during atomic.Bool
	var first, let sync.Once
	// The held git goes on at the latest as the test ends, before the
	// preview's run is stopped.
	release := func() { let.Do(func() { close(goOn) }) }
	t.Cleanup(release)
	t.Cleanup(crew.SetTreeGitForTest(func(ctx context.Context, workTree, objects string, env map[string]string, in io.Reader, w io.Writer, args ...string) error {
		mine := false
		first.Do(func() { mine = true })
		if !mine && holding.Load() {
			during.Store(true)
		}
		if mine {
			holding.Store(true)
			close(held)
			<-goOn
			holding.Store(false)
		}
		return gitcli.RunTree(ctx, workTree, objects, env, in, w, args...)
	}))
	tr := &p.e.srv.trees
	handed := make(chan int, 2)
	tr.seam = func(at, dir string) {
		if at == "handed" {
			tr.mu.Lock()
			handed <- tr.entries[dir].reserved
			tr.mu.Unlock()
		}
	}
	captured := p.postAsync(t, b)
	<-handed
	<-held
	labelled := p.labelAsync()
	if n := <-handed; n != 2 {
		t.Errorf("the entry counted %d reads, want the capture's and the label's", n)
	}
	// The label waits at the gate, seen in the runtime's stacks: polled,
	// and answered meanwhile only when the gate let it through.
	for !goroutineIn("(*treeReads).take") {
		select {
		case a := <-labelled:
			t.Fatalf("the label was answered while the capture's git was held: %d %s", a.status, a.body)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if during.Load() {
		t.Error("the label started git while the capture's was held")
	}
	release()
	if a := <-captured; a.status != http.StatusCreated {
		t.Fatalf("the capture: %d %s %v", a.status, a.body, a.err)
	}
	if a := <-labelled; a.status != http.StatusOK || during.Load() {
		t.Fatalf("the label: %d %s; git started while held %v", a.status, a.body, during.Load())
	}
}

// A capture's step that fails, that panics and that outlives the read's
// time limit: the read answers the commit alone and the step's error, the
// panic goes on to the read's caller, the limit's end answers the commit
// alone and the context's error; after each no read is registered, the
// tree's entry is free (no reservation, its gate open) and the next read
// reads the tree whole.
func TestACapturesStepThatFailsPanicsOrRunsPastItsLimit(t *testing.T) {
	p := previewEnv(t, nil)
	wp, ok := p.e.srv.runs.WorktreePreview(p.run, "build", 1)
	if !ok {
		t.Fatal("no preview")
	}
	tr := &p.e.srv.trees
	failed := errors.New("the step failed")
	var ended context.CancelFunc
	tr.limit = func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		ended = cancel
		return ctx, cancel
	}
	for name, step := range map[string]func(ctx context.Context, t *crew.Tree, ix *crew.TreeIndex, commit string) error{
		"fails":  func(context.Context, *crew.Tree, *crew.TreeIndex, string) error { return failed },
		"panics": func(context.Context, *crew.Tree, *crew.TreeIndex, string) error { panic(failed) },
		"outlives its limit": func(ctx context.Context, _ *crew.Tree, _ *crew.TreeIndex, _ string) error {
			ended()
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		var read crew.TreeRead
		var err error
		var panicked any
		func() {
			defer func() { panicked = recover() }()
			read, err = tr.read(t.Context(), wp, step)
		}()
		switch name {
		case "fails":
			if read.Commit == "" || read.Content != "" || !errors.Is(err, failed) {
				t.Errorf("%s: %+v %v", name, read, err)
			}
		case "panics":
			if panicked != failed {
				t.Errorf("%s: recovered %v", name, panicked)
			}
		default:
			if read.Commit == "" || read.Content != "" || !errors.Is(err, context.Canceled) || readFailure(err) != "late" {
				t.Errorf("%s: %+v %v", name, read, err)
			}
		}
		tr.mu.Lock()
		e := tr.entries[wp.Dir]
		if len(tr.reads) != 0 || e.reserved != 0 || len(e.gate) != 0 {
			t.Errorf("%s: %d reads registered, the entry reserved %d, its gate held %d", name, len(tr.reads), e.reserved, len(e.gate))
		}
		tr.mu.Unlock()
		if whole, err := tr.read(t.Context(), wp, nil); err != nil || whole.Content == "" {
			t.Errorf("%s: the next read: %+v %v", name, whole, err)
		}
	}
}

// A capture of a tree whose committed attributes file is gone is kept
// with its commit alone (201): no content and no changed paths, and the
// log says so.
func TestACaptureOfATreeWithoutItsAttributesFileKeepsTheCommit(t *testing.T) {
	logs := &logBuffer{}
	p := previewEnvLogging(t, map[string]string{".gitattributes": "*.txt text\n"}, logs)
	l := p.label(t)
	if err := os.Remove(filepath.Join(p.dir, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	o := originOf(created(t, p.e, p.body(t, l, button()), nil))
	if o["commit"] != l["commit"] || o["content"] != nil || len(o["changedPaths"].([]any)) != 0 {
		t.Fatalf("the origin: %v", o)
	}
	if !strings.Contains(logs.String(), "a capture's tree was read to its commit alone") {
		t.Fatalf("the log: %s", logs.String())
	}
}
