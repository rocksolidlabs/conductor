package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"testing"
	"time"
)

// The walks of the captures (Program item PV1, docs/plans/browser-evidence.md):
// every read the store offers after a delete, and every way out of an
// upload after its file is published.

// carries is whether a result of the type can hold a capture's words: a
// capture, its document, an annotation, a change row, a receipt, a file,
// an interface, or a string or bytes the read gives whole.
func carries(t reflect.Type, top bool, seen map[reflect.Type]bool) bool {
	switch t {
	case reflect.TypeFor[Capture](), reflect.TypeFor[CaptureDocument](), reflect.TypeFor[Annotation](), reflect.TypeFor[ChangeRow](),
		reflect.TypeFor[Receipt](), reflect.TypeFor[os.File]():
		return true
	case reflect.TypeFor[error](), reflect.TypeFor[time.Time]():
		return false
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.String:
		return top
	case reflect.Interface:
		return true
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return top
		}
		return carries(t.Elem(), top, seen)
	case reflect.Pointer, reflect.Array:
		return carries(t.Elem(), top, seen)
	case reflect.Map:
		return carries(t.Key(), top, seen) || carries(t.Elem(), top, seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() && carries(f.Type, false, seen) {
				return true
			}
		}
	}
	return false
}

// storeReads is every exported method of the store that takes no
// operation (an operation's answer is its receipt, Replay's below) and
// whose results can hold a capture's words (carries).
func storeReads() []string {
	st := reflect.TypeFor[*Store]()
	var names []string
	for i := 0; i < st.NumMethod(); i++ {
		m := st.Method(i)
		op := false
		for in := 0; in < m.Type.NumIn(); in++ {
			op = op || m.Type.In(in) == reflect.TypeFor[Op]()
		}
		if op {
			continue
		}
		for o := 0; o < m.Type.NumOut(); o++ {
			if carries(m.Type.Out(o), true, map[reflect.Type]bool{}) {
				names = append(names, m.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

// A deleted capture is its tombstone, not found or left out through every
// read the store offers: each read whose results can hold a capture's
// words (storeReads) is driven after the delete or says why it holds none,
// a read added later in neither failing, and the feed's reads among them.
func TestEveryReadHidesADeletedCapture(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	img := pngOf(t, 8, 8, 40)
	n, f := testCapture(true)
	n.Title, n.URL, n.Viewport.UserAgent = "Walk title", "http://127.0.0.1:5173/walk-page", "WalkAgent/1"
	n.Annotations[0].Note = "walk note"
	f.ChangedPaths, f.Scope.Repo = []string{"walk/changed.vue"}, "/walk-repo"
	moment(&n)
	id := mustCreate(t, s, "walk-create", n, f, img)
	edits := kept(captureOf(t, s, id))
	edits[0].Note = "walk note edited"
	rc, err := reedit(t, s, "walk-edit", id, 1, edits)
	want(t, rc, err, StatusDone, "")
	rc, err = s.DeleteCapture(ctx, testOp("walk-delete"), id)
	want(t, rc, err, StatusDone, "")
	words := []string{"Walk title", "walk-page", "WalkAgent", "walk note", "walk/changed.vue", "/walk-repo", sumOf(img)}
	ops := []string{"walk-create", "walk-edit", "walk-delete"}
	enc := func(vs ...any) []byte {
		b, _ := json.Marshal(vs)
		return b
	}
	file := func(fh *os.File) []byte {
		if fh == nil {
			return nil
		}
		defer fh.Close()
		b, _ := io.ReadAll(fh)
		return b
	}
	reads := map[string]func() ([]byte, error){
		"Capture": func() ([]byte, error) {
			c, err := s.Capture(ctx, id)
			if c.DeletedAt.IsZero() || c.ID != id {
				err = fmt.Errorf("not its tombstone: %+v", c)
			}
			return enc(c), err
		},
		"Captures": func() ([]byte, error) {
			var out []byte
			for _, filter := range []CaptureFilter{{}, {Run: f.Run}, {Run: f.Run, Task: f.Task, Attempt: f.Attempt}} {
				page, err := s.Captures(ctx, filter, "", MaxCapturePage)
				if err != nil || len(page.Captures) != 0 {
					return enc(page), fmt.Errorf("listed: %+v %v", page, err)
				}
			}
			return out, nil
		},
		"ChangeValue": func() ([]byte, error) {
			feed, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
			if err != nil {
				return nil, err
			}
			var out []byte
			for _, c := range feed {
				v, err := s.ChangeValue(ctx, c.Seq)
				if err != nil {
					return nil, err
				}
				out = append(out, v...)
			}
			return out, nil
		},
		"Committed": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, _, err := s.Committed(ctx, "test", op)
				if err != nil {
					return nil, err
				}
				out = append(out, enc(rc)...)
			}
			return out, nil
		},
		"Receipt": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, _, err := s.Receipt(ctx, "test", op)
				if err != nil {
					return nil, err
				}
				out = append(out, enc(rc)...)
			}
			return out, nil
		},
		"PlanReceipt": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				rc, bound, _, err := s.PlanReceipt(ctx, "test", op, id)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return nil, err
				}
				out = append(out, enc(rc, bound)...)
			}
			return out, nil
		},
		"OpTicket": func() ([]byte, error) {
			var out []byte
			for _, op := range ops {
				k, _, err := s.OpTicket(ctx, "test", op)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return nil, err
				}
				out = append(out, k...)
			}
			return out, nil
		},
		"Document": func() ([]byte, error) {
			doc, err := s.Document(ctx, id, 2)
			if !errors.Is(err, ErrNotFound) {
				return doc, fmt.Errorf("not not found: %v", err)
			}
			return doc, nil
		},
		"OpenCaptureImage": func() ([]byte, error) {
			c, fh, err := s.OpenCaptureImage(ctx, id)
			b := append(enc(c), file(fh)...)
			if !errors.Is(err, ErrNotFound) {
				return b, fmt.Errorf("not not found: %v", err)
			}
			return b, nil
		},
		"OpenAttachment": func() ([]byte, error) {
			a, fh, err := s.OpenAttachment(ctx, id)
			b := append(enc(a), file(fh)...)
			if !errors.Is(err, ErrNotFound) {
				return b, fmt.Errorf("not not found: %v", err)
			}
			return b, nil
		},
		"OpenLaunchFile": func() ([]byte, error) {
			fh, err := s.OpenLaunchFile(ctx, id, sumOf(img))
			if err == nil {
				return file(fh), errors.New("a capture's image opened as a launch's file")
			}
			return nil, nil
		},
		"History": func() ([]byte, error) {
			page, err := s.History(ctx, key, 0, 0)
			return enc(page), err
		},
		"Finding": func() ([]byte, error) {
			v, w, err := s.Finding(ctx, id)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			return enc(v, w), nil
		},
		"LaunchActor": func() ([]byte, error) {
			a, err := s.LaunchActor(ctx, id)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			return []byte(a), nil
		},
		"Instance": func() ([]byte, error) {
			v, err := s.Instance(ctx)
			return []byte(v), err
		},
		// Beside the store's reads that carries finds: the feed's rows, a
		// resend's answer and an export's hashes.
		"ChangesAfter": func() ([]byte, error) {
			feed, err := s.ChangesAfter(ctx, 0, MaxFeedPage)
			return enc(feed), err
		},
		"Replay": func() ([]byte, error) {
			rc, _, err := s.Replay(ctx, testOp("walk-create"), IntentCaptureCreate, captureArgs{Received: digestOf(t, n, img)})
			return enc(rc), err
		},
		"SnapshotHashes": func() ([]byte, error) {
			snap := filepath.Join(t.TempDir(), "walk.sqlite")
			if err := s.Snapshot(ctx, snap); err != nil {
				return nil, err
			}
			sums, err := SnapshotHashes(ctx, snap)
			return enc(sums), err
		},
	}
	notReads := map[string]string{
		"Dir":      "the store's own directory",
		"ReadOnly": "whether the store takes writes, and the store's own words why",
	}
	for _, name := range storeReads() {
		_, driven := reads[name]
		if _, why := notReads[name]; !driven && !why {
			t.Errorf("the read %s is not in the walk: drive it after a delete, or say why it holds no capture", name)
		}
	}
	for name := range notReads {
		if !slices.Contains(storeReads(), name) {
			t.Errorf("notReads names %s, which is no read of the store's", name)
		}
	}
	names := make([]string, 0, len(reads))
	for name := range reads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got, err := reads[name]()
		if err != nil {
			t.Errorf("%s after the delete: %v", name, err)
		}
		for _, w := range words {
			if bytes.Contains(got, []byte(w)) {
				t.Errorf("%s after the delete gives %q: %.300s", name, w, got)
			}
		}
		if bytes.Contains(got, img) {
			t.Errorf("%s after the delete gives the screenshot", name)
		}
	}
}

