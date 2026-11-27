package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocksolidlabs/conductor/ext"
	"github.com/rocksolidlabs/conductor/internal/config"
	"github.com/rocksolidlabs/conductor/internal/export"
)

// putUIDoc puts body as the UI document name and answers the reply.
func putUIDoc(t *testing.T, e *testEnv, name string, body any, status int) map[string]any {
	t.Helper()
	b, ok := body.([]byte)
	if !ok {
		b, _ = json.Marshal(body)
	}
	got, _, data, out := e.send(t, "PUT", "/api/ui/"+name, b, "application/json")
	if got != status {
		t.Fatalf("PUT %s: %d %.300s", name, got, data)
	}
	return out
}

// uiFile is the document as the data directory holds it.
func uiFile(t *testing.T, e *testEnv, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "ui", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var (
	testLayout  = map[string]any{"v": 1, "editor": map[string]any{"layout": "side", "sideSplit": 0.6}, "sidebar": map[string]any{"mode": "rail", "width": 18.5, "folds": map[string]any{"exited": true, "pinned": false}}, "crewGraph": map[string]any{"direction": "TB", "handoffs": false}, "runView": "graph", "projectView": "board"}
	testPalette = map[string]any{"v": 1, "recents": map[string]any{"go:/dispatch": map[string]any{"count": 3, "last": 1_700_000_000_000}}}
	testPins    = map[string]any{"v": 1, "pins": []any{map[string]any{"session": "s-1", "name": "fix the tests", "agent": "claude", "at": 1_700_000_000_000}}}
)

// The three documents' routes: none answers {}; a put answers the
// document as kept, with at and by, which a get then answers; a body past
// its bound is 413, one with an unknown field, past its schema or of a
// name not one of the three 400; without a data directory 503.
func TestUIStateRoutes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for name, body := range map[string]any{uiLayout: testLayout, uiPalette: testPalette, uiPins: testPins} {
		if out := e.expect(t, "GET", "/api/ui/"+name, nil, http.StatusOK, ""); len(out) != 0 {
			t.Fatalf("%s before a put: %v", name, out)
		}
		out := putUIDoc(t, e, name, body, http.StatusOK)
		if out["v"] != 1.0 || out["at"] == nil || !uiWriterPattern.MatchString(out["by"].(string)) {
			t.Fatalf("%s as kept: %v", name, out)
		}
		got := e.expect(t, "GET", "/api/ui/"+name, nil, http.StatusOK, "")
		if g, _ := json.Marshal(got); string(g) != string(must(json.Marshal(out))) {
			t.Fatalf("%s read back: %s, put %v", name, g, out)
		}
	}
	if out := e.expect(t, "GET", "/api/ui/layout", nil, http.StatusOK, ""); out["runView"] != "graph" || out["editor"].(map[string]any)["sideSplit"] != 0.6 {
		t.Fatalf("the layout: %v", out)
	}
	for _, c := range []struct {
		name string
		body any
		want string
	}{
		{"layout", map[string]any{"v": 1, "theme": "dark"}, `unknown field "theme"`},
		{"layout", map[string]any{"v": 1, "at": "2026-11-19T00:00:00Z"}, `unknown field "at"`},
		{"layout", map[string]any{"v": 1, "editor": map[string]any{"split": 1.5}}, "fractions"},
		{"layout", map[string]any{"v": 1, "sidebar": map[string]any{"folds": map[string]any{"elsewhere": true}}}, "no section"},
		{"layout", map[string]any{"v": 1, "runView": "/home"}, "runView"},
		{"layout", map[string]any{"v": 2}, "v is 1"},
		{"palette", map[string]any{"v": 1, "recents": map[string]any{strings.Repeat("k", uiMaxRecentKey+1): map[string]any{"count": 1, "last": 1}}}, "key"},
		{"pins", map[string]any{"v": 1, "pins": []any{map[string]any{"session": "../x", "name": "", "agent": "", "at": 1}}}, "session"},
		{"pins", map[string]any{"v": 1, "pins": []any{testPins["pins"].([]any)[0], testPins["pins"].([]any)[0]}}, "twice"},
		{"windows", map[string]any{"v": 1}, "no UI document"},
	} {
		out := putUIDoc(t, e, c.name, c.body, http.StatusBadRequest)
		if errorCode(out) != "invalid_request" || !strings.Contains(apiMessage(out), c.want) {
			t.Errorf("%s %v: %v", c.name, c.body, out)
		}
	}
	many := map[string]any{}
	for i := range uiMaxRecents + 1 {
		many["go:/"+strings.Repeat("p", i%50)+string(rune('a'+i%26))+strings.Repeat("q", i/26)] = map[string]any{"count": 1, "last": i}
	}
	if out := putUIDoc(t, e, "palette", map[string]any{"v": 1, "recents": many}, http.StatusBadRequest); !strings.Contains(apiMessage(out), "more than 200") {
		t.Fatalf("too many recents: %v", out)
	}
	pins := []any{}
	for i := range uiMaxPins + 1 {
		pins = append(pins, map[string]any{"session": "s" + strings.Repeat("x", i), "name": "", "agent": "", "at": 1})
	}
	if out := putUIDoc(t, e, "pins", map[string]any{"v": 1, "pins": pins}, http.StatusBadRequest); !strings.Contains(apiMessage(out), "more than 20") {
		t.Fatalf("too many pins: %v", out)
	}
	big := []byte(`{"v":1,"runView":"` + strings.Repeat("x", maxUIDoc) + `"}`)
	if out := putUIDoc(t, e, "layout", big, http.StatusRequestEntityTooLarge); errorCode(out) != "bound" {
		t.Fatalf("past its bound: %v", out)
	}
	if out := e.expect(t, "GET", "/api/ui/layout", nil, http.StatusOK, ""); out["runView"] != "graph" {
		t.Fatalf("a refused put changed the layout: %v", out)
	}

	srv, err := New(e.srv.cfg, e.srv.base, e.srv.log, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	none := e.serve(srv)
	for _, m := range []string{"GET", "PUT"} {
		if resp, out := none.do(m, "/api/ui/layout", adminToken, testLayout); resp.StatusCode != http.StatusServiceUnavailable || errorCode(out) != "store_unavailable" {
			t.Errorf("%s without a data directory: %d %v", m, resp.StatusCode, out)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// A put that changes nothing keeps the document's at and by and writes
// nothing (the data directory commits nothing): a replay of an unchanged
// document is never newer than one another machine wrote since.
func TestUIStateKeepsAtAndByOnReplay(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	first := putUIDoc(t, e, "layout", testLayout, http.StatusOK)
	// The document's own file is what is judged, never the data
	// directory's count of commits: the server commits files of its own
	// beside a test's.
	path := filepath.Join(e.srv.store.Dir(), "ui", "layout.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again := putUIDoc(t, e, "layout", testLayout, http.StatusOK)
	if again["at"] != first["at"] || again["by"] != first["by"] {
		t.Fatalf("a put of the same content moved the stamp: %v, was %v", again, first)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("a put of the same content wrote the document again: %v, was %v", after.ModTime(), before.ModTime())
	}
	changed := map[string]any{"v": 1, "runView": "tasks"}
	next := putUIDoc(t, e, "layout", changed, http.StatusOK)
	if next["at"] == first["at"] || next["by"] != first["by"] {
		t.Fatalf("a change kept the stamp: %v", next)
	}
}

// laterDoc is the document content stamped at, by by, as another machine
// left it.
func laterDoc(t *testing.T, content map[string]any, at time.Time, by string) []byte {
	t.Helper()
	m := map[string]any{}
	for k, v := range content {
		m[k] = v
	}
	m["at"], m["by"] = at, by
	return must(json.Marshal(m))
}

// An edit made after a document arrived from a machine whose clock runs
// an hour ahead is still the newest: its at is past the held one's.
func TestUIEditAfterALaterDatedDocumentIsNewest(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	ahead := time.Now().UTC().Add(time.Hour)
	if err := e.srv.setUIFromElsewhere("layout", laterDoc(t, testLayout, ahead, strings.Repeat("f", 32))); err != nil {
		t.Fatal(err)
	}
	out := putUIDoc(t, e, "layout", map[string]any{"v": 1, "runView": "timeline"}, http.StatusOK)
	at, err := time.Parse(time.RFC3339Nano, out["at"].(string))
	if err != nil || !at.After(ahead) {
		t.Fatalf("the edit's at %v, the held one %v: %v", out["at"], ahead, err)
	}
}

// The sync store's door keeps a layout or palette as another machine left
// it, at and by kept, and refuses an older one (or the same) with
// ErrRefused, and pins whatever their stamp; every other Host answers
// ErrUnsupported.
func TestSetUIStateRefusesAnOlderDocument(t *testing.T) {
	ctx := context.Background()
	e, own := syncDoorsEnv(t, newFakeSync())
	other := &extHost{s: e.srv, name: "another", ctx: ctx}
	desk := &extHost{s: e.srv, name: "cloud", ctx: ctx, desk: true}
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	doc := laterDoc(t, testLayout, t0, strings.Repeat("a", 32))
	for _, h := range []*extHost{other, desk} {
		if err := h.SetUIState(ctx, "layout", doc); !errors.Is(err, ext.ErrUnsupported) {
			t.Fatalf("another Host's door: %v", err)
		}
		if err := h.SyncReplay(ctx); !errors.Is(err, ext.ErrUnsupported) {
			t.Fatalf("another Host's replay: %v", err)
		}
	}
	if err := own.SetUIState(ctx, "layout", doc); err != nil {
		t.Fatal(err)
	}
	if got := uiFile(t, e, "layout"); got["at"] != t0.Format(time.RFC3339Nano) || got["by"] != strings.Repeat("a", 32) || got["runView"] != "graph" {
		t.Fatalf("the layout as kept: %v", got)
	}
	for name, d := range map[string][]byte{
		"the same":                     doc,
		"an older one":                 laterDoc(t, map[string]any{"v": 1, "runView": "tasks"}, t0.Add(-time.Second), strings.Repeat("f", 32)),
		"one of the same time, lesser": laterDoc(t, map[string]any{"v": 1, "runView": "tasks"}, t0, strings.Repeat("0", 32)),
	} {
		if err := own.SetUIState(ctx, "layout", d); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "an older document") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got := uiFile(t, e, "layout"); got["runView"] != "graph" {
		t.Fatalf("a refused document was kept: %v", got)
	}
	if err := own.SetUIState(ctx, "layout", laterDoc(t, map[string]any{"v": 1, "runView": "tasks"}, t0, strings.Repeat("b", 32))); err != nil {
		t.Fatalf("one of the same time, greater: %v", err)
	}
	for name, d := range map[string][]byte{
		"pins":    laterDoc(t, testPins, t0.Add(time.Hour), strings.Repeat("c", 32)),
		"palette": laterDoc(t, map[string]any{"v": 1, "recents": map[string]any{}, "extra": 1}, t0.Add(time.Hour), strings.Repeat("c", 32)),
		"layout":  []byte(`{"v":1,"at":"2026-11-19T00:00:00Z"}`),
	} {
		if err := own.SetUIState(ctx, name, d); !errors.Is(err, ext.ErrRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The three documents are on the sync store's allowlist: each put is
// handed over at its commit and each is listed in a replay; what leaves
// of the palette and the pins is narrower (TestPaletteDeliveredWithoutPaths),
// and the writer's id never leaves.
func TestUIStateOnTheAllowlist(t *testing.T) {
	f := newFakeSync()
	e := syncEnv(t, syncDataDir(t), f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	for name, body := range map[string]any{uiLayout: testLayout, uiPalette: testPalette, uiPins: testPins} {
		putUIDoc(t, e, name, body, http.StatusOK)
	}
	calls := f.until(t, "the three documents", func(c []syncCall) bool {
		k := keysOf(c, "")
		return slices.Contains(k, syncKey{syncData, "ui/layout.json"}) && slices.Contains(k, syncKey{syncData, "ui/palette.json"}) &&
			slices.Contains(k, syncKey{syncData, "ui/pins.json"})
	})
	model := syncModel(calls)
	var layout map[string]any
	if err := json.Unmarshal(model[syncKey{syncData, "ui/layout.json"}], &layout); err != nil || layout["runView"] != "graph" || layout["by"] == nil {
		t.Fatalf("the layout handed over: %s %v", model[syncKey{syncData, "ui/layout.json"}], err)
	}
	for _, c := range calls {
		if strings.HasSuffix(c.d.Path, "writer") {
			t.Fatalf("%s reached the sync store", c.d.Path)
		}
	}
	// A replay lists them.
	e.srv.syncs.requestReplay()
	calls = f.until(t, "the second replay", func(c []syncCall) bool { return replayedIn(c, 2) })
	keys := keysOf(calls, nthEpoch(calls, 2))
	for _, want := range []string{"ui/layout.json", "ui/palette.json", "ui/pins.json"} {
		if !slices.Contains(keys, syncKey{syncData, want}) {
			t.Fatalf("the replay did not list %s: %v", want, keys)
		}
	}
}

// What leaves of the palette is the recents of five kinds (a page, a
// ticket, a crew, a run, a built-in action): every other kind (a file's
// path, a joined link, a session, an artifact, a member, an answer-all)
// stays; what leaves of the pins is each pin with its name when the
// copied-output check judged it clean (TestPinNameUnderTheOutputCheck),
// and never the verdict. The server's own documents keep everything.
func TestPaletteDeliveredWithoutPaths(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	kinds := []string{"go:/dispatch", "ticket:CND-1", "crew:todo", "run:team-1", "action:theme", "open:/home/someone/notes.md",
		"touched:/work/secret.txt", "shared:https://x.example.com/j/abc", "session:s-1", "artifact:a1", "member:m1", "answer-all:q", "later:thing"}
	recents := map[string]any{}
	for i, k := range kinds {
		recents[k] = map[string]any{"count": i + 1, "last": 1_700_000_000_000 + i}
	}
	putUIDoc(t, e, "palette", map[string]any{"v": 1, "recents": recents}, http.StatusOK)
	putUIDoc(t, e, "pins", testPins, http.StatusOK)
	held, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "ui", "palette.json"))
	if err != nil {
		t.Fatal(err)
	}
	var kept, out uiPaletteDoc
	_ = json.Unmarshal(held, &kept)
	if len(kept.Recents) != len(kinds) {
		t.Fatalf("the server keeps %d recents of %d", len(kept.Recents), len(kinds))
	}
	if err := json.Unmarshal(deliveredUIDoc("ui/palette.json", held), &out); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for k := range out.Recents {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := []string{"action:theme", "crew:todo", "go:/dispatch", "run:team-1", "ticket:CND-1"}; !slices.Equal(got, want) {
		t.Fatalf("the recents delivered: %v, want %v", got, want)
	}
	heldPins, _ := os.ReadFile(filepath.Join(e.srv.store.Dir(), "ui", "pins.json"))
	var pins uiPinsDoc
	if err := json.Unmarshal(deliveredUIDoc("ui/pins.json", heldPins), &pins); err != nil || len(pins.Pins) != 1 || pins.Pins[0].Name != "fix the tests" || pins.Pins[0].NameVerdict != "" ||
		pins.Pins[0].Session != "s-1" || pins.Pins[0].Agent != "claude" {
		t.Fatalf("the pins delivered: %+v %v", pins, err)
	}
	if !bytes.Contains(heldPins, []byte("fix the tests")) {
		t.Fatal("the server's pins lost a name")
	}
}

// An export carries the three documents, the pins with their names, and
// leaves the writer's id out as this machine's own (instance).
func TestPinsInTheExport(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for name, body := range map[string]any{uiLayout: testLayout, uiPalette: testPalette, uiPins: testPins} {
		putUIDoc(t, e, name, body, http.StatusOK)
	}
	resp, b := getExport(t, e, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the export: %d %.200s", resp.StatusCode, b)
	}
	paths := exportMatches(t, b, e.srv.store.Dir())
	for _, want := range []string{"ui/layout.json", "ui/palette.json", "ui/pins.json"} {
		if !paths[want] {
			t.Fatalf("the archive lacks %s: %v", want, paths)
		}
	}
	if paths["ui/writer"] {
		t.Fatal("the archive holds the writer's id")
	}
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range zr.File {
		if f.Name == export.DataDir+"ui/pins.json" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if !bytes.Contains(data, []byte("fix the tests")) {
				t.Fatalf("the pins exported without their names: %s", data)
			}
		}
		if f.Name == export.ManifestName {
			rc, _ := f.Open()
			var m export.Manifest
			_ = json.NewDecoder(rc).Decode(&m)
			rc.Close()
			if !slices.Contains(m.Left, export.Left{Path: "ui/writer", Why: export.WhyInstance}) {
				t.Fatalf("the manifest's left out: %+v", m.Left)
			}
		}
	}
}

// A replay the sync store asks for is a new epoch listing every document,
// with no document it hands changing: the UI documents and the writer's
// id, read by name before and after, the same bytes on disk and handed
// over alike. Other parts of the server write the data directory as they
// will meanwhile (another document is saved as the replay begins, through
// the adapter's afterReset), which says nothing of the replay.
func TestReplayDoorReplaysEveryDocWithoutAChange(t *testing.T) {
	f := newFakeSync()
	e, own := syncDoorsEnv(t, f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	putUIDoc(t, e, "layout", testLayout, http.StatusOK)
	f.until(t, "the layout", func(c []syncCall) bool { return slices.Contains(keysOf(c, ""), syncKey{syncData, "ui/layout.json"}) })
	read := func() map[string][]byte {
		out := map[string][]byte{}
		for _, name := range []string{"layout.json", uiWriterFile} {
			b, err := os.ReadFile(filepath.Join(e.srv.store.Dir(), "ui", name))
			if err != nil {
				t.Fatal(err)
			}
			out[name] = b
		}
		return out
	}
	before := read()
	handed := syncModel(f.all())[syncKey{syncData, "ui/layout.json"}]
	crews, err := e.srv.store.Sub("crews")
	if err != nil {
		t.Fatal(err)
	}
	e.srv.syncs.mu.Lock()
	e.srv.syncs.afterReset = func() {
		if err := crews.Save("another-writer.json", map[string]string{"id": "another-writer", "name": "Another writer"}); err != nil {
			t.Error(err)
		}
	}
	e.srv.syncs.mu.Unlock()
	if err := own.SyncReplay(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls := f.until(t, "the asked replay", func(c []syncCall) bool { return replayedIn(c, 2) })
	for name, b := range read() {
		if !bytes.Equal(b, before[name]) {
			t.Fatalf("the replay changed ui/%s", name)
		}
	}
	if !slices.Contains(keysOf(calls, nthEpoch(calls, 2)), syncKey{syncData, "ui/layout.json"}) {
		t.Fatal("the replay did not list the layout")
	}
	if after := syncModel(calls)[syncKey{syncData, "ui/layout.json"}]; !bytes.Equal(after, handed) {
		t.Fatalf("the layout handed over changed across the replay:\n%s\n%s", handed, after)
	}
}

// Replay requests coalesce: with one replay running (its marker held at
// the sync store), fifty requests are one waiting, so the asked replay
// and one more run, and no third.
func TestReplayRequestsCoalesce(t *testing.T) {
	f := newFakeSync()
	e, own := syncDoorsEnv(t, f)
	f.until(t, "the start's replay", func(c []syncCall) bool { return replayedIn(c, 1) })
	release, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.set(func(f *fakeSync) {
		f.gate = func(op string, d ext.Doc) <-chan struct{} {
			if d.Store != syncReplay {
				return nil
			}
			once.Do(func() { close(held) })
			return release
		}
	})
	if err := own.SyncReplay(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, held, "the asked replay's marker at the sync store")
	for range 50 {
		if err := own.SyncReplay(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	f.until(t, "the replay the requests waited for", func(c []syncCall) bool { return replayedIn(c, 3) })
	waitPending(t, e.srv.syncs, 0)
	if epochs := countEpochs(f.all()); epochs != 3 {
		t.Fatalf("%d replays: the start's, the asked one and %d for fifty requests", epochs, epochs-2)
	}
}

// countEpochs is how many replays the calls begin.
func countEpochs(calls []syncCall) int {
	n := 0
	for n < len(calls) && nthEpoch(calls, n+1) != "" {
		n++
	}
	return n
}

// A UI document is bounded as its file holds it, at both writers: a
// palette whose keys grow as stored (each & escaped, every recent
// indented) is refused 413 by the PUT and by another machine's write, and
// the next read still answers. A file past the bound, written before the
// bound was kept at the writers, is set aside and read as none, and the
// next PUT keeps the person's document.
func TestAUIDocumentIsBoundedAsItIsStored(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.DataDir = t.TempDir() })
	recents := map[string]any{}
	for i := range 200 {
		recents[fmt.Sprintf("go:/dispatch?q=%03d&%s", i, strings.Repeat("&", 200))] = map[string]any{"count": 1, "last": 1_700_000_000_000 + i}
	}
	doc := map[string]any{"v": 1, "recents": recents}
	var sent bytes.Buffer
	enc := json.NewEncoder(&sent)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil || sent.Len() > maxUIDoc {
		t.Fatalf("the document sent is %d bytes, past the PUT's bound: %v", sent.Len(), err)
	}
	out := putUIDoc(t, e, "palette", sent.Bytes(), http.StatusRequestEntityTooLarge)
	if !strings.Contains(apiMessage(out), "stored") {
		t.Fatalf("the refusal: %v", out)
	}
	// The same compact document from another machine, stamped: within the
	// input's bound, past the stored one.
	from := bytes.TrimSuffix(bytes.TrimSpace(sent.Bytes()), []byte("}"))
	from = append(from, fmt.Sprintf(`,"at":%q,"by":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), strings.Repeat("c", 32))...)
	if len(from) > maxUIDoc {
		t.Fatalf("another machine's document is %d bytes", len(from))
	}
	if err := e.srv.setUIFromElsewhere("palette", from); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "stored") {
		t.Fatalf("another machine's document past the bound: %v", err)
	}
	e.expect(t, http.MethodGet, "/api/ui/palette", nil, http.StatusOK, "")
	dir := filepath.Join(e.srv.store.Dir(), "ui")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "palette.json"), bytes.Repeat([]byte(" "), maxUIStored+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.expect(t, http.MethodGet, "/api/ui/palette", nil, http.StatusOK, ""); len(got) != 0 {
		t.Fatalf("a file past the bound read as %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "palette.json.aside")); err != nil {
		t.Fatalf("the file past the bound not set aside: %v", err)
	}
	putUIDoc(t, e, "palette", testPalette, http.StatusOK)
	if got := uiFile(t, e, "palette"); got["recents"] == nil {
		t.Fatalf("the next PUT's document: %v", got)
	}
}

// The layout keeps the run page's last choice for its members' tiles
// (runTiles): each of the four layouts, with Activity on and with it
// off, is kept and answered as put; an unknown layout, a member runTiles
// does not have and a value of the wrong type are refused, and the
// document kept stays; a document past the bound is refused with it.
func TestUIStateKeepsTheRunTiles(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	for _, layout := range []string{"grid", "columns", "rows", "large"} {
		for _, activity := range []bool{true, false} {
			putUIDoc(t, e, "layout", map[string]any{"v": 1, "runView": "grid", "runTiles": map[string]any{"layout": layout, "activity": activity}}, http.StatusOK)
			got := e.expect(t, "GET", "/api/ui/layout", nil, http.StatusOK, "")
			if rt, _ := got["runTiles"].(map[string]any); rt["layout"] != layout || rt["activity"] != activity || got["runView"] != "grid" {
				t.Fatalf("%s with activity %v read back: %v", layout, activity, got)
			}
		}
	}
	for _, c := range []struct {
		body any
		want string
	}{
		{map[string]any{"v": 1, "runTiles": map[string]any{"layout": "focus"}}, "runTiles.layout is grid, columns, rows or large"},
		{map[string]any{"v": 1, "runTiles": map[string]any{"layout": "grid", "large": "core"}}, `unknown field "large"`},
		{map[string]any{"v": 1, "runTiles": map[string]any{"activity": "yes"}}, "cannot unmarshal"},
		{map[string]any{"v": 1, "runTiles": map[string]any{"layout": 2}}, "cannot unmarshal"},
		{map[string]any{"v": 1, "runTiles": "grid"}, "cannot unmarshal"},
	} {
		out := putUIDoc(t, e, "layout", c.body, http.StatusBadRequest)
		if errorCode(out) != "invalid_request" || !strings.Contains(apiMessage(out), c.want) {
			t.Errorf("%v: %v", c.body, out)
		}
	}
	if rt, _ := uiFile(t, e, "layout")["runTiles"].(map[string]any); rt["layout"] != "large" || rt["activity"] != false {
		t.Fatalf("a refused layout changed the one kept: %v", rt)
	}
	big := []byte(`{"v":1,"runTiles":{"layout":"rows","activity":true},"runView":"` + strings.Repeat("x", maxUIDoc) + `"}`)
	if out := putUIDoc(t, e, "layout", big, http.StatusRequestEntityTooLarge); errorCode(out) != "bound" {
		t.Fatalf("past its bound: %v", out)
	}
}

// A layout another machine left that holds runTiles is taken through the
// sync store's door, and one whose runTiles is past its schema is refused.
func TestALayoutFromElsewhereKeepsItsRunTiles(t *testing.T) {
	ctx := context.Background()
	e, own := syncDoorsEnv(t, newFakeSync())
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	doc := laterDoc(t, map[string]any{"v": 1, "runTiles": map[string]any{"layout": "columns", "activity": true}}, t0, strings.Repeat("a", 32))
	if err := own.SetUIState(ctx, "layout", doc); err != nil {
		t.Fatal(err)
	}
	if rt, _ := uiFile(t, e, "layout")["runTiles"].(map[string]any); rt["layout"] != "columns" || rt["activity"] != true {
		t.Fatalf("the layout as kept: %v", rt)
	}
	bad := laterDoc(t, map[string]any{"v": 1, "runTiles": map[string]any{"layout": "mosaic"}}, t0.Add(time.Second), strings.Repeat("b", 32))
	if err := own.SetUIState(ctx, "layout", bad); !errors.Is(err, ext.ErrRefused) || !strings.Contains(err.Error(), "runTiles.layout") {
		t.Fatalf("a runTiles past its schema: %v", err)
	}
}