// The image of a capture deleted and swept between the row's read and the
// file's open is not found, as an attachment's is.
func TestAnImageDeletedWhileOpeningIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	img := pngOf(t, 8, 8, 41)
	n, f := testCapture(true)
	id := mustCreate(t, s, "c1", n, f, img)
	s.beforeOpen = func() {
		s.beforeOpen = nil
		rc, err := s.DeleteCapture(ctx, testOp("d1"), id)
		want(t, rc, err, StatusDone, "")
		if err := s.sweep(ctx, time.Now()); err != nil {
			t.Error(err)
		}
	}
	_, fh, err := s.OpenCaptureImage(ctx, id)
	if fh != nil {
		fh.Close()
	}
	if exists(t, contentAt(s, sumOf(img))) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the image opened while its capture was deleted: %v (its file there %v)", err, exists(t, contentAt(s, sumOf(img))))
	}
}

// Every way out of an upload after its file is published ends in the one
// collection: a failed sync of its directory, a refusal in the
// transaction, a repeat answered by another create's receipt (two creates
// at once under one op id and digest with other images), a context
// cancelled. After each the store's own sweep finds nothing to collect, no
// lease or temp file is left, and a file another holder names is kept.
// An attachment's failed sync answers as before, its file the sweep's.
func TestEveryWayOutOfAnUploadCollectsItsFile(t *testing.T) {
	ctx := context.Background()
	s, key := attachmentTicket(t)
	shared := pngOf(t, 8, 8, 50)
	mustPut(t, s, "shared", key, "shared.png", shared)
	settled := func(name string) {
		t.Helper()
		noUploads(t, s)
		before := contentFiles(t, s)
		for _, sum := range before {
			if named, err := isNamed(ctx, s.db, sum); err != nil || !named {
				t.Errorf("%s: the file %.12s is there and nothing names it (%v)", name, sum, err)
			}
		}
		if err := s.sweep(ctx, time.Now().Add(2*defaultSweepGrace)); err != nil {
			t.Fatal(err)
		}
		if after := contentFiles(t, s); !slices.Equal(after, before) {
			t.Errorf("%s: the sweep collected %v of %v", name, after, before)
		}
		if !exists(t, contentAt(s, sumOf(shared))) {
			t.Fatalf("%s: the attachment's file is gone", name)
		}
	}
	errSync := errors.New("the shard's sync failed")
	failSync := func(on bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.syncDir = syncDirectory
		if on {
			s.syncDir = func(p string) error {
				if len(filepath.Base(p)) == 2 {
					return errSync
				}
				return syncDirectory(p)
			}
		}
	}
	syncs := runtime.GOOS != "windows" // a Windows publish writes its move through and syncs nothing
	n, f := testCapture(true)
	if syncs {
		for i, img := range [][]byte{pngOf(t, 8, 8, 51), shared} {
			failSync(true)
			_, err := create(t, s, opID("sync", i), n, f, img)
			failSync(false)
			if !errors.Is(err, errSync) {
				t.Fatalf("a create whose sync fails: %v", err)
			}
			settled(fmt.Sprintf("a failed sync (%d)", i))
		}
	}
	// A refusal in the transaction: a retake of no capture.
	retake := n
	retake.RetakeOf = newID()
	rc, err := create(t, s, "refused", retake, f, pngOf(t, 8, 8, 52))
	want(t, rc, err, StatusFailed, RefusalInvalid)
	settled("a refusal")
	// Two creates at once under one op id and digest, other images: the
	// second answered by the first's receipt.
	a, b := pngOf(t, 8, 8, 53), pngOf(t, 8, 8, 54)
	digest := digestOf(t, n, b)
	var rcB Receipt
	var errB error
	s.beforeCommit = func() {
		s.beforeCommit = nil
		rcB, errB = s.CreateCapture(ctx, testOp("twice"), digest, n, f, bytes.NewReader(b))
	}
	rcA, errA := s.CreateCapture(ctx, testOp("twice"), digest, n, f, bytes.NewReader(a))
	want(t, rcB, errB, StatusDone, "")
	if errA != nil || rcA.Status != StatusDone || !rcA.Repeat || rcA.ID != rcB.ID {
		t.Fatalf("the repeat: %+v %v, the first %+v", rcA, errA, rcB)
	}
	if exists(t, contentAt(s, sumOf(a))) || !exists(t, contentAt(s, sumOf(b))) {
		t.Fatal("the repeat's file is kept, or the first's is gone")
	}
	settled("a repeat")
	// A context cancelled between the publication and the operation.
	cctx, cancel := context.WithCancel(ctx)
	s.beforeCommit = func() { s.beforeCommit = nil; cancel() }
	if _, err := s.CreateCapture(cctx, testOp("cancelled"), digestOf(t, n, pngOf(t, 8, 8, 55)), n, f, bytes.NewReader(pngOf(t, 8, 8, 55))); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled create: %v", err)
	}
	settled("a cancelled context")
	// An attachment whose sync fails answers its error, as before, and
	// leaves its file to the sweep.
	if syncs {
		content := []byte("an attachment's content")
		failSync(true)
		_, _, err := put(s, "attach-sync", key, "a.txt", content)
		failSync(false)
		if !errors.Is(err, errSync) || !exists(t, contentAt(s, sumOf(content))) {
			t.Fatalf("an attachment whose sync fails: %v, its file there %v", err, exists(t, contentAt(s, sumOf(content))))
		}
		if err := s.sweep(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		if exists(t, contentAt(s, sumOf(content))) {
			t.Fatal("the sweep kept a failed attachment's file")
		}
		settled("an attachment's failed sync")
	}
}

// The captures are listed newest first, a run's, a task's and an
// attempt's among them, in pages of at most MaxCapturePage by their
// cursor, a capture made and one deleted between two pages, the cursor's
// own capture deleted; a cursor that names no capture is refused, and a
// listing shows the first annotation's kind and note alone.
func TestCapturesAreListedNewestFirstByPage(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, storeDir(t))
	var ids []string
	add := func(name string, set func(n *NewCapture, f *CaptureFacts)) string {
		n, f := testCapture(false)
		if set != nil {
			set(&n, &f)
		}
		moment(&n)
		id := mustCreate(t, s, name, n, f, nil)
		// Each its own moment, a second after the one before.
		if _, err := s.db.Exec("UPDATE captures SET created_at = ? WHERE id = ?", stamp(momentAt.Add(time.Duration(len(ids))*time.Second)), id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	long := rep("é", 300)
	for i := range 3 {
		add(opID("attempt2-", i), func(n *NewCapture, _ *CaptureFacts) { n.Annotations[0].Note = long })
	}
	add("attempt3", func(_ *NewCapture, f *CaptureFacts) { f.Attempt = 3 })
	add("task", func(_ *NewCapture, f *CaptureFacts) { f.Task = "test" })
	add("run", func(_ *NewCapture, f *CaptureFacts) { f.Run = "crew-89abcdef" })
	add("link", func(n *NewCapture, f *CaptureFacts) { n.Label, *f = TreeRead{}, CaptureFacts{Origin: OriginURL} })
	newest := slices.Clone(ids)
	slices.Reverse(newest)
	list := func(f CaptureFilter, after string, limit int) CapturesPage {
		t.Helper()
		page, err := s.Captures(ctx, f, after, limit)
		if err != nil {
			t.Fatalf("%+v after %q: %v", f, after, err)
		}
		return page
	}
	idsOf := func(p CapturesPage) []string {
		var out []string
		for _, c := range p.Captures {
			out = append(out, c.ID)
		}
		return out
	}
	if got := idsOf(list(CaptureFilter{}, "", MaxCapturePage)); !slices.Equal(got, newest) {
		t.Fatalf("newest first: %v, want %v", got, newest)
	}
	run, task := "crew-0123abcd", "build"
	for _, c := range []struct {
		f    CaptureFilter
		want []string
	}{
		{CaptureFilter{Run: run}, []string{ids[4], ids[3], ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: run, Task: task}, []string{ids[3], ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: run, Task: task, Attempt: 2}, []string{ids[2], ids[1], ids[0]}},
		{CaptureFilter{Run: "crew-89abcdef"}, []string{ids[5]}},
	} {
		if got := idsOf(list(c.f, "", MaxCapturePage)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
	}
	for _, c := range []struct {
		f     CaptureFilter
		after string
		limit int
	}{
		{CaptureFilter{}, "", MaxCapturePage + 1}, {CaptureFilter{}, "", 0}, {CaptureFilter{Task: task}, "", 10},
		{CaptureFilter{Run: run, Attempt: 2}, "", 10}, {CaptureFilter{}, newID(), 10},
	} {
		if _, err := s.Captures(ctx, c.f, c.after, c.limit); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v after %q, %d: %v, want a bad query", c.f, c.after, c.limit, err)
		}
	}
	// Two pages and the rest: between the first and the second, a capture
	// made, one of the second page deleted and the cursor's own.
	first := list(CaptureFilter{}, "", 3)
	if !slices.Equal(idsOf(first), newest[:3]) || first.Next != newest[2] {
		t.Fatalf("the first page: %v next %q", idsOf(first), first.Next)
	}
	add("between", nil)
	for _, id := range []string{newest[3], newest[2]} {
		rc, err := s.DeleteCapture(ctx, testOp("delete-"+id), id)
		want(t, rc, err, StatusDone, "")
	}
	second := list(CaptureFilter{}, first.Next, 3)
	if !slices.Equal(idsOf(second), newest[4:7]) || second.Next != "" {
		t.Fatalf("the second page: %v next %q, want %v", idsOf(second), second.Next, newest[4:7])
	}
	c := second.Captures[2]
	if len(c.Annotations) != 1 || c.Annotations[0].Kind != AnnotationElement || c.Annotations[0].Note != rep("é", 200) ||
		c.Annotations[0].Element != nil || c.Annotations[0].Observed != nil {
		t.Fatalf("a listed capture's annotation: %+v", c.Annotations)
	}
}
